package telegram

import (
	"context"
	"sort"

	"github.com/gotd/td/tg"

	"tg-harvester/internal/domain"
)

// historyPageSize is Telegram's maximum for messages.getHistory.
const historyPageSize = 100

// historyPage fetches one page of channel history: up to limit messages with
// ID < offsetID (or the newest ones when offsetID is 0) and ID > minID,
// newest first. This is messages.getHistory's primary behaviour, the one
// every client relies on, which is why the harvester uses nothing else.
type historyPage func(ctx context.Context, offsetID, minID, limit int) (tg.ModifiedMessagesMessages, error)

// collectNew walks the history backwards from the newest message until it
// reaches afterID and returns the messages oldest first. Walking the whole
// unseen range, rather than a window at the cursor, means the caller can be
// sure nothing older than what it received is still pending. More than max
// messages is reported as a *domain.BacklogError.
func collectNew(ctx context.Context, ch domain.Channel, fetch historyPage, afterID int64, max int) ([]domain.Message, error) {
	var out []domain.Message
	seen := 0
	offsetID := 0
	for {
		page, err := fetch(ctx, offsetID, int(afterID), historyPageSize)
		if err != nil {
			return nil, err
		}
		raw := page.GetMessages()
		if len(raw) == 0 {
			break
		}
		seen += len(raw)
		if seen > max {
			return nil, &domain.BacklogError{Limit: max}
		}
		// Paging must follow the raw IDs: service and empty messages are
		// dropped from the output but still occupy positions in the history.
		minID := raw[0].GetID()
		for _, item := range raw[1:] {
			if id := item.GetID(); id < minID {
				minID = id
			}
		}
		for _, m := range convertMessages(ch, raw, usersByID(page.GetUsers())) {
			if m.ID > afterID {
				out = append(out, m)
			}
		}
		// A short page is the last one, and once a page reaches the
		// message right after the cursor nothing older can be pending.
		if len(raw) < historyPageSize || int64(minID) <= afterID+1 {
			break
		}
		offsetID = minID
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
