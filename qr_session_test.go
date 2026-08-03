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

func TestGetCurrentSessionQRCodeDoesNotReturnPersistedOldCode(t *testing.T) {
	const userID = "current-qr-old-code-user"
	s := makeTestServer(t)
	seedSessionQRCode(t, s, userID, "persisted-old-code")

	mycli := &MyClient{
		userID:    userID,
		db:        s.db,
		qrSession: newQRSessionState(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	_, err := s.getCurrentSessionQRCode(ctx, mycli)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("getCurrentSessionQRCode() error = %v; want deadline exceeded", err)
	}
}

func TestGetCurrentSessionQRCodeReturnsFreshStoredCode(t *testing.T) {
	const (
		userID = "current-qr-fresh-code-user"
		want   = "fresh-current-code"
	)
	s := makeTestServer(t)
	seedSessionQRCode(t, s, userID, want)

	state := newQRSessionState()
	state.markFirstCodeReady()
	mycli := &MyClient{userID: userID, db: s.db, qrSession: state}

	got, err := s.getCurrentSessionQRCode(context.Background(), mycli)
	if err != nil {
		t.Fatalf("getCurrentSessionQRCode() error = %v", err)
	}
	if got != want {
		t.Fatalf("getCurrentSessionQRCode() = %q; want %q", got, want)
	}
}

func TestGetCurrentSessionQRCodeRejectsEmptyStoredCode(t *testing.T) {
	const userID = "current-qr-empty-code-user"
	s := makeTestServer(t)
	seedSessionQRCode(t, s, userID, "")

	state := newQRSessionState()
	state.markFirstCodeReady()
	mycli := &MyClient{userID: userID, db: s.db, qrSession: state}

	_, err := s.getCurrentSessionQRCode(context.Background(), mycli)
	if !errors.Is(err, errQRSessionUnavailable) {
		t.Fatalf("getCurrentSessionQRCode() error = %v; want unavailable", err)
	}
}

func TestStartFreshQRLoginClearsOldCodeAndReplacesPreviousSession(t *testing.T) {
	const (
		userID = "fresh-qr-login-user"
		token  = "fresh-qr-login-token"
	)
	s := makeTestServer(t)
	seedSessionQRCode(t, s, userID, "persisted-old-code")

	oldState := newQRSessionState()
	oldClient := &MyClient{userID: userID, db: s.db, qrSession: oldState}
	clientManager.SetMyClient(userID, oldClient)
	t.Cleanup(func() { clientManager.DeleteMyClient(userID) })

	observedCode := "not-read"
	freshClient := &MyClient{userID: userID, db: s.db, qrSession: newQRSessionState()}
	s.startQRLogin = func(userID, token string, kill chan bool, ready chan<- *MyClient) {
		if err := s.db.Get(&observedCode, `SELECT qrcode FROM users WHERE id=$1`, userID); err != nil {
			t.Errorf("read QR code before fresh start: %v", err)
			return
		}
		ready <- freshClient
	}

	got, err := s.startFreshQRLogin(context.Background(), userID, "", token)
	if err != nil {
		t.Fatalf("startFreshQRLogin() error = %v", err)
	}
	if got != freshClient {
		t.Fatal("startFreshQRLogin() did not return the current fresh client")
	}
	if observedCode != "" {
		t.Fatalf("fresh login started while persisted QR code was still %q", observedCode)
	}
	if err := oldState.waitForFirstCode(context.Background()); !errors.Is(err, errQRSessionReplaced) {
		t.Fatalf("old session wait error = %v; want replacement", err)
	}
}

func seedSessionQRCode(t *testing.T, s *server, userID, code string) {
	t.Helper()
	if _, err := s.db.Exec(
		`INSERT INTO users (id, name, token, jid, qrcode, connected) VALUES ($1,$2,$3,$4,$5,$6)`,
		userID, "tester", userID+"-token", "", code, 0,
	); err != nil {
		t.Fatalf("seed session QR code: %v", err)
	}
}
