package main

import (
	"context"
	"errors"
	"sync"
)

var (
	errQRSessionTimedOut    = errors.New("QR session timed out")
	errQRSessionUnavailable = errors.New("QR session unavailable")
	errQRSessionReplaced    = errors.New("QR session replaced")
)

// qrSessionState records the result of the current unauthenticated QR login
// session. The first terminal event wins: a QR code is persisted successfully,
// or the session fails before it can produce one.
type qrSessionState struct {
	done  chan struct{}
	once  sync.Once
	mu    sync.RWMutex
	err   error
	ready bool
}

func newQRSessionState() *qrSessionState {
	return &qrSessionState{done: make(chan struct{})}
}

func (state *qrSessionState) markFirstCodeReady() {
	if state == nil {
		return
	}
	state.once.Do(func() {
		state.mu.Lock()
		state.ready = true
		state.mu.Unlock()
		close(state.done)
	})
}

func (state *qrSessionState) markFailed(err error) {
	if state == nil {
		return
	}
	if err == nil {
		err = errQRSessionUnavailable
	}
	state.once.Do(func() {
		state.mu.Lock()
		state.err = err
		state.mu.Unlock()
		close(state.done)
	})
}

func (state *qrSessionState) waitForFirstCode(ctx context.Context) error {
	if state == nil {
		return errQRSessionUnavailable
	}
	select {
	case <-state.done:
		state.mu.RLock()
		defer state.mu.RUnlock()
		if state.ready {
			return nil
		}
		if state.err != nil {
			return state.err
		}
		return errQRSessionUnavailable
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (state *qrSessionState) isFirstCodeReady() bool {
	if state == nil {
		return false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.ready
}
