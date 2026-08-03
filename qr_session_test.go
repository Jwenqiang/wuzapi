package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestQRSessionStateWaitsUntilFirstCodeIsStored(t *testing.T) {
	state := newQRSessionState()
	done := make(chan error, 1)
	go func() {
		done <- state.waitForFirstCode(context.Background())
	}()

	select {
	case err := <-done:
		t.Fatalf("waitForFirstCode returned before a QR code was stored: %v", err)
	default:
	}

	state.markFirstCodeReady()
	if err := <-done; err != nil {
		t.Fatalf("waitForFirstCode() error = %v", err)
	}
	if !state.isFirstCodeReady() {
		t.Fatal("state did not report the stored QR code as ready")
	}
}

func TestQRSessionStateFailureNeverReportsReady(t *testing.T) {
	state := newQRSessionState()
	state.markFailed(errQRSessionTimedOut)

	if err := state.waitForFirstCode(context.Background()); !errors.Is(err, errQRSessionTimedOut) {
		t.Fatalf("waitForFirstCode() error = %v; want timeout", err)
	}
	if state.isFirstCodeReady() {
		t.Fatal("failed QR session reported a ready code")
	}
}

func TestQRSessionStateWaitHonorsContextCancellation(t *testing.T) {
	state := newQRSessionState()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	if err := state.waitForFirstCode(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waitForFirstCode() error = %v; want deadline exceeded", err)
	}
}
