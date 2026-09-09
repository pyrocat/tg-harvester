// Package app contains the use cases of tg-harvester and the ports
// (interfaces) they consume. It depends only on the domain package.
package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"tg-harvester/internal/domain"
)

// TelegramRunner runs a Telegram client session.
type TelegramRunner interface {
	// Run connects, ensures the account is authorized (asking p for the code
	// and password when needed), calls ready and then blocks until ctx is
	// cancelled or the connection fails. A fresh client is used per call.
	Run(ctx context.Context, creds domain.Credentials, p AuthPrompter, ready func(account string)) error
	// Logout terminates the current authorization on Telegram's side.
	Logout(ctx context.Context) error
}

// AuthPrompter is asked for interactive login inputs by the TelegramRunner.
type AuthPrompter interface {
	AskCode(ctx context.Context, hint string) (string, error)
	AskPassword(ctx context.Context) (string, error)
}

// CredentialStore persists the Telegram credentials and session.
type CredentialStore interface {
	Credentials(ctx context.Context) (domain.Credentials, error)
	SaveCredentials(ctx context.Context, c domain.Credentials) error
	DeleteSession(ctx context.Context) error
}

// SessionManager owns the Telegram login state machine and supervises the
// client goroutine. It implements AuthPrompter to bridge the web UI and the
// interactive login flow.
type SessionManager struct {
	runner TelegramRunner
	store  CredentialStore
	log    *slog.Logger

	minBackoff time.Duration
	maxBackoff time.Duration
	// submitTimeout bounds how long SubmitCode/SubmitPassword wait for the
	// login flow to pick up the answer.
	submitTimeout time.Duration

	// lc serializes start/stop of the client goroutine.
	lc sync.Mutex
	// mu guards the fields below.
	mu      sync.Mutex
	base    context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	answers chan string
	status  domain.AuthStatus
}

// NewSessionManager creates a SessionManager. Call Run to activate it.
func NewSessionManager(runner TelegramRunner, store CredentialStore, log *slog.Logger) *SessionManager {
	return &SessionManager{
		runner:        runner,
		store:         store,
		log:           log,
		minBackoff:    time.Second,
		maxBackoff:    5 * time.Minute,
		submitTimeout: 2 * time.Second,
	}
}

// Run activates the manager, auto-starts the client when credentials exist,
// and blocks until ctx is cancelled.
func (m *SessionManager) Run(ctx context.Context) error {
	creds, err := m.store.Credentials(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.base = ctx
	m.status = domain.AuthStatus{State: domain.AuthNotConfigured, Phone: creds.Phone}
	m.mu.Unlock()

	if creds.IsSet() {
		if err := m.start(creds); err != nil {
			return err
		}
	}
	<-ctx.Done()
	m.stop()
	m.update(func(s *domain.AuthStatus) { s.State = domain.AuthStopped })
	return nil
}

// Configure stores new credentials and (re)starts the login flow.
func (m *SessionManager) Configure(ctx context.Context, creds domain.Credentials) error {
	if err := creds.Validate(); err != nil {
		return err
	}
	old, err := m.store.Credentials(ctx)
	if err != nil {
		return err
	}
	if err := m.store.SaveCredentials(ctx, creds); err != nil {
		return err
	}
	if old.Phone != creds.Phone || old.APIID != creds.APIID {
		if err := m.store.DeleteSession(ctx); err != nil {
			return err
		}
	}
	return m.start(creds)
}

// Retry restarts the client with the stored credentials after a failure.
func (m *SessionManager) Retry(ctx context.Context) error {
	creds, err := m.store.Credentials(ctx)
	if err != nil {
		return err
	}
	if !creds.IsSet() {
		return domain.ErrNotConfigured
	}
	return m.start(creds)
}

// Logout ends the Telegram session and forgets it; credentials are kept.
func (m *SessionManager) Logout(ctx context.Context) error {
	m.lc.Lock()
	defer m.lc.Unlock()
	if m.Authorized() {
		if err := m.runner.Logout(ctx); err != nil {
			m.log.Warn("telegram logout failed; dropping local session anyway", "err", err)
		}
	}
	m.stopLocked()
	if err := m.store.DeleteSession(ctx); err != nil {
		return err
	}
	m.update(func(s *domain.AuthStatus) {
		*s = domain.AuthStatus{State: domain.AuthNotConfigured, Phone: s.Phone}
	})
	return nil
}

// Credentials returns the stored credentials for display in the UI.
func (m *SessionManager) Credentials(ctx context.Context) (domain.Credentials, error) {
	return m.store.Credentials(ctx)
}

// Status returns a snapshot of the login state.
func (m *SessionManager) Status() domain.AuthStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// Authorized reports whether the client is connected and logged in.
func (m *SessionManager) Authorized() bool {
	return m.Status().State == domain.AuthAuthorized
}

// SubmitCode hands the confirmation code to the waiting login flow.
func (m *SessionManager) SubmitCode(code string) error {
	return m.submit(domain.AuthAwaitingCode, code)
}

// SubmitPassword hands the two-factor password to the waiting login flow.
func (m *SessionManager) SubmitPassword(password string) error {
	return m.submit(domain.AuthAwaitingPassword, password)
}

// AskCode implements AuthPrompter.
func (m *SessionManager) AskCode(ctx context.Context, hint string) (string, error) {
	return m.ask(ctx, domain.AuthAwaitingCode, hint)
}

// AskPassword implements AuthPrompter.
func (m *SessionManager) AskPassword(ctx context.Context) (string, error) {
	return m.ask(ctx, domain.AuthAwaitingPassword, "")
}

func (m *SessionManager) ask(ctx context.Context, state domain.AuthState, hint string) (string, error) {
	m.mu.Lock()
	m.status.State = state
	m.status.CodeHint = hint
	m.status.Err = ""
	answers := m.answers
	m.mu.Unlock()

	select {
	case answer := <-answers:
		m.update(func(s *domain.AuthStatus) { s.State = domain.AuthConnecting })
		return answer, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (m *SessionManager) submit(want domain.AuthState, answer string) error {
	m.mu.Lock()
	state := m.status.State
	answers := m.answers
	m.mu.Unlock()
	if state != want || answers == nil {
		return &domain.ValidationError{Msg: "the login flow is not waiting for this input (current state: " + state.String() + ")"}
	}
	select {
	case answers <- answer:
		return nil
	case <-time.After(m.submitTimeout):
		return errors.New("login flow did not accept the input; try again")
	}
}

func (m *SessionManager) start(creds domain.Credentials) error {
	m.lc.Lock()
	defer m.lc.Unlock()
	m.stopLocked()

	m.mu.Lock()
	if m.base == nil {
		m.mu.Unlock()
		return errors.New("session manager is not running")
	}
	ctx, cancel := context.WithCancel(m.base)
	done := make(chan struct{})
	m.cancel, m.done = cancel, done
	m.answers = make(chan string)
	m.status = domain.AuthStatus{State: domain.AuthConnecting, Phone: creds.Phone}
	m.mu.Unlock()

	go m.supervise(ctx, creds, done)
	return nil
}

func (m *SessionManager) stop() {
	m.lc.Lock()
	defer m.lc.Unlock()
	m.stopLocked()
}

// stopLocked cancels the running client and waits for it; lc must be held.
func (m *SessionManager) stopLocked() {
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.cancel, m.done = nil, nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (m *SessionManager) supervise(ctx context.Context, creds domain.Credentials, done chan struct{}) {
	defer close(done)
	backoff := m.minBackoff
	for {
		err := m.runner.Run(ctx, creds, m, m.onReady)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errors.New("telegram client stopped unexpectedly")
		}
		if errors.Is(err, domain.ErrUnauthorized) {
			m.log.Warn("telegram authorization failed; waiting for the user", "err", err)
			if derr := m.store.DeleteSession(context.WithoutCancel(ctx)); derr != nil {
				m.log.Error("delete telegram session", "err", derr)
			}
			m.setFailed(err.Error())
			return
		}
		m.log.Error("telegram client failed; will reconnect", "err", err, "backoff", backoff)
		m.setFailed(err.Error() + " (reconnecting in " + backoff.String() + ")")
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, m.maxBackoff)
		m.update(func(s *domain.AuthStatus) { s.State = domain.AuthConnecting })
	}
}

func (m *SessionManager) onReady(account string) {
	m.update(func(s *domain.AuthStatus) {
		s.State = domain.AuthAuthorized
		s.Account = account
		s.CodeHint = ""
		s.Err = ""
	})
	m.log.Info("telegram authorized", "account", account)
}

func (m *SessionManager) setFailed(msg string) {
	m.update(func(s *domain.AuthStatus) {
		s.State = domain.AuthFailed
		s.Err = msg
		s.Account = ""
		s.CodeHint = ""
	})
}

func (m *SessionManager) update(fn func(*domain.AuthStatus)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(&m.status)
}
