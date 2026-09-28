package direct

import (
	"context"
	"errors"
	"testing"

	"github.com/nextroad-dev/Auto-Router/internal/providers"
)

type timeoutFailure struct{}

func (timeoutFailure) Error() string   { return "dial timed out" }
func (timeoutFailure) Timeout() bool   { return true }
func (timeoutFailure) Temporary() bool { return true }

func TestClassifyConnectTimeoutAsSafePreRequestFailure(t *testing.T) {
	classified := classify(context.Background(), &requestTrace{}, timeoutFailure{})
	if !errors.Is(classified, providers.ErrUpstreamTimeout) {
		t.Fatalf("classification %v does not preserve timeout identity", classified)
	}
	if !errors.Is(classified, providers.ErrPreRequestFailure) {
		t.Fatalf("classification %v does not identify safe pre-request retry", classified)
	}
}

func TestClassifyConnectedTimeoutAsUncertain(t *testing.T) {
	trace := &requestTrace{}
	trace.gotConn.Store(true)
	classified := classify(context.Background(), trace, timeoutFailure{})
	if !errors.Is(classified, providers.ErrUpstreamTimeout) {
		t.Fatalf("classification %v does not preserve timeout identity", classified)
	}
	if errors.Is(classified, providers.ErrPreRequestFailure) {
		t.Fatalf("connected timeout %v was incorrectly marked safe to retry", classified)
	}
}

func TestClassifyCancellationNeverAsPreRequestRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	classified := classify(ctx, &requestTrace{}, context.Canceled)
	if !errors.Is(classified, providers.ErrCanceled) {
		t.Fatalf("classification %v does not preserve cancellation", classified)
	}
	if errors.Is(classified, providers.ErrPreRequestFailure) {
		t.Fatalf("canceled request %v was incorrectly marked safe to retry", classified)
	}
}
