package main

import (
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// The Windows Go runtime cannot raise os.Interrupt for the current process, so
// this verifies the real signal-to-shutdown wiring on Unix CI while skipping
// locally. Graceful draining itself is covered by the serve tests.
func TestSignalWiringCancelsContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows cannot deliver os.Interrupt to the current process")
	}
	ctx, stop := notifyContext(t.Context())
	defer stop()
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	select {
	case <-ctx.Done():
		if err := ctx.Err(); err == nil {
			t.Fatal("cancellation must carry a reason")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("signal did not cancel the shutdown context")
	}
}
