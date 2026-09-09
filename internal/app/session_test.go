package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"tg-harvester/internal/domain"
)

type fakeRunner struct {
	mu           sync.Mutex
	needCode     bool
	needPassword bool
	runErr       error
	runs         int
	codes        []string
	passwords    []string
	logouts      int
}

func (r *fakeRunner) Run(ctx context.Context, _ domain.Credentials, p AuthPrompter, ready func(string)) error {
	r.mu.Lock()
	r.runs++
	err, needCode, needPassword := r.runErr, r.needCode, r.needPassword
	r.mu.Unlock()
	if err != nil {
		return err
	}
	if needCode {
		code, err := p.AskCode(ctx, "app")
		if err != nil {
			return err
		}
		r.mu.Lock()
		r.codes = append(r.codes, code)
		r.mu.Unlock()
	}
	if needPassword {
		pw, err := p.AskPassword(ctx)
		if err != nil {
			return err
		}
		r.mu.Lock()
		r.passwords = append(r.passwords, pw)
		r.mu.Unlock()
	}
	ready("Test User")
	<-ctx.Done()
	return ctx.Err()
}

func (r *fakeRunner) Logout(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logouts++
	return nil
}

func (r *fakeRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs
}

type fakeCredStore struct {
	mu             sync.Mutex
	creds          domain.Credentials
	sessionDeleted int
}

func (s *fakeCredStore) Credentials(context.Context) (domain.Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.creds, nil
}

func (s *fakeCredStore) SaveCredentials(_ context.Context, c domain.Credentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creds = c
	return nil
}

func (s *fakeCredStore) DeleteSession(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionDeleted++
	return nil
}

func (s *fakeCredStore) deleted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionDeleted
}

var testCreds = domain.Credentials{APIID: 1, APIHash: strings.Repeat("a", 32), Phone: "+12025550123"}

func expectState(t *testing.T, m *SessionManager, want domain.AuthState) {
	t.Helper()
	if got := m.Status().State; got != want {
		t.Fatalf("state = %s, want %s (status %+v)", got, want, m.Status())
	}
}

func TestSessionManagerInteractiveLogin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runner := &fakeRunner{needCode: true, needPassword: true}
		store := &fakeCredStore{}
		m := NewSessionManager(runner, store, discardLogger())
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() { _ = m.Run(ctx) }()
		synctest.Wait()
		expectState(t, m, domain.AuthNotConfigured)
		if err := m.SubmitCode("1"); err == nil {
			t.Fatal("SubmitCode must fail when no login is in progress")
		}

		if err := m.Configure(ctx, testCreds); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		expectState(t, m, domain.AuthAwaitingCode)
		if m.Status().CodeHint != "app" {
			t.Errorf("code hint = %q", m.Status().CodeHint)
		}
		if err := m.SubmitPassword("x"); err == nil {
			t.Fatal("SubmitPassword must fail while awaiting a code")
		}
		if err := m.SubmitCode("12345"); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		expectState(t, m, domain.AuthAwaitingPassword)
		if err := m.SubmitPassword("secret"); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		expectState(t, m, domain.AuthAuthorized)
		if st := m.Status(); st.Account != "Test User" || st.Phone != testCreds.Phone {
			t.Errorf("status = %+v", st)
		}
		if !m.Authorized() {
			t.Error("Authorized() must be true")
		}
		runner.mu.Lock()
		if len(runner.codes) != 1 || runner.codes[0] != "12345" || len(runner.passwords) != 1 || runner.passwords[0] != "secret" {
			t.Errorf("runner got codes %v passwords %v", runner.codes, runner.passwords)
		}
		runner.mu.Unlock()

		if err := m.Logout(ctx); err != nil {
			t.Fatal(err)
		}
		expectState(t, m, domain.AuthNotConfigured)
		if runner.logouts != 1 {
			t.Errorf("logouts = %d, want 1", runner.logouts)
		}
		// Once because the phone changed on Configure, once on Logout.
		if store.deleted() != 2 {
			t.Errorf("session deleted %d times, want 2", store.deleted())
		}

		cancel()
		synctest.Wait()
		expectState(t, m, domain.AuthStopped)
	})
}

func TestSessionManagerAutoStartsWithStoredSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runner := &fakeRunner{}
		store := &fakeCredStore{creds: testCreds}
		m := NewSessionManager(runner, store, discardLogger())
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() { _ = m.Run(ctx) }()
		synctest.Wait()
		expectState(t, m, domain.AuthAuthorized)
		if store.deleted() != 0 {
			t.Error("a stored session must not be deleted on start")
		}
	})
}

func TestSessionManagerAuthErrorWaitsForUser(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runner := &fakeRunner{runErr: fmt.Errorf("PHONE_CODE_INVALID: %w", domain.ErrUnauthorized)}
		store := &fakeCredStore{creds: testCreds}
		m := NewSessionManager(runner, store, discardLogger())
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() { _ = m.Run(ctx) }()
		synctest.Wait()
		expectState(t, m, domain.AuthFailed)
		if !strings.Contains(m.Status().Err, "PHONE_CODE_INVALID") {
			t.Errorf("err = %q", m.Status().Err)
		}
		if store.deleted() != 1 {
			t.Errorf("session deleted %d times, want 1", store.deleted())
		}

		time.Sleep(time.Hour)
		synctest.Wait()
		if runner.count() != 1 {
			t.Fatalf("runs = %d; auth errors must not auto-restart", runner.count())
		}

		runner.mu.Lock()
		runner.runErr = nil
		runner.mu.Unlock()
		if err := m.Retry(ctx); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		expectState(t, m, domain.AuthAuthorized)
		if runner.count() != 2 {
			t.Errorf("runs = %d, want 2", runner.count())
		}
	})
}

func TestSessionManagerReconnectsWithBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runner := &fakeRunner{runErr: errors.New("network down")}
		store := &fakeCredStore{creds: testCreds}
		m := NewSessionManager(runner, store, discardLogger())
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() { _ = m.Run(ctx) }()
		synctest.Wait()
		expectState(t, m, domain.AuthFailed)
		if runner.count() != 1 {
			t.Fatalf("runs = %d", runner.count())
		}

		time.Sleep(time.Second)
		synctest.Wait()
		if runner.count() != 2 {
			t.Fatalf("runs after 1s = %d, want 2", runner.count())
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if runner.count() != 2 {
			t.Fatalf("runs after 2s = %d, want still 2 (backoff doubled)", runner.count())
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if runner.count() != 3 {
			t.Fatalf("runs after 3s = %d, want 3", runner.count())
		}
		if store.deleted() != 0 {
			t.Error("transient errors must keep the session")
		}
	})
}

func TestSessionManagerConfigureBeforeRunFails(t *testing.T) {
	m := NewSessionManager(&fakeRunner{}, &fakeCredStore{}, discardLogger())
	if err := m.Configure(context.Background(), testCreds); err == nil {
		t.Fatal("expected error when the manager is not running")
	}
	var ve *domain.ValidationError
	if err := m.Configure(context.Background(), domain.Credentials{}); !errors.As(err, &ve) {
		t.Fatalf("expected validation error, got %v", err)
	}
}
