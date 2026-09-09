package app

import (
	"context"
	"time"

	"tg-harvester/internal/domain"
)

// Status is the aggregated view shown on the dashboard.
type Status struct {
	Now      time.Time
	Auth     domain.AuthStatus
	Poll     domain.PollStatus
	Settings domain.Settings
	Channels []domain.Channel
}

// AuthStatusSource exposes the login state.
type AuthStatusSource interface {
	Status() domain.AuthStatus
}

// PollStatusSource exposes the scheduler state.
type PollStatusSource interface {
	Status() domain.PollStatus
}

// ChannelLister lists channels for display.
type ChannelLister interface {
	List(ctx context.Context) ([]domain.Channel, error)
}

// StatusService aggregates the dashboard status.
type StatusService struct {
	auth     AuthStatusSource
	poll     PollStatusSource
	settings SettingsStore
	channels ChannelLister
	now      func() time.Time
}

// NewStatusService creates a StatusService.
func NewStatusService(auth AuthStatusSource, poll PollStatusSource, settings SettingsStore, channels ChannelLister) *StatusService {
	return &StatusService{auth: auth, poll: poll, settings: settings, channels: channels, now: time.Now}
}

// Status collects the current state of every component.
func (s *StatusService) Status(ctx context.Context) (Status, error) {
	settings, err := s.settings.Settings(ctx)
	if err != nil {
		return Status{}, err
	}
	channels, err := s.channels.List(ctx)
	if err != nil {
		return Status{}, err
	}
	return Status{
		Now:      s.now(),
		Auth:     s.auth.Status(),
		Poll:     s.poll.Status(),
		Settings: settings,
		Channels: channels,
	}, nil
}
