package telegram

import (
	"strings"
	"time"

	"github.com/gotd/td/tg"

	"tg-harvester/internal/domain"
)

// convertMessages turns the messages of a getHistory response into domain
// messages, skipping service and empty messages. Messages without text and
// without a photo are skipped as well: there is nothing to forward.
func convertMessages(ch domain.Channel, raw []tg.MessageClass, users map[int64]*tg.User) []domain.Message {
	out := make([]domain.Message, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(*tg.Message)
		if !ok {
			continue
		}
		msg := domain.Message{
			ID:        int64(m.ID),
			GroupedID: m.GroupedID,
			Date:      time.Unix(int64(m.Date), 0).UTC(),
			Text:      m.Message,
			Author:    messageAuthor(ch, m, users),
			Photo:     photoRef(ch, m),
		}
		if msg.Text == "" && msg.Photo == nil {
			continue
		}
		out = append(out, msg)
	}
	return out
}

// messageAuthor resolves who published a message: the posting user when the
// channel exposes one, the signature of a signed post, or the channel itself.
func messageAuthor(ch domain.Channel, m *tg.Message, users map[int64]*tg.User) domain.Author {
	if peer, ok := m.FromID.(*tg.PeerUser); ok {
		if u, ok := users[peer.UserID]; ok {
			return domain.Author{Name: userName(u), Username: u.Username}
		}
	}
	if author, ok := m.GetPostAuthor(); ok && author != "" {
		return domain.Author{Name: author, Username: ch.Username}
	}
	return domain.Author{Name: ch.Title, Username: ch.Username}
}

func userName(u *tg.User) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name != "" {
		return name
	}
	if u.Username != "" {
		return "@" + u.Username
	}
	return "unknown"
}

// photoRef extracts a downloadable reference to the message's photo, if any.
func photoRef(ch domain.Channel, m *tg.Message) *domain.PhotoRef {
	media, ok := m.Media.(*tg.MessageMediaPhoto)
	if !ok {
		return nil
	}
	p, ok := media.Photo.(*tg.Photo)
	if !ok {
		return nil
	}
	size, w, h, ok := largestSize(p.Sizes)
	if !ok {
		return nil
	}
	return &domain.PhotoRef{
		ChannelID:         int64(ch.ID),
		ChannelAccessHash: ch.AccessHash,
		MessageID:         int64(m.ID),
		ID:                p.ID,
		AccessHash:        p.AccessHash,
		FileReference:     p.FileReference,
		SizeType:          size,
		Width:             w,
		Height:            h,
	}
}

// largestSize picks the highest-resolution downloadable size, ignoring
// stripped and cached thumbnails which carry no separate file.
func largestSize(sizes []tg.PhotoSizeClass) (sizeType string, width, height int, ok bool) {
	best := -1
	for _, s := range sizes {
		var t string
		var w, h int
		switch v := s.(type) {
		case *tg.PhotoSize:
			t, w, h = v.Type, v.W, v.H
		case *tg.PhotoSizeProgressive:
			t, w, h = v.Type, v.W, v.H
		default:
			continue
		}
		if area := w * h; area > best {
			best, sizeType, width, height, ok = area, t, w, h, true
		}
	}
	return sizeType, width, height, ok
}

// usersByID indexes the users returned alongside a getHistory response.
func usersByID(raw []tg.UserClass) map[int64]*tg.User {
	out := make(map[int64]*tg.User, len(raw))
	for _, item := range raw {
		if u, ok := item.(*tg.User); ok {
			out[u.ID] = u
		}
	}
	return out
}

// channelFromChats finds the channel a resolved username points at.
func channelFromChats(chats []tg.ChatClass) (*tg.Channel, bool) {
	for _, item := range chats {
		if c, ok := item.(*tg.Channel); ok {
			return c, true
		}
	}
	return nil, false
}
