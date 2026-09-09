package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"tg-harvester/internal/domain"
)

var fixedNow = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeSource struct {
	msgs        map[domain.ChannelID][]domain.Message
	fetchErr    map[domain.ChannelID]error
	block       map[domain.ChannelID]bool // Fetch waits for ctx
	fetches     int
	lastMax     int
	downloads   int
	downloadErr error
}

func (f *fakeSource) Resolve(context.Context, string) (domain.Channel, error) {
	return domain.Channel{}, errors.New("not implemented")
}

// Fetch mirrors the real contract: everything newer than afterID, oldest
// first, or a BacklogError when there is more than max of it.
func (f *fakeSource) Fetch(ctx context.Context, ch domain.Channel, afterID int64, max int) ([]domain.Message, error) {
	f.fetches++
	f.lastMax = max
	if f.block[ch.ID] {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err := f.fetchErr[ch.ID]; err != nil {
		return nil, err
	}
	var out []domain.Message
	for _, m := range f.msgs[ch.ID] {
		if m.ID > afterID {
			out = append(out, m)
		}
	}
	if len(out) > max {
		return nil, &domain.BacklogError{Limit: max}
	}
	return out, nil
}

func (f *fakeSource) Download(_ context.Context, ref domain.PhotoRef) ([]byte, error) {
	f.downloads++
	if f.downloadErr != nil {
		return nil, f.downloadErr
	}
	return []byte(fmt.Sprintf("photo-%d", ref.ID)), nil
}

type fakeForwarder struct {
	posts    []domain.Post
	fail     func(post domain.Post, attempt int) error
	attempts map[int64]int
}

func (f *fakeForwarder) Forward(_ context.Context, _ domain.Endpoint, post domain.Post) error {
	if f.attempts == nil {
		f.attempts = map[int64]int{}
	}
	f.attempts[post.FirstID()]++
	if f.fail != nil {
		if err := f.fail(post, f.attempts[post.FirstID()]); err != nil {
			return err
		}
	}
	f.posts = append(f.posts, post)
	return nil
}

type fakeCursorStore struct {
	channels []domain.Channel
	cursors  map[domain.ChannelID]int64
	polls    map[domain.ChannelID]string
	polled   []domain.ChannelID
}

func newFakeCursorStore(channels ...domain.Channel) *fakeCursorStore {
	return &fakeCursorStore{channels: channels, cursors: map[domain.ChannelID]int64{}, polls: map[domain.ChannelID]string{}}
}

func (f *fakeCursorStore) ListEnabled(context.Context) ([]domain.Channel, error) {
	var out []domain.Channel
	for _, ch := range f.channels {
		if ch.Enabled {
			out = append(out, ch)
		}
	}
	return out, nil
}

func (f *fakeCursorStore) AdvanceCursor(_ context.Context, id domain.ChannelID, cursor int64, _ time.Time) error {
	f.cursors[id] = cursor
	return nil
}

func (f *fakeCursorStore) RecordPoll(_ context.Context, id domain.ChannelID, _ time.Time, errMsg string) error {
	f.polls[id] = errMsg
	f.polled = append(f.polled, id)
	return nil
}

type fakeSettingsStore struct {
	mu sync.Mutex
	s  domain.Settings
}

func (f *fakeSettingsStore) Settings(context.Context) (domain.Settings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.s, nil
}

func (f *fakeSettingsStore) SaveSettings(_ context.Context, s domain.Settings) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.s = s
	return nil
}

func testSettings() *fakeSettingsStore {
	s := domain.DefaultSettings()
	s.Endpoint.URL = "https://example.com/hook"
	return &fakeSettingsStore{s: s}
}

func msg(id, grouped int64, text string, photo bool) domain.Message {
	m := domain.Message{ID: id, GroupedID: grouped, Date: fixedNow.Add(-time.Minute), Text: text, Author: domain.Author{Name: "Chan"}}
	if photo {
		m.Photo = &domain.PhotoRef{MessageID: id, ID: id * 10}
	}
	return m
}

func newTestHarvester(src *fakeSource, fwd *fakeForwarder, store *fakeCursorStore, settings SettingsStore) *Harvester {
	h := NewHarvester(src, fwd, store, settings, discardLogger())
	h.RetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	h.now = func() time.Time { return fixedNow }
	return h
}

func TestPollAllForwardsPostsAndAdvancesCursor(t *testing.T) {
	ch := domain.Channel{ID: 1, Username: "chan", Enabled: true, Cursor: 0}
	src := &fakeSource{msgs: map[domain.ChannelID][]domain.Message{
		1: {msg(1, 0, "hello", false), msg(2, 7, "album", true), msg(3, 7, "", true)},
	}}
	fwd := &fakeForwarder{}
	store := newFakeCursorStore(ch)
	h := newTestHarvester(src, fwd, store, testSettings())

	if err := h.PollAll(context.Background()); err != nil {
		t.Fatalf("PollAll: %v", err)
	}
	if len(fwd.posts) != 2 {
		t.Fatalf("forwarded %d posts, want 2", len(fwd.posts))
	}
	album := fwd.posts[1]
	if len(album.Photos) != 2 || string(album.Photos[0].Data) != "photo-20" || album.Photos[0].FileName != "chan_2_1.jpg" {
		t.Errorf("album photos = %+v", album.Photos)
	}
	if album.Photos[1].FileName != "chan_3_2.jpg" {
		t.Errorf("second photo name = %q", album.Photos[1].FileName)
	}
	if store.cursors[1] != 3 {
		t.Errorf("cursor = %d, want 3", store.cursors[1])
	}
	if store.polls[1] != "" {
		t.Errorf("poll error = %q, want empty", store.polls[1])
	}
	if src.lastMax != h.MaxBacklog {
		t.Errorf("fetch limit = %d, want MaxBacklog %d", src.lastMax, h.MaxBacklog)
	}
}

func TestPollAllResumesFromCursor(t *testing.T) {
	ch := domain.Channel{ID: 1, Username: "chan", Enabled: true, Cursor: 2}
	src := &fakeSource{msgs: map[domain.ChannelID][]domain.Message{
		1: {msg(1, 0, "old", false), msg(2, 0, "old", false), msg(3, 0, "new", false)},
	}}
	fwd := &fakeForwarder{}
	store := newFakeCursorStore(ch)
	h := newTestHarvester(src, fwd, store, testSettings())

	if err := h.PollAll(context.Background()); err != nil {
		t.Fatalf("PollAll: %v", err)
	}
	if len(fwd.posts) != 1 || fwd.posts[0].FirstID() != 3 || store.cursors[1] != 3 {
		t.Fatalf("posts = %+v cursor = %d", fwd.posts, store.cursors[1])
	}
}

func TestPollAllFailureStopsChannelButNotOthers(t *testing.T) {
	a := domain.Channel{ID: 1, Username: "a", Enabled: true}
	b := domain.Channel{ID: 2, Username: "b", Enabled: true}
	src := &fakeSource{msgs: map[domain.ChannelID][]domain.Message{
		1: {msg(1, 0, "ok", false), msg(2, 0, "rejected", false), msg(3, 0, "never", false)},
		2: {msg(1, 0, "b1", false)},
	}}
	fwd := &fakeForwarder{fail: func(post domain.Post, _ int) error {
		if post.Channel.ID == 1 && post.FirstID() == 2 {
			return &domain.PermanentError{Err: errors.New("400 Bad Request")}
		}
		return nil
	}}
	store := newFakeCursorStore(a, b)
	h := newTestHarvester(src, fwd, store, testSettings())

	if err := h.PollAll(context.Background()); err != nil {
		t.Fatalf("PollAll: %v", err)
	}
	if store.cursors[1] != 1 {
		t.Errorf("channel a cursor = %d, want 1", store.cursors[1])
	}
	if store.polls[1] == "" {
		t.Error("channel a should have a recorded error")
	}
	if fwd.attempts[2] != 1 {
		t.Errorf("permanent error retried %d times", fwd.attempts[2])
	}
	if store.cursors[2] != 1 || store.polls[2] != "" {
		t.Errorf("channel b cursor = %d err = %q", store.cursors[2], store.polls[2])
	}
	if len(fwd.posts) != 2 {
		t.Errorf("forwarded %d posts, want 2", len(fwd.posts))
	}
}

func TestPollAllBacklogTooLargeIsPerChannel(t *testing.T) {
	a := domain.Channel{ID: 1, Username: "a", Enabled: true}
	b := domain.Channel{ID: 2, Username: "b", Enabled: true}
	var many []domain.Message
	for i := int64(1); i <= 4; i++ {
		many = append(many, msg(i, 0, "m", false))
	}
	src := &fakeSource{msgs: map[domain.ChannelID][]domain.Message{1: many, 2: {msg(1, 0, "b", false)}}}
	fwd := &fakeForwarder{}
	store := newFakeCursorStore(a, b)
	h := newTestHarvester(src, fwd, store, testSettings())
	h.MaxBacklog = 3

	if err := h.PollAll(context.Background()); err != nil {
		t.Fatalf("PollAll: %v", err)
	}
	if _, ok := store.cursors[1]; ok {
		t.Error("nothing may be delivered from a channel whose backlog is too large")
	}
	if !strings.Contains(store.polls[1], "Skip to latest") {
		t.Errorf("channel a error = %q", store.polls[1])
	}
	if store.cursors[2] != 1 {
		t.Errorf("channel b cursor = %d, want 1", store.cursors[2])
	}
}

func TestPollAllChannelTimeoutDoesNotAbortCycle(t *testing.T) {
	a := domain.Channel{ID: 1, Username: "a", Enabled: true}
	b := domain.Channel{ID: 2, Username: "b", Enabled: true}
	src := &fakeSource{
		msgs:  map[domain.ChannelID][]domain.Message{2: {msg(1, 0, "b", false)}},
		block: map[domain.ChannelID]bool{1: true},
	}
	fwd := &fakeForwarder{}
	store := newFakeCursorStore(a, b)
	h := newTestHarvester(src, fwd, store, testSettings())
	h.ChannelTimeout = 20 * time.Millisecond

	if err := h.PollAll(context.Background()); err != nil {
		t.Fatalf("PollAll: %v", err)
	}
	if !strings.Contains(store.polls[1], "stopped after") {
		t.Errorf("channel a error = %q", store.polls[1])
	}
	if store.cursors[2] != 1 {
		t.Errorf("channel b cursor = %d; a slow channel must not starve the others", store.cursors[2])
	}
}

func TestPollAllParentCancellationPropagates(t *testing.T) {
	a := domain.Channel{ID: 1, Username: "a", Enabled: true}
	b := domain.Channel{ID: 2, Username: "b", Enabled: true}
	src := &fakeSource{block: map[domain.ChannelID]bool{1: true}}
	store := newFakeCursorStore(a, b)
	h := newTestHarvester(src, &fakeForwarder{}, store, testSettings())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if err := h.PollAll(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if len(store.polled) != 1 {
		t.Errorf("polled = %v; channel b must not run after cancellation", store.polled)
	}
}

func TestPollChannelRespectsSettleDelay(t *testing.T) {
	ch := domain.Channel{ID: 1, Username: "a", Enabled: true}
	young := msg(2, 0, "fresh", false)
	young.Date = fixedNow.Add(-2 * time.Second)
	src := &fakeSource{msgs: map[domain.ChannelID][]domain.Message{1: {msg(1, 0, "old", false), young}}}
	fwd := &fakeForwarder{}
	store := newFakeCursorStore(ch)
	h := newTestHarvester(src, fwd, store, testSettings())

	if err := h.PollAll(context.Background()); err != nil {
		t.Fatalf("PollAll: %v", err)
	}
	if len(fwd.posts) != 1 || store.cursors[1] != 1 {
		t.Errorf("posts = %d cursor = %d; young message must wait", len(fwd.posts), store.cursors[1])
	}
}

func TestPollAllFloodWaitAbortsCycle(t *testing.T) {
	a := domain.Channel{ID: 1, Username: "a", Enabled: true}
	b := domain.Channel{ID: 2, Username: "b", Enabled: true}
	src := &fakeSource{
		msgs:     map[domain.ChannelID][]domain.Message{2: {msg(1, 0, "b", false)}},
		fetchErr: map[domain.ChannelID]error{1: &domain.FloodWaitError{Wait: time.Minute}},
	}
	fwd := &fakeForwarder{}
	store := newFakeCursorStore(a, b)
	h := newTestHarvester(src, fwd, store, testSettings())

	err := h.PollAll(context.Background())
	var fw *domain.FloodWaitError
	if !errors.As(err, &fw) {
		t.Fatalf("want FloodWaitError, got %v", err)
	}
	if len(store.polled) != 1 || store.polled[0] != 1 {
		t.Errorf("polled = %v, want only channel 1", store.polled)
	}
	if len(fwd.posts) != 0 {
		t.Error("channel b must not be polled after a flood wait")
	}
}

func TestDeliverRetriesTransientErrors(t *testing.T) {
	ch := domain.Channel{ID: 1, Username: "a", Enabled: true}
	src := &fakeSource{msgs: map[domain.ChannelID][]domain.Message{1: {msg(1, 0, "m", false)}}}
	fwd := &fakeForwarder{fail: func(_ domain.Post, attempt int) error {
		if attempt < 3 {
			return errors.New("503 Service Unavailable")
		}
		return nil
	}}
	store := newFakeCursorStore(ch)
	h := newTestHarvester(src, fwd, store, testSettings())

	if err := h.PollAll(context.Background()); err != nil {
		t.Fatalf("PollAll: %v", err)
	}
	if fwd.attempts[1] != 3 || store.cursors[1] != 1 {
		t.Errorf("attempts = %d cursor = %d", fwd.attempts[1], store.cursors[1])
	}
}

func TestDeliverGivesUpAfterRetries(t *testing.T) {
	ch := domain.Channel{ID: 1, Username: "a", Enabled: true}
	src := &fakeSource{msgs: map[domain.ChannelID][]domain.Message{1: {msg(1, 0, "m", false)}}}
	fwd := &fakeForwarder{fail: func(domain.Post, int) error { return errors.New("down") }}
	store := newFakeCursorStore(ch)
	h := newTestHarvester(src, fwd, store, testSettings())

	if err := h.PollAll(context.Background()); err != nil {
		t.Fatalf("PollAll: %v", err)
	}
	if fwd.attempts[1] != 3 {
		t.Errorf("attempts = %d, want 3", fwd.attempts[1])
	}
	if _, ok := store.cursors[1]; ok {
		t.Error("cursor must not advance on failure")
	}
	if store.polls[1] == "" {
		t.Error("error must be recorded on the channel")
	}
}

func TestPollAllRequiresEndpoint(t *testing.T) {
	store := newFakeCursorStore()
	h := newTestHarvester(&fakeSource{}, &fakeForwarder{}, store, &fakeSettingsStore{s: domain.DefaultSettings()})
	if err := h.PollAll(context.Background()); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("want ErrNotConfigured, got %v", err)
	}
}
