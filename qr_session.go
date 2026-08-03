package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/patrickmn/go-cache"
	"go.mau.fi/whatsmeow"
)

var (
	errQRSessionTimedOut    = errors.New("QR session timed out")
	errQRSessionUnavailable = errors.New("QR session unavailable")
	errQRSessionReplaced    = errors.New("QR session replaced")
	errQRSessionAuthorized  = errors.New("QR session already authorized")
)

type qrLoginStarter func(userID, token string, kill chan bool, ready chan<- *MyClient)

type phonePairer func(context.Context, *MyClient, string) (string, error)

const phonePairWaitTimeout = 30 * time.Second

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

func (s *server) getCurrentSessionQRCode(ctx context.Context, mycli *MyClient) (string, error) {
	if s == nil || s.db == nil || mycli == nil {
		return "", errQRSessionUnavailable
	}
	if err := mycli.qrSession.waitForFirstCode(ctx); err != nil {
		return "", err
	}

	var code string
	if err := s.db.GetContext(ctx, &code, `SELECT qrcode FROM users WHERE id=$1`, mycli.userID); err != nil {
		return "", err
	}
	if strings.TrimSpace(code) == "" {
		return "", errQRSessionUnavailable
	}
	return code, nil
}

func (s *server) startFreshQRLogin(ctx context.Context, userID, jid, token string) (*MyClient, error) {
	if s == nil || s.db == nil {
		return nil, errQRSessionUnavailable
	}
	s.qrLoginMu.Lock()
	defer s.qrLoginMu.Unlock()
	if strings.TrimSpace(jid) != "" {
		return nil, errQRSessionAuthorized
	}

	var storedJID string
	if err := s.db.GetContext(ctx, &storedJID, `SELECT COALESCE(jid, '') FROM users WHERE id=$1`, userID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(storedJID) != "" {
		return nil, errQRSessionAuthorized
	}

	previous := clientManager.GetMyClient(userID)
	kill := make(chan bool, 1)
	killchannelMu.Lock()
	previousKill := killchannel[userID]
	killchannel[userID] = kill
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET qrcode='' WHERE id=$1`, userID); err != nil {
		if previousKill == nil {
			delete(killchannel, userID)
		} else {
			killchannel[userID] = previousKill
		}
		killchannelMu.Unlock()
		return nil, err
	}
	killchannelMu.Unlock()
	if previousKill == nil && previous != nil {
		previousKill = previous.loginKill
	}
	if userinfo, found := userinfocache.Get(token); found {
		userinfocache.Set(token, updateUserInfo(userinfo, "Qrcode", ""), cache.NoExpiration)
	}
	if previous != nil {
		previous.qrSession.markFailed(errQRSessionReplaced)
		if previous.WAClient != nil {
			previous.WAClient.Disconnect()
		}
	}
	if previousKill != nil {
		select {
		case previousKill <- true:
		default:
		}
	}

	ready := make(chan *MyClient, 1)
	if s.startQRLogin != nil {
		s.startQRLogin(userID, token, kill, ready)
	} else {
		go s.startClientWithReady(userID, "", token, kill, ready)
	}

	select {
	case mycli := <-ready:
		if mycli == nil {
			return nil, errQRSessionUnavailable
		}
		return mycli, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func defaultPhonePairer(ctx context.Context, mycli *MyClient, phone string) (string, error) {
	if mycli == nil || mycli.WAClient == nil {
		return "", errQRSessionUnavailable
	}
	return mycli.WAClient.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
}

func (s *server) preparePhonePairing(ctx context.Context, mycli *MyClient, phone string) (string, error) {
	if s == nil || mycli == nil {
		return "", errQRSessionUnavailable
	}
	pairCtx, cancel := context.WithTimeout(ctx, phonePairWaitTimeout)
	defer cancel()
	if err := mycli.qrSession.waitForFirstCode(pairCtx); err != nil {
		return "", err
	}

	pairer := s.phonePairer
	if pairer == nil {
		pairer = defaultPhonePairer
	}
	return pairer(pairCtx, mycli, phone)
}
