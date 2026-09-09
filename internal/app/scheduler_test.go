package app

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"tg-harvester/internal/domain"
)

type fakePoller struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (p *fakePoller) PollAll(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.err
}

func (p *fakePoller) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *fakePoller) setErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

type fakeAuth struct{ ok bool }

func (a fakeAuth) Authorized() bool { return a.ok }

func TestSchedulerPollsOnIntervalAndTrigger(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		poller := &fakePoller{}
		settings := &fakeSettingsStore{s: domain.Settings{PollInterval: 30 * time.Second}}
		s := NewScheduler(poller, fakeAuth{ok: true}, settings, discardLogger())
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() { _ = s.Run(ctx) }()
		synctest.Wait()
		if poller.count() != 0 {
			t.Fatalf("polled before the interval elapsed")
		}
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if poller.count() != 1 {
			t.Fatalf("calls after 30s = %d, want 1", poller.count())
		}
		s.Trigger()
		synctest.Wait()
		if poller.count() != 2 {
			t.Fatalf("calls after trigger = %d, want 2", poller.count())
		}
		if st := s.Status(); st.LastPollAt.IsZero() || st.LastError != "" || st.Polling {
			t.Errorf("status = %+v", st)
		}

		// Shorten the interval; Refresh must apply it without a restart.
		_ = settings.SaveSettings(ctx, domain.Settings{PollInterval: 10 * time.Second})
		s.Refresh()
		synctest.Wait()
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if poller.count() != 3 {
			t.Fatalf("calls after shortened interval = %d, want 3", poller.count())
		}
	})
}

func TestSchedulerSkipsWhenNotAuthorized(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		poller := &fakePoller{}
		settings := &fakeSettingsStore{s: domain.Settings{PollInterval: 30 * time.Second}}
		s := NewScheduler(poller, fakeAuth{ok: false}, settings, discardLogger())
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() { _ = s.Run(ctx) }()
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if poller.count() != 0 {
			t.Fatalf("polled while not authorized")
		}
		if s.Status().LastError == "" {
			t.Error("status should explain why polling is skipped")
		}
	})
}

func TestSchedulerPausesOnFloodWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		poller := &fakePoller{err: &domain.FloodWaitError{Wait: 5 * time.Minute}}
		settings := &fakeSettingsStore{s: domain.Settings{PollInterval: 30 * time.Second}}
		s := NewScheduler(poller, fakeAuth{ok: true}, settings, discardLogger())
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() { _ = s.Run(ctx) }()
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if poller.count() != 1 {
			t.Fatalf("calls = %d, want 1", poller.count())
		}
		if s.Status().PausedUntil.IsZero() {
			t.Fatal("flood wait must pause the scheduler")
		}
		poller.setErr(nil)
		time.Sleep(4 * time.Minute)
		synctest.Wait()
		if poller.count() != 1 {
			t.Fatalf("polled during the pause: calls = %d", poller.count())
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if poller.count() != 2 {
			t.Fatalf("calls after the pause = %d, want 2", poller.count())
		}
		if !s.Status().PausedUntil.IsZero() {
			t.Error("pause must be cleared after a successful poll")
		}
	})
}
