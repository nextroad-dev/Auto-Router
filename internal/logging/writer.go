package logging

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// The writer's fixed bounds. They are named constants rather than settings: the
// request path must have one, reproducible cost, and a deployment that could
// raise the queue would only move the point at which a slow database starts to
// stall the router. A full queue is dropped and counted, never awaited.
const (
	// DefaultQueueCapacity is how many events may wait to be written.
	DefaultQueueCapacity = 1024
	// defaultBatchSize is how many events one transaction may carry.
	defaultBatchSize = 100
	// defaultFlushInterval is how long a partial batch may wait.
	defaultFlushInterval = 200 * time.Millisecond
	// defaultRetentionInterval is how often expired rows are swept.
	defaultRetentionInterval = time.Hour
	// defaultRetentionBatch is how many rows one delete statement may remove.
	defaultRetentionBatch = 1000
	// defaultRetentionBatches bounds how many delete statements one sweep may
	// run. The sweep is a maintenance task: it must not hold the single write
	// connection long enough to delay request logging.
	defaultRetentionBatches = 20
	// closeRetryLimit bounds how many consecutive failures a shutdown flush
	// accepts before it gives up and reports the remaining events as dropped.
	closeRetryLimit = 3
	// operationTimeout bounds one batch insert or one retention statement.
	operationTimeout = 30 * time.Second
	// reportEvery is how often a repeated failure is reported. The first one is
	// always reported; after that the writer reports at most once per hundred so a
	// broken database cannot flood the process log.
	reportEvery = 100
)

// Store is the persistence seam. It is implemented by *storage.RoutingLog and
// deliberately has no query method: stage 8 writes and expires routing rows, and
// reading them is stage 9's Admin API and the -logs-check maintenance mode.
type Store interface {
	// Insert writes one batch of events atomically. A batch that cannot be
	// written must not leave a partial batch behind.
	Insert(ctx context.Context, events []Event) error
	// DeleteEventsBefore removes at most limit events older than cutoff,
	// returning how many rows were deleted.
	DeleteEventsBefore(ctx context.Context, cutoff time.Time, limit int) (int, error)
	// DeleteJevCallsBefore removes at most limit Jev traces older than cutoff,
	// returning how many rows were deleted.
	DeleteJevCallsBefore(ctx context.Context, cutoff time.Time, limit int) (int, error)
}

// AttemptStore is implemented by stores that persist upstream attempts. It is
// optional so existing Store implementations remain source-compatible.
type AttemptStore interface {
	InsertAttempts(context.Context, []Attempt) error
	DeleteAttemptsBefore(context.Context, time.Time, int) (int, error)
}

// Config is the writer's configuration. A zero bound selects the documented
// default, so a caller that only cares about retention cannot accidentally build
// an unbounded queue. A retention of zero days means "keep forever".
type Config struct {
	// ClientIP selects whether Event.ClientIP is stored. The writer clears the
	// field when it is false, so a disclosure that was never opted into cannot
	// reach the database even if a caller filled the field in.
	ClientIP bool
	// RetentionDays bounds how long a routing event is kept.
	RetentionDays int
	// JevRetentionDays bounds how long a Jev trace is kept. Traces have their own
	// deadline because the event is the audit record and the trace is the
	// diagnostic.
	JevRetentionDays int

	QueueCapacity     int
	BatchSize         int
	FlushInterval     time.Duration
	RetentionInterval time.Duration
	RetentionBatch    int
	RetentionBatches  int
	// Now supplies the current time. Nil means time.Now.
	Now func() time.Time
}

// withDefaults fills every zero bound with its documented default.
func (c Config) withDefaults() Config {
	if c.QueueCapacity <= 0 {
		c.QueueCapacity = DefaultQueueCapacity
	}
	if c.BatchSize <= 0 {
		c.BatchSize = defaultBatchSize
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = defaultFlushInterval
	}
	if c.RetentionInterval <= 0 {
		c.RetentionInterval = defaultRetentionInterval
	}
	if c.RetentionBatch <= 0 {
		c.RetentionBatch = defaultRetentionBatch
	}
	if c.RetentionBatches <= 0 {
		c.RetentionBatches = defaultRetentionBatches
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.RetentionDays < 0 {
		c.RetentionDays = 0
	}
	if c.JevRetentionDays < 0 {
		c.JevRetentionDays = 0
	}
	return c
}

// Writer records routing events asynchronously. The request path calls Record,
// which never blocks on the database: the event either fits in the bounded queue
// or is dropped and counted. One goroutine drains the queue, writes one
// transaction per batch and sweeps expired rows on the same timeline.
//
// A Writer is safe for concurrent use. Record after Close is a no-op, which is
// what makes shutdown deterministic: in-flight requests finish, their events are
// either flushed by Close or counted as dropped, and a late Record cannot
// resurrect the writer.
//
// Five settings are live rather than fixed at construction: enabled,
// store_client_ip, the Jev trace switch and the two retention bounds. They are
// atomic so Record can read them without a lock, and so an administrator who turns
// the log off (or the trace on) sees the change on the next request instead of
// after a restart. The bounded queue, the batch sizes and the flush interval stay
// constants: those decide the request path's fixed cost, and an operator must not
// be able to make a logged request block on the database by accident.
type Writer struct {
	store  Store
	config Config
	logger *slog.Logger
	// live holds the five runtime switches. They are read once per Record and once
	// per sweep, so a change applies to the next event rather than to the batch in
	// flight.
	live liveSettings

	// ctx bounds every database call. Close cancels it when its own deadline
	// expires, so a database that stopped answering cannot leave the drain
	// goroutine alive past shutdown.
	ctx    context.Context
	cancel context.CancelFunc

	mutex    sync.Mutex
	queue    []Event
	attempts []Attempt
	closed   bool
	dropped  uint64
	failed   uint64
	written  uint64
	// signal wakes the drain goroutine when a batch is ready or Close ran.
	signal chan struct{}
	done   chan struct{}
}

// New builds a writer and starts its drain goroutine. A nil store or a nil
// logger still yields a usable writer: events are counted and reported as
// dropped rather than panicking on the request path.
//
// The writer starts recording. That is deliberate: a caller builds a writer when
// it wants a log, and the two runtime switches (SetEnabled, SetJevTraces) exist so
// an administrator can turn recording off, or the optional trace on, without
// restarting. The composition root applies the configured state immediately after
// construction, so the process still starts in exactly the state the configuration
// describes.
func New(store Store, cfg Config, logger *slog.Logger) *Writer {
	config := cfg.withDefaults()
	ctx, cancel := context.WithCancel(context.Background())
	writer := &Writer{
		store:  store,
		config: config,
		logger: logger,
		ctx:    ctx,
		cancel: cancel,
		queue:  make([]Event, 0, config.QueueCapacity),
		signal: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	// The initial live state mirrors the configuration the writer was built from,
	// including the retention bounds, so a writer that is never touched behaves
	// exactly like the stage-8 writer: recording on, tracing on, the client address
	// stored only when it was configured.
	writer.live.enabled.Store(true)
	writer.live.storeClientIP.Store(config.ClientIP)
	writer.live.jevTraces.Store(true)
	writer.live.retentionDays.Store(int64(cfg.RetentionDays))
	writer.live.jevRetentionDays.Store(int64(cfg.JevRetentionDays))
	go writer.run()
	return writer
}

// liveSettings is the writer's runtime state. Every field is read on the request
// path without a lock, which is what makes a live switch cost one atomic load.
type liveSettings struct {
	enabled          atomic.Bool
	storeClientIP    atomic.Bool
	jevTraces        atomic.Bool
	retentionDays    atomic.Int64
	jevRetentionDays atomic.Int64
}

// SetEnabled turns durable recording on or off. A disabled writer drops every
// event at the door, which is the documented "routing.log.enabled is false" state;
// the queue and the drain goroutine stay alive so the switch is reversible
// without a restart.
func (w *Writer) SetEnabled(enabled bool) {
	if w == nil {
		return
	}
	w.live.enabled.Store(enabled)
}

// Enabled reports whether the writer is currently recording.
func (w *Writer) Enabled() bool {
	if w == nil {
		return false
	}
	return w.live.enabled.Load()
}

// SetStoreClientIP decides whether the observed client address is stored. A
// disabled switch clears the field on every event, exactly as the constructor
// setting does.
func (w *Writer) SetStoreClientIP(enabled bool) {
	if w == nil {
		return
	}
	w.live.storeClientIP.Store(enabled)
}

// SetJevTraces decides whether the optional Jev trace is stored. Turning the
// switch off drops the trace and keeps everything else, including
// Event.JevLatencyMS: the latency belongs to the event, not to the trace, and
// "how long the call took" must not disappear with "which distribution came back".
func (w *Writer) SetJevTraces(enabled bool) {
	if w == nil {
		return
	}
	w.live.jevTraces.Store(enabled)
}

// SetRetention changes both retention bounds. Zero means "keep forever", which is
// the same value the configuration uses, so no separate "disabled" flag exists.
func (w *Writer) SetRetention(days, jevDays int) {
	if w == nil {
		return
	}
	if days < 0 {
		days = 0
	}
	if jevDays < 0 {
		jevDays = 0
	}
	w.live.retentionDays.Store(int64(days))
	w.live.jevRetentionDays.Store(int64(jevDays))
}

// Stats is the writer's observable state. It exists for the Admin API's health
// report, which has to say "the log is on, the queue holds this much and nothing is
// being dropped" without reading the queue itself.
type Stats struct {
	Enabled       bool
	QueueDepth    int
	QueueCapacity int
	Written       uint64
	Dropped       uint64
	Failed        uint64
	Closed        bool
}

// Stats returns a consistent snapshot of the writer's counters.
func (w *Writer) Stats() Stats {
	if w == nil {
		return Stats{}
	}
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return Stats{
		Enabled:       w.live.enabled.Load(),
		QueueDepth:    len(w.queue),
		QueueCapacity: w.config.QueueCapacity,
		Written:       w.written,
		Dropped:       w.dropped,
		Failed:        w.failed,
		Closed:        w.closed,
	}
}

// Record enqueues one event. It never blocks, never returns an error and never
// touches the database: a routing log that can slow down or fail a request is
// worse than a missing row. A full queue, a closed writer or an unusable event
// drops the record and counts it.
func (w *Writer) Record(event Event) {
	if w == nil {
		return
	}
	w.RecordWithSettings(event, w.live.enabled.Load(), w.live.storeClientIP.Load(), w.live.jevTraces.Load())
}

// RecordWithSettings queues an event using the settings snapshot captured when
// the request began, so a concurrent settings update applies to later requests.
func (w *Writer) RecordWithSettings(event Event, enabled, storeClientIP, jevTraces bool) {
	if w == nil {
		return
	}
	if !enabled {
		return
	}
	if !storeClientIP {
		event.ClientIP = ""
	}
	if !jevTraces {
		event.Jev = nil
	}
	if err := event.Valid(); err != nil {
		// An invalid event can only come from a caller bug. It is counted and
		// reported rather than written, because one bad row would abort the whole
		// batch it travelled in.
		w.countDrop("the event was rejected", err)
		return
	}
	w.mutex.Lock()
	if w.closed {
		w.mutex.Unlock()
		return
	}
	if len(w.queue) >= w.config.QueueCapacity {
		w.dropped++
		dropped := w.dropped
		w.mutex.Unlock()
		w.report("the routing log queue is full", dropped)
		return
	}
	w.queue = append(w.queue, event)
	ready := len(w.queue) >= w.config.BatchSize
	w.mutex.Unlock()
	if ready {
		w.wake()
	}
}

// RecordAttempt enqueues one upstream attempt without blocking on storage. It is
// safe to call as soon as an attempt completes; bodies and headers are not fields
// of Attempt and therefore cannot be persisted through this method.
func (w *Writer) RecordAttempt(attempt Attempt) {
	if w == nil || !w.live.enabled.Load() {
		return
	}
	if err := attempt.Valid(); err != nil {
		w.countDrop("the attempt was rejected", err)
		return
	}
	w.mutex.Lock()
	if w.closed {
		w.mutex.Unlock()
		return
	}
	if len(w.queue)+len(w.attempts) >= w.config.QueueCapacity {
		w.dropped++
		dropped := w.dropped
		w.mutex.Unlock()
		w.report("the routing log queue is full", dropped)
		return
	}
	w.attempts = append(w.attempts, attempt)
	ready := len(w.queue)+len(w.attempts) >= w.config.BatchSize
	w.mutex.Unlock()
	if ready {
		w.wake()
	}
}

// Dropped reports how many events were discarded because the queue was full, an
// event was invalid, or the writer was closed before they could be written.
func (w *Writer) Dropped() uint64 { return w.count(func() uint64 { return w.dropped }) }

// Failed reports how many batches could not be written.
func (w *Writer) Failed() uint64 { return w.count(func() uint64 { return w.failed }) }

// Written reports how many events were handed to a successful transaction.
func (w *Writer) Written() uint64 { return w.count(func() uint64 { return w.written }) }

func (w *Writer) count(read func() uint64) uint64 {
	if w == nil {
		return 0
	}
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return read()
}

// Close stops accepting events, flushes what is queued and waits for the drain
// goroutine. The flush is bounded by ctx: a database that no longer answers must
// not keep the process from shutting down. Events that could not be flushed are
// counted as dropped and reported, so shutdown is never silent about data loss.
//
// Close is idempotent.
func (w *Writer) Close(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.mutex.Lock()
	alreadyClosed := w.closed
	w.closed = true
	w.mutex.Unlock()
	w.wake()

	var flushErr error
	select {
	case <-w.done:
	case <-ctx.Done():
		// Cut the in-flight statement short so the drain goroutine can exit, then
		// wait for it: shutdown must not leak the writer.
		w.cancel()
		<-w.done
		flushErr = errors.New("the routing log did not flush before the shutdown deadline")
	}
	if alreadyClosed {
		return flushErr
	}

	w.mutex.Lock()
	pending := len(w.queue) + len(w.attempts)
	w.dropped += uint64(pending)
	w.queue = w.queue[:0]
	w.attempts = w.attempts[:0]
	written, dropped, failed := w.written, w.dropped, w.failed
	w.mutex.Unlock()
	if w.logger != nil && (pending > 0 || failed > 0) {
		// Individual failures were reported as they happened; this is the
		// shutdown total, which is the number an operator needs.
		w.logger.Warn("the routing log was closed",
			"written", written, "dropped", dropped, "unwritten_at_shutdown", pending, "failed_batches", failed)
	}
	return flushErr
}

// run is the single drain goroutine. One writer means one connection user, which
// is what the single-connection pool wants: retention sweeps and request batches
// are serialized instead of competing.
func (w *Writer) run() {
	defer close(w.done)
	flushTicker := time.NewTicker(w.config.FlushInterval)
	defer flushTicker.Stop()
	retentionTicker := time.NewTicker(w.config.RetentionInterval)
	defer retentionTicker.Stop()

	// A sweep runs once at startup so a process that restarts frequently still
	// expires old rows, even if it never stays up for a full retention interval.
	w.sweep()
	failures := 0
	for {
		select {
		case <-w.signal:
		case <-flushTicker.C:
		case <-retentionTicker.C:
			w.sweep()
		}
		// Drain in batch-sized transactions. A single wake must be able to write
		// more than one batch: a burst larger than BatchSize would otherwise wait
		// for the next tick.
		for {
			qlen, closed := w.state()
			if qlen == 0 {
				failures = 0
				break
			}
			written := w.flush()
			if written {
				failures = 0
				continue
			}
			failures++
			if !closed {
				// A live writer keeps the events queued and waits for the next
				// tick instead of spinning on a failing database.
				break
			}
			if failures >= closeRetryLimit {
				// Shutdown must terminate: the remaining events are reported as
				// dropped by Close instead of retried forever.
				if w.logger != nil {
					w.logger.Warn("the routing log could not flush before shutdown", "failed_batches", failures)
				}
				return
			}
		}
		qlen, closed := w.state()
		if closed && qlen == 0 {
			return
		}
	}
}

// state reports the queue length and whether the writer is closed, under one
// lock, so the drain loop cannot read a torn pair.
func (w *Writer) state() (int, bool) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return len(w.queue) + len(w.attempts), w.closed
}

// wake nudges the drain goroutine. The buffered channel makes a redundant wake
// free: a second signal while one is pending is dropped, which is exactly what a
// batch arriving between two flushes should do.
func (w *Writer) wake() {
	select {
	case w.signal <- struct{}{}:
	default:
	}
}

// flush writes one batch and reports whether it was written. A failed batch goes
// back to the front of the queue, so a transient database error delays an event
// instead of losing it; if the queue is then over capacity, its oldest events are
// dropped and counted, which is the same policy Record applies when the queue is
// full.
func (w *Writer) flush() bool {
	w.mutex.Lock()
	if len(w.attempts) > 0 {
		size := len(w.attempts)
		if size > w.config.BatchSize {
			size = w.config.BatchSize
		}
		batch := append([]Attempt(nil), w.attempts[:size]...)
		copy(w.attempts, w.attempts[size:])
		w.attempts = w.attempts[:len(w.attempts)-size]
		w.mutex.Unlock()
		store, ok := w.store.(AttemptStore)
		var err error
		if !ok {
			err = errors.New("the routing log store does not support attempts")
		} else {
			ctx, cancel := context.WithTimeout(w.ctx, operationTimeout)
			err = store.InsertAttempts(ctx, batch)
			cancel()
		}
		if err != nil {
			return w.requeueAttempts(batch, err)
		}
		w.mutex.Lock()
		w.written += uint64(len(batch))
		w.mutex.Unlock()
		return true
	}
	if len(w.queue) == 0 {
		w.mutex.Unlock()
		return false
	}
	size := len(w.queue)
	if size > w.config.BatchSize {
		size = w.config.BatchSize
	}
	batch := make([]Event, size)
	copy(batch, w.queue[:size])
	remaining := len(w.queue) - size
	copy(w.queue, w.queue[size:])
	w.queue = w.queue[:remaining]
	w.mutex.Unlock()

	if err := w.insert(batch); err != nil {
		return w.requeue(batch, err)
	}
	w.mutex.Lock()
	w.written += uint64(len(batch))
	w.mutex.Unlock()
	return true
}

func (w *Writer) requeueAttempts(batch []Attempt, err error) bool {
	w.mutex.Lock()
	w.failed++
	failed := w.failed
	merged := make([]Attempt, 0, len(batch)+len(w.attempts))
	merged = append(merged, batch...)
	merged = append(merged, w.attempts...)
	if overflow := len(merged) + len(w.queue) - w.config.QueueCapacity; overflow > 0 {
		if overflow >= len(merged) {
			merged = nil
		} else {
			merged = merged[overflow:]
		}
		w.dropped += uint64(overflow)
	}
	w.attempts = merged
	w.mutex.Unlock()
	w.reportBatchFailure(err, len(batch), failed)
	return false
}

// insert writes one batch with a deadline. The deadline is what bounds a batch
// whose database has stopped answering.
func (w *Writer) insert(batch []Event) error {
	if w.store == nil {
		return errors.New("no routing log store is configured")
	}
	ctx, cancel := context.WithTimeout(w.ctx, operationTimeout)
	defer cancel()
	return w.store.Insert(ctx, batch)
}

// requeue puts a failed batch back at the front of the queue and reports the
// failure.
func (w *Writer) requeue(batch []Event, err error) bool {
	w.mutex.Lock()
	w.failed++
	failed := w.failed
	merged := make([]Event, 0, len(batch)+len(w.queue))
	merged = append(merged, batch...)
	merged = append(merged, w.queue...)
	if overflow := len(merged) - w.config.QueueCapacity; overflow > 0 {
		merged = merged[overflow:]
		w.dropped += uint64(overflow)
	}
	w.queue = merged
	w.mutex.Unlock()
	// The first failure is reported in full; after that the cause is only repeated
	// once per reportEvery failures, so a permanently broken sink stays visible
	// without flooding the process log.
	w.reportBatchFailure(err, len(batch), failed)
	return false
}

// reportBatchFailure reports a failed batch at a bounded rate.
func (w *Writer) reportBatchFailure(err error, events int, count uint64) {
	if w.logger == nil {
		return
	}
	if count == 1 || count%reportEvery == 0 {
		w.logger.Warn("a routing log batch could not be written",
			"error", err.Error(), "events", events, "failed_batches", count)
	}
}

// sweep deletes expired rows in bounded batches. A section whose retention is
// disabled never runs, and a sweep stops as soon as a batch deletes fewer rows
// than the batch size, so an empty table costs one statement.
func (w *Writer) sweep() {
	if w.store == nil {
		return
	}
	now := w.config.Now()
	// The retention bounds are read per sweep, so shortening a retention period
	// starts deleting on the next pass instead of after a restart.
	if days := w.live.retentionDays.Load(); days > 0 {
		cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
		w.deleteBatches(cutoff, w.store.DeleteEventsBefore)
		if attemptStore, ok := w.store.(AttemptStore); ok {
			w.deleteBatches(cutoff, attemptStore.DeleteAttemptsBefore)
		}
	}
	if days := w.live.jevRetentionDays.Load(); days > 0 {
		cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
		w.deleteBatches(cutoff, w.store.DeleteJevCallsBefore)
	}
}

// deleteBatches runs one bounded retention sweep against one table.
func (w *Writer) deleteBatches(cutoff time.Time, remove func(context.Context, time.Time, int) (int, error)) {
	for batch := 0; batch < w.config.RetentionBatches; batch++ {
		ctx, cancel := context.WithTimeout(w.ctx, operationTimeout)
		deleted, err := remove(ctx, cutoff, w.config.RetentionBatch)
		cancel()
		if err != nil {
			// Retention is maintenance: a failure delays deletion and must never
			// affect request logging.
			if w.logger != nil {
				w.logger.Warn("routing log retention sweep failed", "error", err.Error())
			}
			return
		}
		if deleted < w.config.RetentionBatch {
			return
		}
	}
}

// countDrop records a rejection that happened before the queue.
func (w *Writer) countDrop(detail string, err error) {
	w.mutex.Lock()
	w.dropped++
	dropped := w.dropped
	w.mutex.Unlock()
	if w.logger != nil {
		w.logger.Warn("routing log event was dropped", "detail", detail, "error", err.Error(), "dropped_total", dropped)
	}
}

// report logs a failure at most once per reportEvery occurrences, plus the first
// one. A permanently broken sink must stay visible without flooding the log.
func (w *Writer) report(detail string, count uint64) {
	if w.logger == nil {
		return
	}
	if count == 1 || count%reportEvery == 0 {
		w.logger.Warn("routing log write failure", "detail", detail, "failures_total", count)
	}
}
