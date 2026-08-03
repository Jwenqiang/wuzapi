package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestPreparePhonePairingWaitsForFreshQRCode(t *testing.T) {
	s := makeTestServer(t)
	state := newQRSessionState()
	mycli := &MyClient{userID: "phone-pair-wait-user", qrSession: state}
	called := false
	s.phonePairer = func(context.Context, *MyClient, string) (string, error) {
		called = true
		return "ABCD-EFGH", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err := s.preparePhonePairing(ctx, mycli, "8610000000000")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("preparePhonePairing() error = %v; want deadline exceeded", err)
	}
	if called {
		t.Fatal("PairPhone was called before a fresh QR code was ready")
	}
}

func TestPreparePhonePairingReturnsLinkingCodeAfterFreshQRCode(t *testing.T) {
	s := makeTestServer(t)
	state := newQRSessionState()
	state.markFirstCodeReady()
	mycli := &MyClient{userID: "phone-pair-ready-user", qrSession: state}
	called := false
	s.phonePairer = func(_ context.Context, gotClient *MyClient, gotPhone string) (string, error) {
		called = true
		if gotClient != mycli {
			t.Fatal("phone pairer received a different client")
		}
		if gotPhone != "8610000000000" {
			t.Fatalf("phone pairer phone = %q; want normalized input", gotPhone)
		}
		return "ABCD-EFGH", nil
	}

	got, err := s.preparePhonePairing(context.Background(), mycli, "8610000000000")
	if err != nil {
		t.Fatalf("preparePhonePairing() error = %v", err)
	}
	if !called {
		t.Fatal("PairPhone was not called after the fresh QR code became ready")
	}
	if got != "ABCD-EFGH" {
		t.Fatalf("preparePhonePairing() = %q; want linking code", got)
	}
}

func TestPreparePhonePairingSkipsRequestAfterQRSessionFailure(t *testing.T) {
	s := makeTestServer(t)
	state := newQRSessionState()
	state.markFailed(errQRSessionTimedOut)
	mycli := &MyClient{userID: "phone-pair-failed-user", qrSession: state}
	called := false
	s.phonePairer = func(context.Context, *MyClient, string) (string, error) {
		called = true
		return "", nil
	}

	_, err := s.preparePhonePairing(context.Background(), mycli, "8610000000000")
	if !errors.Is(err, errQRSessionTimedOut) {
		t.Fatalf("preparePhonePairing() error = %v; want QR timeout", err)
	}
	if called {
		t.Fatal("PairPhone was called after the QR session failed")
	}
}

func TestPairPhoneStartsFreshSessionBeforeCreatingLinkingCode(t *testing.T) {
	const (
		userID = "pair-phone-fresh-session-user"
		token  = "pair-phone-fresh-session-token"
	)
	s := makeTestServer(t)
	seedSessionQRCode(t, s, userID, "persisted-old-code")
	t.Cleanup(func() {
		clientManager.DeleteWhatsmeowClient(userID)
		clientManager.DeleteMyClient(userID)
		clientManager.DeleteHTTPClient(userID)
	})

	state := newQRSessionState()
	state.markFirstCodeReady()
	freshClient := &MyClient{userID: userID, db: s.db, qrSession: state}
	started := false
	s.startQRLogin = func(gotUserID, gotToken string, kill chan bool, ready chan<- *MyClient) {
		started = true
		if gotUserID != userID || gotToken != token {
			t.Fatalf("fresh login got user=%q token=%q", gotUserID, gotToken)
		}
		ready <- freshClient
	}
	s.phonePairer = func(_ context.Context, gotClient *MyClient, gotPhone string) (string, error) {
		if !started {
			t.Fatal("PairPhone ran before the fresh session was started")
		}
		if gotClient != freshClient || gotPhone != "8610000000000" {
			t.Fatalf("unexpected pairing request client=%p phone=%q", gotClient, gotPhone)
		}
		return "ABCD-EFGH", nil
	}

	req := httptest.NewRequest(http.MethodPost, "/session/pairphone", strings.NewReader(`{"Phone":"8610000000000"}`))
	ctx := context.WithValue(req.Context(), "userinfo", Values{map[string]string{
		"Id":    userID,
		"Jid":   "",
		"Token": token,
	}})
	req = req.WithContext(ctx)
	recorder := httptest.NewRecorder()

	s.PairPhone().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PairPhone status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data struct {
			LinkingCode string `json:"LinkingCode"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode PairPhone response: %v", err)
	}
	if response.Data.LinkingCode != "ABCD-EFGH" {
		t.Fatalf("PairPhone LinkingCode = %q", response.Data.LinkingCode)
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
