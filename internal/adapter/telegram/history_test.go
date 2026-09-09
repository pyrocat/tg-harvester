package telegram

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"

	"tg-harvester/internal/domain"
)

// fakeHistory simulates messages.getHistory over a channel whose existing
// message IDs are given: it answers with up to limit messages that have
// ID < offsetID (all when offsetID is 0) and ID > minID, newest first.
// Every 50th ID is a service message so that paging is exercised over
// messages that do not make it into the output.
type fakeHistory struct {
	ids      []int // ascending
	requests int
}

func (f *fakeHistory) page(_ context.Context, offsetID, minID, limit int) (tg.ModifiedMessagesMessages, error) {
	f.requests++
	res := &tg.MessagesChannelMessages{}
	for i := len(f.ids) - 1; i >= 0 && len(res.Messages) < limit; i-- {
		id := f.ids[i]
		if offsetID != 0 && id >= offsetID {
			continue
		}
		if id <= minID {
			break
		}
		if id%50 == 0 {
			res.Messages = append(res.Messages, &tg.MessageService{ID: id})
			continue
		}
		res.Messages = append(res.Messages, &tg.Message{ID: id, Date: 1_757_340_000 + id, Message: "m"})
	}
	return res, nil
}

func idsBetween(from, to int, skip map[int]bool) []int {
	var ids []int
	for id := from; id <= to; id++ {
		if !skip[id] {
			ids = append(ids, id)
		}
	}
	return ids
}

func TestCollectNewWalksBackToCursor(t *testing.T) {
	// 1..350 with a few deleted IDs; cursor at 120.
	deleted := map[int]bool{130: true, 131: true, 200: true, 349: true}
	hist := &fakeHistory{ids: idsBetween(1, 350, deleted)}

	got, err := collectNew(context.Background(), testChannel, hist.page, 120, 10_000)
	if err != nil {
		t.Fatalf("collectNew: %v", err)
	}
	var want []int64
	for _, id := range idsBetween(121, 350, deleted) {
		if id%50 != 0 { // service messages are not forwarded
			want = append(want, int64(id))
		}
	}
	if len(got) != len(want) {
		t.Fatalf("got %d messages, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("message %d: id = %d, want %d (order or gap handling broken)", i, got[i].ID, want[i])
		}
	}
	// 350..251, 250..151, 150..121 (short) → three requests.
	if hist.requests != 3 {
		t.Errorf("requests = %d, want 3", hist.requests)
	}
}

func TestCollectNewNothingNew(t *testing.T) {
	hist := &fakeHistory{ids: idsBetween(1, 40, nil)}
	got, err := collectNew(context.Background(), testChannel, hist.page, 40, 100)
	if err != nil || len(got) != 0 || hist.requests != 1 {
		t.Fatalf("got %v, err %v, requests %d", got, err, hist.requests)
	}
}

func TestCollectNewFullPageEndingAtCursorNeedsNoExtraRequest(t *testing.T) {
	hist := &fakeHistory{ids: idsBetween(1, 240, nil)}
	got, err := collectNew(context.Background(), testChannel, hist.page, 140, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].ID != 141 || got[len(got)-1].ID != 240 {
		t.Fatalf("range = %d..%d", got[0].ID, got[len(got)-1].ID)
	}
	if hist.requests != 1 {
		t.Errorf("requests = %d, want 1: the page already reached the cursor", hist.requests)
	}
}

func TestCollectNewBacklogTooLarge(t *testing.T) {
	hist := &fakeHistory{ids: idsBetween(1, 1000, nil)}
	_, err := collectNew(context.Background(), testChannel, hist.page, 0, 250)
	var be *domain.BacklogError
	if !errors.As(err, &be) || be.Limit != 250 {
		t.Fatalf("want BacklogError{250}, got %v", err)
	}
	if hist.requests > 3 {
		t.Errorf("requests = %d; must give up as soon as the limit is exceeded", hist.requests)
	}
}

func TestCollectNewPropagatesErrors(t *testing.T) {
	boom := errors.New("boom")
	fail := func(context.Context, int, int, int) (tg.ModifiedMessagesMessages, error) { return nil, boom }
	if _, err := collectNew(context.Background(), testChannel, fail, 0, 100); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}
