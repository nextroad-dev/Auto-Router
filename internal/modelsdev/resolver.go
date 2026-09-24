package modelsdev

import (
	"context"
	"errors"
	"sync"
	"time"
)

// MetadataResolver caches the public models.dev catalog for a bounded interval.
// Administrative selections therefore do not download the multi-megabyte catalog
// for each clicked model.
type MetadataResolver struct {
	client *Client
	ttl    time.Duration

	mu        sync.Mutex
	document  Document
	expiresAt time.Time
}

// NewMetadataResolver builds a lazy resolver. It does not make a network request
// until metadata is requested.
func NewMetadataResolver(rawURL string, timeout, ttl time.Duration) (*MetadataResolver, error) {
	client, err := NewClient(rawURL, timeout)
	if err != nil {
		return nil, err
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return &MetadataResolver{client: client, ttl: ttl}, nil
}

// Resolve looks up an upstream identifier in models.dev. A provider key is only
// a ranking hint because custom OpenAI-compatible provider names often differ from
// the catalog's provider ID.
func (r *MetadataResolver) Resolve(ctx context.Context, providerHint, upstreamID string) (MetadataMatch, bool, error) {
	if r == nil || r.client == nil {
		return MetadataMatch{}, false, errors.New("models.dev metadata resolver is unavailable")
	}
	r.mu.Lock()
	if r.document == nil || !time.Now().Before(r.expiresAt) {
		payload, err := r.client.Fetch(ctx)
		if err == nil {
			var document Document
			document, err = Decode(payload)
			if err == nil {
				r.document = document
				r.expiresAt = time.Now().Add(r.ttl)
			}
		}
		// A previously fetched catalog remains usable during a transient outage.
		if err != nil && r.document == nil {
			r.mu.Unlock()
			return MetadataMatch{}, false, err
		}
	}
	document := r.document
	r.mu.Unlock()
	match, found := FindMetadata(document, providerHint, upstreamID)
	return match, found, nil
}
