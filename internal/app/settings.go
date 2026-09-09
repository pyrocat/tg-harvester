package app

import (
	"context"
	"time"

	"tg-harvester/internal/domain"
)

// SettingsService reads and updates the runtime settings.
type SettingsService struct {
	store    SettingsStore
	fwd      Forwarder
	onChange func()
	now      func() time.Time
}

// NewSettingsService creates a SettingsService. onChange is invoked after a
// successful update (used to wake the scheduler); it may be nil.
func NewSettingsService(store SettingsStore, fwd Forwarder, onChange func()) *SettingsService {
	return &SettingsService{store: store, fwd: fwd, onChange: onChange, now: time.Now}
}

// Get returns the current settings.
func (s *SettingsService) Get(ctx context.Context) (domain.Settings, error) {
	return s.store.Settings(ctx)
}

// Update validates and stores new settings.
func (s *SettingsService) Update(ctx context.Context, settings domain.Settings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	if err := s.store.SaveSettings(ctx, settings); err != nil {
		return err
	}
	if s.onChange != nil {
		s.onChange()
	}
	return nil
}

// SendTest delivers a sample payload to the configured endpoint.
func (s *SettingsService) SendTest(ctx context.Context) error {
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return err
	}
	if !settings.Endpoint.IsSet() {
		return &domain.ValidationError{Field: "endpoint_url", Msg: "configure the endpoint first"}
	}
	if err := settings.Endpoint.Validate(); err != nil {
		return err
	}
	return s.fwd.Forward(ctx, settings.Endpoint, SamplePost(s.now()))
}

// SamplePost builds the payload used by "Test endpoint".
func SamplePost(at time.Time) domain.Post {
	// A 1x1 white JPEG so receivers can exercise their image handling.
	pixel := []byte{
		0xff, 0xd8, 0xff, 0xdb, 0x00, 0x43, 0x00, 0x03, 0x02, 0x02, 0x02, 0x02, 0x02, 0x03, 0x02, 0x02,
		0x02, 0x03, 0x03, 0x03, 0x03, 0x04, 0x06, 0x04, 0x04, 0x04, 0x04, 0x04, 0x08, 0x06, 0x06, 0x05,
		0x06, 0x09, 0x08, 0x0a, 0x0a, 0x09, 0x08, 0x09, 0x09, 0x0a, 0x0c, 0x0f, 0x0c, 0x0a, 0x0b, 0x0e,
		0x0b, 0x09, 0x09, 0x0d, 0x11, 0x0d, 0x0e, 0x0f, 0x10, 0x10, 0x11, 0x10, 0x0a, 0x0c, 0x12, 0x13,
		0x12, 0x10, 0x13, 0x0f, 0x10, 0x10, 0x10, 0xff, 0xc9, 0x00, 0x0b, 0x08, 0x00, 0x01, 0x00, 0x01,
		0x01, 0x01, 0x11, 0x00, 0xff, 0xcc, 0x00, 0x06, 0x00, 0x10, 0x10, 0x05, 0xff, 0xda, 0x00, 0x08,
		0x01, 0x01, 0x00, 0x00, 0x3f, 0x00, 0xd2, 0xcf, 0x20, 0xff, 0xd9,
	}
	ch := domain.Channel{ID: 1, Username: "tg_harvester_test", Title: "tg-harvester test"}
	return domain.Post{
		Channel:    ch,
		MessageIDs: []int64{1},
		Date:       at,
		Text:       "This is a test payload from tg-harvester.",
		Author:     domain.Author{Name: ch.Title, Username: ch.Username},
		Photos:     []domain.Photo{{FileName: "tg_harvester_test_1_1.jpg", MIME: "image/jpeg", Width: 1, Height: 1, Data: pixel}},
	}
}
