package domain

import (
	"errors"
	"testing"
	"time"
)

func TestParseChannelRef(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "@telegram", want: "telegram"},
		{in: "Telegram", want: "telegram"},
		{in: "  @Some_Channel_1 ", want: "some_channel_1"},
		{in: "t.me/telegram", want: "telegram"},
		{in: "https://t.me/telegram", want: "telegram"},
		{in: "https://t.me/s/telegram", want: "telegram"},
		{in: "https://t.me/telegram/123", want: "telegram"},
		{in: "http://www.telegram.me/telegram?x=1", want: "telegram"},
		{in: "", wantErr: true},
		{in: "https://t.me/+AbCdEf", wantErr: true},
		{in: "https://t.me/joinchat/AbCdEf", wantErr: true},
		{in: "ab", wantErr: true},
		{in: "1abc", wantErr: true},
		{in: "has space", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseChannelRef(tc.in)
			if tc.wantErr {
				var ve *ValidationError
				if !errors.As(err, &ve) {
					t.Fatalf("want ValidationError, got %v (value %q)", err, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCredentialsValidate(t *testing.T) {
	valid := Credentials{APIID: 12345, APIHash: "0123456789abcdef0123456789abcdef", Phone: "+12025550123"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid credentials rejected: %v", err)
	}
	bad := []Credentials{
		{APIID: 0, APIHash: valid.APIHash, Phone: valid.Phone},
		{APIID: 1, APIHash: "short", Phone: valid.Phone},
		{APIID: 1, APIHash: valid.APIHash, Phone: "12025550123"},
		{APIID: 1, APIHash: valid.APIHash, Phone: "+0123"},
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("case %d: expected error for %+v", i, c)
		}
	}
}

func TestNormalizePhone(t *testing.T) {
	tests := map[string]string{
		"+1 (202) 555-0123":  "+12025550123",
		"12025550123":        "+12025550123",
		"  +7 999 123 45 67": "+79991234567",
		"":                   "",
		"abc":                "",
	}
	for in, want := range tests {
		if got := NormalizePhone(in); got != want {
			t.Errorf("NormalizePhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSettingsValidate(t *testing.T) {
	s := DefaultSettings()
	if err := s.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	s.Endpoint = Endpoint{URL: "https://example.com/hook", HeaderName: "Authorization", HeaderValue: "Bearer x"}
	if err := s.Validate(); err != nil {
		t.Fatalf("valid endpoint rejected: %v", err)
	}

	cases := []struct {
		name string
		mut  func(*Settings)
	}{
		{"interval too small", func(s *Settings) { s.PollInterval = time.Second }},
		{"interval too large", func(s *Settings) { s.PollInterval = 48 * time.Hour }},
		{"negative settle", func(s *Settings) { s.SettleDelay = -time.Second }},
		{"relative url", func(s *Settings) { s.Endpoint.URL = "/hook" }},
		{"ftp url", func(s *Settings) { s.Endpoint.URL = "ftp://example.com" }},
		{"header name only", func(s *Settings) { s.Endpoint.HeaderValue = "" }},
		{"header newline", func(s *Settings) { s.Endpoint.HeaderValue = "a\r\nb" }},
		{"header bad name", func(s *Settings) { s.Endpoint.HeaderName = "X Y" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := s
			tc.mut(&c)
			var ve *ValidationError
			if err := c.Validate(); !errors.As(err, &ve) {
				t.Fatalf("expected ValidationError, got %v", err)
			}
		})
	}
}

func TestGroupPosts(t *testing.T) {
	ch := Channel{ID: 1, Username: "chan", Title: "Chan"}
	at := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	ref := func(id int64) *PhotoRef { return &PhotoRef{MessageID: id, ID: id * 10} }
	author := Author{Name: "Chan", Username: "chan"}
	msgs := []Message{
		{ID: 1, Date: at, Text: "hello", Author: author},
		{ID: 2, GroupedID: 77, Date: at, Text: "album caption", Author: author, Photo: ref(2)},
		{ID: 3, GroupedID: 77, Date: at, Author: author, Photo: ref(3)},
		{ID: 4, GroupedID: 77, Date: at, Text: "second caption", Author: author, Photo: ref(4)},
		{ID: 5, Date: at, Text: "single photo", Author: author, Photo: ref(5)},
		{ID: 6, GroupedID: 99, Date: at, Author: author, Photo: ref(6)},
		{ID: 7, GroupedID: 99, Date: at, Author: author, Photo: ref(7)},
	}

	t.Run("last page groups everything", func(t *testing.T) {
		posts, held := GroupPosts(ch, msgs, false)
		if held != 0 {
			t.Fatalf("held = %d, want 0", held)
		}
		if len(posts) != 4 {
			t.Fatalf("len(posts) = %d, want 4", len(posts))
		}
		album := posts[1]
		if got := album.MessageIDs; len(got) != 3 || got[0] != 2 || got[2] != 4 {
			t.Errorf("album ids = %v", got)
		}
		if album.Text != "album caption\n\nsecond caption" {
			t.Errorf("album text = %q", album.Text)
		}
		if len(album.PhotoRefs) != 3 || album.MaxID() != 4 || album.FirstID() != 2 {
			t.Errorf("album = %+v", album)
		}
		if album.Link() != "https://t.me/chan/2" {
			t.Errorf("link = %q", album.Link())
		}
		if posts[0].Text != "hello" || len(posts[0].PhotoRefs) != 0 {
			t.Errorf("first post = %+v", posts[0])
		}
		if len(posts[2].PhotoRefs) != 1 || posts[2].Text != "single photo" {
			t.Errorf("third post = %+v", posts[2])
		}
		if posts[3].GroupedID != 99 || len(posts[3].MessageIDs) != 2 {
			t.Errorf("fourth post = %+v", posts[3])
		}
	})

	t.Run("full page holds back trailing album", func(t *testing.T) {
		posts, held := GroupPosts(ch, msgs, true)
		if held != 2 {
			t.Fatalf("held = %d, want 2", held)
		}
		if len(posts) != 3 || posts[2].MaxID() != 5 {
			t.Fatalf("posts = %+v", posts)
		}
	})

	t.Run("full page does not hold back trailing single", func(t *testing.T) {
		posts, held := GroupPosts(ch, msgs[:5], true)
		if held != 0 || len(posts) != 3 {
			t.Fatalf("held = %d, posts = %d", held, len(posts))
		}
	})

	t.Run("empty input", func(t *testing.T) {
		posts, held := GroupPosts(ch, nil, true)
		if held != 0 || len(posts) != 0 {
			t.Fatalf("held = %d, posts = %d", held, len(posts))
		}
	})
}
