package telemetry

import (
	"context"
	"testing"
)

// TestInit_NoEndpoint_Noop verifies that Init with an empty endpoint returns no
// error and a callable no-op shutdown.
func TestInit_NoEndpoint_Noop(t *testing.T) {
	shutdownFn, err := Init(context.Background(), "")
	if err != nil {
		t.Fatalf("Init with empty endpoint: %v", err)
	}
	if shutdownFn == nil {
		t.Fatalf("Init returned nil shutdown function")
	}
	// Must be callable without panic.
	shutdownFn()
}

// TestInit_WithEndpoint_Fails_NoServer verifies that Init with a non-existent
// OTLP endpoint either returns an error or succeeds (OTLP gRPC connects lazily)
// without panicking, and that the returned shutdown function is always callable.
func TestInit_WithEndpoint_Fails_NoServer(t *testing.T) {
	shutdownFn, err := Init(context.Background(), "localhost:19999")
	// OTLP gRPC connects lazily — Init may succeed even with no server.
	// Either outcome is acceptable; we only require no panic and a callable shutdown.
	if err != nil {
		// Error path: acceptable, just log it.
		t.Logf("Init returned error (acceptable for missing server): %v", err)
		return
	}
	if shutdownFn == nil {
		t.Fatalf("Init returned nil shutdown function on success path")
	}
	shutdownFn()
}
