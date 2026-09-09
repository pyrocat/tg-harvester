package forward

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"tg-harvester/internal/domain"
)

type captured struct {
	method string
	header http.Header
	body   []byte
}

func newServer(t *testing.T, status int) (*httptest.Server, *captured) {
	t.Helper()
	var mu sync.Mutex
	c := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		c.method, c.header, c.body = r.Method, r.Header.Clone(), body
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

func samplePost() domain.Post {
	ch := domain.Channel{ID: 123, Username: "example", Title: "Example"}
	return domain.Post{
		Channel:    ch,
		MessageIDs: []int64{45, 46},
		GroupedID:  999,
		Date:       time.Date(2026, 9, 8, 19, 0, 0, 0, time.UTC),
		Text:       "hello",
		Author:     domain.Author{Name: "Jane Doe", Username: "janedoe"},
		Photos:     []domain.Photo{{FileName: "example_45_1.jpg", MIME: "image/jpeg", Width: 2, Height: 3, Data: []byte{0xff, 0xd8, 0x00}}},
	}
}

func TestForwardSendsExpectedJSON(t *testing.T) {
	srv, c := newServer(t, http.StatusOK)
	f := New(srv.Client())
	target := domain.Endpoint{URL: srv.URL + "/hook", HeaderName: "Authorization", HeaderValue: "Bearer secret"}

	if err := f.Forward(context.Background(), target, samplePost()); err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if c.method != http.MethodPost {
		t.Errorf("method = %s", c.method)
	}
	if got := c.header.Get("Content-Type"); got != "application/json" {
		t.Errorf("content-type = %q", got)
	}
	if got := c.header.Get("Authorization"); got != "Bearer secret" {
		t.Errorf("authorization = %q", got)
	}
	if got := c.header.Get("X-Idempotency-Key"); got != "123:46" {
		t.Errorf("idempotency key = %q", got)
	}

	var p Payload
	if err := json.Unmarshal(c.body, &p); err != nil {
		t.Fatalf("decode body: %v\n%s", err, c.body)
	}
	if p.Channel.ID != 123 || p.Channel.Username != "example" || p.Channel.Title != "Example" {
		t.Errorf("channel = %+v", p.Channel)
	}
	if p.MessageID != 45 || len(p.MessageIDs) != 2 || p.GroupedID != "999" {
		t.Errorf("ids = %d %v grouped %q", p.MessageID, p.MessageIDs, p.GroupedID)
	}
	if p.Link != "https://t.me/example/45" || p.Text != "hello" {
		t.Errorf("link/text = %q %q", p.Link, p.Text)
	}
	if p.Author.Name != "Jane Doe" || p.Author.Username != "janedoe" {
		t.Errorf("author = %+v", p.Author)
	}
	if len(p.Photos) != 1 || p.Photos[0].FileName != "example_45_1.jpg" || string(p.Photos[0].Data) != "\xff\xd8\x00" || p.Photos[0].Width != 2 {
		t.Errorf("photos = %+v", p.Photos)
	}

	// Photos must be base64 on the wire and never null.
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(c.body, &raw)
	if string(raw["photos"]) == "null" {
		t.Error("photos must be an array")
	}
	var photos []map[string]any
	_ = json.Unmarshal(raw["photos"], &photos)
	if photos[0]["data"] != "/9gA" {
		t.Errorf("photo data = %v, want base64 /9gA", photos[0]["data"])
	}
}

func TestForwardEmptyPhotosIsArray(t *testing.T) {
	srv, c := newServer(t, http.StatusAccepted)
	f := New(srv.Client())
	post := samplePost()
	post.Photos = nil
	post.GroupedID = 0
	if err := f.Forward(context.Background(), domain.Endpoint{URL: srv.URL}, post); err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(c.body, &raw)
	if string(raw["photos"]) != "[]" {
		t.Errorf("photos = %s, want []", raw["photos"])
	}
	if _, ok := raw["grouped_id"]; ok {
		t.Error("grouped_id must be omitted for single messages")
	}
}

func TestForwardConfiguredHeaderCannotReplaceProtocolHeaders(t *testing.T) {
	srv, c := newServer(t, http.StatusOK)
	f := New(srv.Client())
	// Validation rejects these names, but the forwarder must be safe even
	// if a value reaches it another way.
	target := domain.Endpoint{URL: srv.URL, HeaderName: "content-type", HeaderValue: "text/plain"}
	if err := f.Forward(context.Background(), target, samplePost()); err != nil {
		t.Fatal(err)
	}
	if got := c.header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q, want application/json", got)
	}
	target.HeaderName, target.HeaderValue = "X-Idempotency-Key", "spoofed"
	if err := f.Forward(context.Background(), target, samplePost()); err != nil {
		t.Fatal(err)
	}
	if got := c.header.Get("X-Idempotency-Key"); got != "123:46" {
		t.Fatalf("idempotency key = %q", got)
	}
}

func TestForwardStatusHandling(t *testing.T) {
	cases := []struct {
		status    int
		wantErr   bool
		permanent bool
	}{
		{http.StatusOK, false, false},
		{http.StatusNoContent, false, false},
		{http.StatusBadRequest, true, true},
		{http.StatusNotFound, true, true},
		{http.StatusUnauthorized, true, true},
		{http.StatusRequestTimeout, true, false},
		{http.StatusTooManyRequests, true, false},
		{http.StatusInternalServerError, true, false},
		{http.StatusBadGateway, true, false},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv, _ := newServer(t, tc.status)
			f := New(srv.Client())
			err := f.Forward(context.Background(), domain.Endpoint{URL: srv.URL}, samplePost())
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && domain.IsPermanent(err) != tc.permanent {
				t.Fatalf("permanent = %v, want %v (%v)", domain.IsPermanent(err), tc.permanent, err)
			}
		})
	}
}

func TestForwardNetworkErrorIsTransient(t *testing.T) {
	srv, _ := newServer(t, http.StatusOK)
	url := srv.URL
	srv.Close()
	f := New(&http.Client{Timeout: time.Second})
	err := f.Forward(context.Background(), domain.Endpoint{URL: url}, samplePost())
	if err == nil || domain.IsPermanent(err) {
		t.Fatalf("want transient error, got %v", err)
	}
}
