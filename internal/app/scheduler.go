package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"tg-harvester/internal/domain"
)

// Poller runs one harvesting cycle.
type Poller interface {
	PollAll(ctx context.Context) error
}

// AuthChecker reports whether harvesting is possible.
type AuthChecker interface {
	Authorized() bool
}

// Scheduler runs the Poller periodically and on demand.
type Scheduler struct {
	poller   Poller
	auth     AuthChecker
	settings SettingsStore
	log      *slog.Logger
	now      func() time.Time

	trigger chan struct{}
	wake    chan struct{}

	mu     sync.Mutex
	status domain.PollStatus
}

// NewScheduler creates a Scheduler; call Run to start it.
func NewScheduler(poller Poller, auth AuthChecker, settings SettingsStore, log *slog.Logger) *Scheduler {
	return &Scheduler{
		poller:   poller,
		auth:     auth,
		settings: settings,
		log:      log,
		now:      time.Now,
		trigger:  make(chan struct{}, 1),
		wake:     make(chan struct{}, 1),
	}
}

// Trigger requests an immediate poll ("Poll now"). It never blocks.
func (s *Scheduler) Trigger() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

// Refresh asks the scheduler to re-read the poll interval. It never blocks.
func (s *Scheduler) Refresh() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Status returns a snapshot of the scheduler state.
func (s *Scheduler) Status() domain.PollStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Run polls until ctx is cancelled. The interval is re-read from settings
// before every wait so that changes made in the UI apply immediately.
func (s *Scheduler) Run(ctx context.Context) error {
	for {
		wait := s.nextWait(ctx)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
			s.poll(ctx)
		case <-s.trigger:
			timer.Stop()
			s.poll(ctx)
		case <-s.wake:
			timer.Stop()
		}
	}
}

func (s *Scheduler) nextWait(ctx context.Context) time.Duration {
	interval := domain.DefaultPollInterval
	if st, err := s.settings.Settings(ctx); err == nil {
		interval = st.PollInterval
	} else {
		s.log.Error("load settings", "err", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.now().Add(interval)
	if !s.status.LastPollAt.IsZero() {
		if candidate := s.status.LastPollAt.Add(interval); candidate.Before(next) {
			next = candidate
		}
	}
	if s.status.PausedUntil.After(next) {
		next = s.status.PausedUntil
	}
	s.status.NextPollAt = next
	return max(time.Until(next), 0)
}

func (s *Scheduler) poll(ctx context.Context) {
	if !s.auth.Authorized() {
		s.setResult("waiting for Telegram login")
		return
	}
	s.mu.Lock()
	if s.status.PausedUntil.After(s.now()) {
		s.mu.Unlock()
		return
	}
	s.status.Polling = true
	s.mu.Unlock()

	err := s.poller.PollAll(ctx)

	s.mu.Lock()
	s.status.Polling = false
	s.status.LastPollAt = s.now()
	s.status.LastError = ""
	s.status.PausedUntil = time.Time{}
	var fw *domain.FloodWaitError
	switch {
	case err == nil:
	case errors.As(err, &fw):
		s.status.PausedUntil = s.now().Add(fw.Wait)
		s.status.LastError = err.Error()
	case errors.Is(err, context.Canceled):
	default:
		s.status.LastError = err.Error()
	}
	s.mu.Unlock()
	if err != nil && !errors.Is(err, context.Canceled) {
		s.log.Warn("poll cycle failed", "err", err)
	}
}

func (s *Scheduler) setResult(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.LastError = msg
}
