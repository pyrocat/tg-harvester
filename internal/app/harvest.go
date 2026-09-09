package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"tg-harvester/internal/domain"
)

// MessageSource reads messages and media from Telegram.
type MessageSource interface {
	// Resolve looks up a public channel by username and returns it with
	// Cursor set to the ID of its newest message.
	Resolve(ctx context.Context, username string) (domain.Channel, error)
	// Fetch returns up to limit messages with ID > afterID, ascending by ID.
	Fetch(ctx context.Context, ch domain.Channel, afterID int64, limit int) ([]domain.Message, error)
	// Download returns the bytes of a photo.
	Download(ctx context.Context, ref domain.PhotoRef) ([]byte, error)
}

// Forwarder delivers a post to the configured HTTP endpoint. Failures that
// must not be retried are wrapped in domain.PermanentError.
type Forwarder interface {
	Forward(ctx context.Context, target domain.Endpoint, post domain.Post) error
}

// ChannelCursorStore is the harvester's view of channel persistence.
type ChannelCursorStore interface {
	ListEnabled(ctx context.Context) ([]domain.Channel, error)
	AdvanceCursor(ctx context.Context, id domain.ChannelID, cursor int64, at time.Time) error
	RecordPoll(ctx context.Context, id domain.ChannelID, at time.Time, errMsg string) error
}

// SettingsStore persists the runtime settings.
type SettingsStore interface {
	Settings(ctx context.Context) (domain.Settings, error)
	SaveSettings(ctx context.Context, s domain.Settings) error
}

// Harvester fetches new posts from every enabled channel and forwards them.
type Harvester struct {
	src      MessageSource
	fwd      Forwarder
	channels ChannelCursorStore
	settings SettingsStore
	log      *slog.Logger

	PageSize    int
	MaxPages    int
	RetryDelays []time.Duration
	now         func() time.Time
}

// NewHarvester wires a Harvester with production defaults.
func NewHarvester(src MessageSource, fwd Forwarder, channels ChannelCursorStore, settings SettingsStore, log *slog.Logger) *Harvester {
	return &Harvester{
		src:         src,
		fwd:         fwd,
		channels:    channels,
		settings:    settings,
		log:         log,
		PageSize:    100,
		MaxPages:    5,
		RetryDelays: []time.Duration{time.Second, 4 * time.Second, 16 * time.Second},
		now:         time.Now,
	}
}

// PollAll harvests every enabled channel once. Per-channel failures are
// recorded on the channel and do not stop the other channels; errors that
// affect the whole session (flood wait, lost connection, revoked auth,
// missing endpoint, cancellation) are returned.
func (h *Harvester) PollAll(ctx context.Context) error {
	settings, err := h.settings.Settings(ctx)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	if !settings.Endpoint.IsSet() {
		return fmt.Errorf("endpoint URL: %w", domain.ErrNotConfigured)
	}
	channels, err := h.channels.ListEnabled(ctx)
	if err != nil {
		return fmt.Errorf("list channels: %w", err)
	}
	for _, ch := range channels {
		err := h.PollChannel(ctx, ch, settings)
		msg := ""
		if err != nil {
			msg = err.Error()
			h.log.Warn("poll channel failed", "channel", ch.DisplayName(), "err", err)
		}
		if rerr := h.channels.RecordPoll(ctx, ch.ID, h.now(), msg); rerr != nil && err == nil {
			err = fmt.Errorf("record poll: %w", rerr)
		}
		if err != nil && isGlobal(err) {
			return err
		}
	}
	return nil
}

// maxAlbumSize is Telegram's limit on the number of messages in one media group.
const maxAlbumSize = 10

// PollChannel harvests one channel: fetch pages of new messages, group
// albums, download photos, forward, and advance the cursor after each
// successful delivery.
func (h *Harvester) PollChannel(ctx context.Context, ch domain.Channel, settings domain.Settings) error {
	cursor := ch.Cursor
	for page := 0; page < h.MaxPages; page++ {
		posts, pageFull, err := h.fetchPosts(ctx, ch, cursor, h.now().Add(-settings.SettleDelay))
		if err != nil {
			return err
		}
		if len(posts) == 0 {
			return nil
		}
		for _, post := range posts {
			if err := h.deliver(ctx, settings.Endpoint, post); err != nil {
				return fmt.Errorf("message %d: %w", post.FirstID(), err)
			}
			cursor = post.MaxID()
			if err := h.channels.AdvanceCursor(ctx, ch.ID, cursor, h.now()); err != nil {
				return fmt.Errorf("advance cursor to %d: %w", cursor, err)
			}
			h.log.Info("post forwarded", "channel", ch.DisplayName(), "message_id", post.FirstID(), "photos", len(post.Photos))
		}
		if !pageFull {
			return nil
		}
	}
	return nil
}

// fetchPosts reads the next page of messages after cursor and groups them
// into posts. A trailing album is held back because the rest of it may be on
// the next page; when the held-back album fills the entire page the window is
// widened so that the album can still be delivered whole instead of stalling
// the channel.
func (h *Harvester) fetchPosts(ctx context.Context, ch domain.Channel, cursor int64, cutoff time.Time) ([]domain.Post, bool, error) {
	for limit := h.PageSize; ; limit += maxAlbumSize {
		msgs, err := h.src.Fetch(ctx, ch, cursor, limit)
		if err != nil {
			return nil, false, fmt.Errorf("fetch after %d: %w", cursor, err)
		}
		pageFull := len(msgs) >= limit
		msgs = settled(msgs, cutoff)
		if len(msgs) == 0 {
			return nil, false, nil
		}
		// The page is only "full" for grouping purposes if nothing was held
		// back by the settle delay; a trimmed page has a known end.
		posts, _ := domain.GroupPosts(ch, msgs, pageFull)
		if len(posts) > 0 || !pageFull {
			return posts, pageFull, nil
		}
		if limit > h.PageSize+maxAlbumSize {
			// Larger than any real album: deliver what we have rather than
			// retrying forever.
			posts, _ = domain.GroupPosts(ch, msgs, false)
			return posts, pageFull, nil
		}
	}
}

// settled keeps the leading run of messages older than cutoff, so that an
// album still being uploaded is never split.
func settled(msgs []domain.Message, cutoff time.Time) []domain.Message {
	for i, m := range msgs {
		if m.Date.After(cutoff) {
			return msgs[:i]
		}
	}
	return msgs
}

func (h *Harvester) deliver(ctx context.Context, target domain.Endpoint, post domain.Post) error {
	base := post.Channel.Username
	if base == "" {
		base = strconv.FormatInt(int64(post.Channel.ID), 10)
	}
	for i, ref := range post.PhotoRefs {
		data, err := h.src.Download(ctx, ref)
		if err != nil {
			return fmt.Errorf("download photo %d: %w", ref.ID, err)
		}
		post.Photos = append(post.Photos, domain.Photo{
			FileName: fmt.Sprintf("%s_%d_%d.jpg", base, ref.MessageID, i+1),
			MIME:     "image/jpeg",
			Width:    ref.Width,
			Height:   ref.Height,
			Data:     data,
		})
	}

	var err error
	for attempt := 0; ; attempt++ {
		err = h.fwd.Forward(ctx, target, post)
		if err == nil {
			return nil
		}
		if attempt >= len(h.RetryDelays) || domain.IsPermanent(err) || ctx.Err() != nil {
			return fmt.Errorf("forward: %w", err)
		}
		delay := h.RetryDelays[attempt]
		h.log.Warn("forward failed; retrying", "message_id", post.FirstID(), "attempt", attempt+1, "delay", delay, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

// isGlobal reports whether an error should abort the whole poll cycle.
func isGlobal(err error) bool {
	var fw *domain.FloodWaitError
	return errors.As(err, &fw) ||
		errors.Is(err, domain.ErrNotConnected) ||
		errors.Is(err, domain.ErrUnauthorized) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}
