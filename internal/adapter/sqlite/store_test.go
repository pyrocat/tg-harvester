package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"tg-harvester/internal/domain"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Migrations must be idempotent.
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	return New(db)
}

func TestSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	got, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != domain.DefaultSettings() {
		t.Fatalf("defaults = %+v, want %+v", got, domain.DefaultSettings())
	}

	want := domain.Settings{
		Endpoint:     domain.Endpoint{URL: "https://example.com/hook", HeaderName: "Authorization", HeaderValue: "Bearer x"},
		PollInterval: 90 * time.Second,
		SettleDelay:  5 * time.Second,
	}
	if err := s.SaveSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err = s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("settings = %+v, want %+v", got, want)
	}
}

func TestCredentialsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	c, err := s.Credentials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.IsSet() {
		t.Fatalf("fresh database must have no credentials, got %+v", c)
	}
	want := domain.Credentials{APIID: 42, APIHash: "0123456789abcdef0123456789abcdef", Phone: "+12025550123"}
	if err := s.SaveCredentials(ctx, want); err != nil {
		t.Fatal(err)
	}
	c, err = s.Credentials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c != want {
		t.Fatalf("credentials = %+v, want %+v", c, want)
	}
}

func TestSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.LoadSession(ctx); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("empty session: got %v, want ErrNotFound", err)
	}
	if err := s.StoreSession(ctx, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreSession(ctx, []byte("two")); err != nil {
		t.Fatal(err)
	}
	data, err := s.LoadSession(ctx)
	if err != nil || string(data) != "two" {
		t.Fatalf("LoadSession = %q, %v", data, err)
	}
	if err := s.DeleteSession(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadSession(ctx); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("after delete: got %v, want ErrNotFound", err)
	}
}

func TestChannelsCRUD(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	at := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)

	ch := domain.Channel{ID: 100, AccessHash: -5, Username: "chan", Title: "Chan", Cursor: 7, Enabled: true, AddedAt: at}
	if err := s.Add(ctx, ch); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(ctx, ch); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate add: got %v, want ErrAlreadyExists", err)
	}
	other := domain.Channel{ID: 200, AccessHash: 1, Username: "other", Title: "Other", Enabled: false, AddedAt: at.Add(time.Second)}
	if err := s.Add(ctx, other); err != nil {
		t.Fatal(err)
	}

	all, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != 100 || all[1].ID != 200 {
		t.Fatalf("List = %+v", all)
	}
	if all[0] != ch {
		t.Fatalf("stored channel = %+v, want %+v", all[0], ch)
	}
	enabled, err := s.ListEnabled(ctx)
	if err != nil || len(enabled) != 1 || enabled[0].ID != 100 {
		t.Fatalf("ListEnabled = %+v, %v", enabled, err)
	}

	if err := s.AdvanceCursor(ctx, 100, 9, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPoll(ctx, 100, at.Add(2*time.Minute), "boom"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cursor != 9 || got.LastError != "boom" || !got.LastPolledAt.Equal(at.Add(2*time.Minute)) {
		t.Fatalf("after updates = %+v", got)
	}

	if err := s.ResetCursor(ctx, 100, 50); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, 100)
	if got.Cursor != 50 || got.LastError != "" {
		t.Fatalf("after reset = %+v", got)
	}

	if err := s.SetEnabled(ctx, 200, true); err != nil {
		t.Fatal(err)
	}
	enabled, _ = s.ListEnabled(ctx)
	if len(enabled) != 2 {
		t.Fatalf("ListEnabled after enable = %d", len(enabled))
	}

	if err := s.Remove(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(ctx, 100); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second remove: got %v, want ErrNotFound", err)
	}
	if _, err := s.Get(ctx, 100); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get removed: got %v, want ErrNotFound", err)
	}
	if err := s.SetEnabled(ctx, 999, true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetEnabled unknown: got %v", err)
	}
}
