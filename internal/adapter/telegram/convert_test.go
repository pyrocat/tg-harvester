package telegram

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"tg-harvester/internal/domain"
)

var testChannel = domain.Channel{ID: 77, AccessHash: 5, Username: "chan", Title: "The Channel"}

func TestLargestSize(t *testing.T) {
	sizes := []tg.PhotoSizeClass{
		&tg.PhotoStrippedSize{Type: "i"},
		&tg.PhotoSize{Type: "m", W: 320, H: 240},
		&tg.PhotoSizeProgressive{Type: "y", W: 1280, H: 720},
		&tg.PhotoSize{Type: "x", W: 800, H: 600},
		&tg.PhotoCachedSize{Type: "s"},
	}
	got, w, h, ok := largestSize(sizes)
	if !ok || got != "y" || w != 1280 || h != 720 {
		t.Fatalf("largestSize = %q %d %d %v", got, w, h, ok)
	}
	if _, _, _, ok := largestSize([]tg.PhotoSizeClass{&tg.PhotoStrippedSize{Type: "i"}}); ok {
		t.Error("stripped-only sizes are not downloadable")
	}
}

func TestConvertMessages(t *testing.T) {
	photo := &tg.Photo{ID: 900, AccessHash: 901, FileReference: []byte("ref")}
	photo.Sizes = []tg.PhotoSizeClass{&tg.PhotoSize{Type: "x", W: 100, H: 50}}
	withPhoto := &tg.Message{ID: 3, Date: 1757340000, Message: "caption", Media: &tg.MessageMediaPhoto{Photo: photo}}
	withPhoto.SetGroupedID(42)

	signed := &tg.Message{ID: 4, Date: 1757340001, Message: "signed"}
	signed.SetPostAuthor("Editor")

	fromUser := &tg.Message{ID: 5, Date: 1757340002, Message: "from user", FromID: &tg.PeerUser{UserID: 11}}

	raw := []tg.MessageClass{
		&tg.MessageEmpty{ID: 1},
		&tg.MessageService{ID: 2},
		withPhoto,
		signed,
		fromUser,
		&tg.Message{ID: 6, Date: 1757340003}, // no text, no photo
	}
	users := map[int64]*tg.User{11: {ID: 11, FirstName: "Jane", LastName: "Doe", Username: "janedoe"}}

	got := convertMessages(testChannel, raw, users)
	if len(got) != 3 {
		t.Fatalf("converted %d messages, want 3: %+v", len(got), got)
	}

	first := got[0]
	if first.ID != 3 || first.GroupedID != 42 || first.Text != "caption" {
		t.Errorf("first = %+v", first)
	}
	if !first.Date.Equal(time.Unix(1757340000, 0).UTC()) {
		t.Errorf("date = %s", first.Date)
	}
	if first.Photo == nil {
		t.Fatal("photo reference missing")
	}
	if first.Photo.ID != 900 || first.Photo.AccessHash != 901 || string(first.Photo.FileReference) != "ref" {
		t.Errorf("photo ref = %+v", first.Photo)
	}
	if first.Photo.SizeType != "x" || first.Photo.Width != 100 || first.Photo.MessageID != 3 {
		t.Errorf("photo size = %+v", first.Photo)
	}
	if first.Photo.ChannelID != 77 || first.Photo.ChannelAccessHash != 5 {
		t.Errorf("photo channel = %+v", first.Photo)
	}
	// A channel post without a signature is attributed to the channel.
	if first.Author.Name != "The Channel" || first.Author.Username != "chan" {
		t.Errorf("author = %+v", first.Author)
	}
	if got[1].Author.Name != "Editor" || got[1].Author.Username != "chan" {
		t.Errorf("signed author = %+v", got[1].Author)
	}
	if got[2].Author.Name != "Jane Doe" || got[2].Author.Username != "janedoe" {
		t.Errorf("user author = %+v", got[2].Author)
	}
}

func TestUserName(t *testing.T) {
	tests := []struct {
		user *tg.User
		want string
	}{
		{&tg.User{FirstName: "Jane", LastName: "Doe"}, "Jane Doe"},
		{&tg.User{FirstName: "Jane"}, "Jane"},
		{&tg.User{Username: "janedoe"}, "@janedoe"},
		{&tg.User{}, "unknown"},
	}
	for _, tc := range tests {
		if got := userName(tc.user); got != tc.want {
			t.Errorf("userName(%+v) = %q, want %q", tc.user, got, tc.want)
		}
	}
}

func TestCodeHint(t *testing.T) {
	tests := []struct {
		typ  tg.AuthSentCodeTypeClass
		want string
	}{
		{&tg.AuthSentCodeTypeApp{}, "Telegram app"},
		{&tg.AuthSentCodeTypeSMS{}, "SMS"},
		{&tg.AuthSentCodeTypeCall{}, "phone call"},
		{&tg.AuthSentCodeTypeFirebaseSMS{}, ""},
	}
	for _, tc := range tests {
		if got := codeHint(&tg.AuthSentCode{Type: tc.typ}); got != tc.want {
			t.Errorf("codeHint(%T) = %q, want %q", tc.typ, got, tc.want)
		}
	}
	if got := codeHint(nil); got != "" {
		t.Errorf("codeHint(nil) = %q", got)
	}
}

func TestMapError(t *testing.T) {
	if err := mapError(nil); err != nil {
		t.Fatalf("mapError(nil) = %v", err)
	}

	flood := tgerr.New(420, "FLOOD_WAIT_30")
	var fw *domain.FloodWaitError
	if err := mapError(flood); !errors.As(err, &fw) || fw.Wait != 30*time.Second {
		t.Fatalf("flood wait: got %v (%T)", err, err)
	}

	unauthorized := tgerr.New(401, "AUTH_KEY_UNREGISTERED")
	if err := mapError(unauthorized); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("401: got %v", err)
	}

	private := tgerr.New(400, "CHANNEL_PRIVATE")
	err := mapError(private)
	if err == nil || errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("CHANNEL_PRIVATE: got %v", err)
	}

	other := errors.New("boom")
	if got := mapError(other); !errors.Is(got, other) {
		t.Fatalf("passthrough: got %v", got)
	}
}

func TestLoginError(t *testing.T) {
	// gotd wraps RPC errors several layers deep and appends a stack trace;
	// the UI must get the Telegram error, on one line.
	wrapped := fmt.Errorf("callback:\n\tframe\n  - login: send code: %w", tgerr.New(400, "API_ID_INVALID"))
	err := loginError(wrapped)
	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("a login failure must unwrap to ErrUnauthorized")
	}
	if got := err.Error(); got != "Telegram rejected the login: API_ID_INVALID (code 400)" {
		t.Fatalf("message = %q", got)
	}
	if !errors.Is(err, wrapped) {
		t.Error("the original error must stay reachable for logs")
	}

	plain := loginError(errors.New("dial tcp: refused\nstack frame"))
	if got := plain.Error(); got != "login failed: dial tcp: refused" {
		t.Fatalf("plain message = %q", got)
	}
}

func TestChannelFromChats(t *testing.T) {
	chats := []tg.ChatClass{&tg.Chat{ID: 1}, &tg.Channel{ID: 2, Title: "T"}}
	got, ok := channelFromChats(chats)
	if !ok || got.ID != 2 {
		t.Fatalf("got %+v %v", got, ok)
	}
	if _, ok := channelFromChats([]tg.ChatClass{&tg.Chat{ID: 1}}); ok {
		t.Error("a basic group is not a channel")
	}
}
