package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"tg-harvester/internal/domain"
)

// ChannelResolver looks up channels on Telegram.
type ChannelResolver interface {
	Resolve(ctx context.Context, username string) (domain.Channel, error)
}

// ChannelStore persists the harvested channels.
type ChannelStore interface {
	List(ctx context.Context) ([]domain.Channel, error)
	Get(ctx context.Context, id domain.ChannelID) (domain.Channel, error)
	Add(ctx context.Context, ch domain.Channel) error
	Remove(ctx context.Context, id domain.ChannelID) error
	SetEnabled(ctx context.Context, id domain.ChannelID, enabled bool) error
	ResetCursor(ctx context.Context, id domain.ChannelID, cursor int64) error
}

// ChannelService manages the list of harvested channels.
type ChannelService struct {
	store    ChannelStore
	resolver ChannelResolver
	now      func() time.Time
}

// NewChannelService creates a ChannelService.
func NewChannelService(store ChannelStore, resolver ChannelResolver) *ChannelService {
	return &ChannelService{store: store, resolver: resolver, now: time.Now}
}

// List returns all channels.
func (s *ChannelService) List(ctx context.Context) ([]domain.Channel, error) {
	return s.store.List(ctx)
}

// Add resolves a channel reference on Telegram and stores it. Harvesting
// starts from the channel's newest message; history is not backfilled.
func (s *ChannelService) Add(ctx context.Context, ref string) (domain.Channel, error) {
	username, err := domain.ParseChannelRef(ref)
	if err != nil {
		return domain.Channel{}, err
	}
	ch, err := s.resolver.Resolve(ctx, username)
	if err != nil {
		if errors.Is(err, domain.ErrNotConnected) || errors.Is(err, domain.ErrUnauthorized) {
			return domain.Channel{}, &domain.ValidationError{Field: "channel", Msg: "log in to Telegram before adding channels"}
		}
		return domain.Channel{}, fmt.Errorf("resolve @%s: %w", username, err)
	}
	ch.Enabled = true
	ch.AddedAt = s.now()
	if err := s.store.Add(ctx, ch); err != nil {
		if errors.Is(err, domain.ErrAlreadyExists) {
			return domain.Channel{}, &domain.ValidationError{Field: "channel", Msg: "@" + username + " is already in the list"}
		}
		return domain.Channel{}, err
	}
	return ch, nil
}

// Remove deletes a channel.
func (s *ChannelService) Remove(ctx context.Context, id domain.ChannelID) error {
	return s.store.Remove(ctx, id)
}

// Toggle flips the enabled flag of a channel.
func (s *ChannelService) Toggle(ctx context.Context, id domain.ChannelID) error {
	ch, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	return s.store.SetEnabled(ctx, id, !ch.Enabled)
}

// Reset moves the cursor to the channel's newest message, skipping anything
// pending. It is the escape hatch for a post the endpoint keeps rejecting.
func (s *ChannelService) Reset(ctx context.Context, id domain.ChannelID) error {
	ch, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if ch.Username == "" {
		return &domain.ValidationError{Field: "channel", Msg: "cannot reset a channel without a username"}
	}
	fresh, err := s.resolver.Resolve(ctx, ch.Username)
	if err != nil {
		return fmt.Errorf("resolve @%s: %w", ch.Username, err)
	}
	return s.store.ResetCursor(ctx, id, fresh.Cursor)
}
