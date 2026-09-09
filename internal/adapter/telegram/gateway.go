package telegram

import (
	"bytes"
	"context"
	"fmt"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"tg-harvester/internal/app"
	"tg-harvester/internal/domain"
)

var _ app.MessageSource = (*Client)(nil)

// Resolve looks up a public channel by username. The returned channel's
// cursor is the ID of its newest message, so harvesting starts from now.
func (c *Client) Resolve(ctx context.Context, username string) (domain.Channel, error) {
	api, err := c.apiClient()
	if err != nil {
		return domain.Channel{}, err
	}
	resolved, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: username})
	if err != nil {
		return domain.Channel{}, mapError(err)
	}
	raw, ok := channelFromChats(resolved.Chats)
	if !ok {
		return domain.Channel{}, &domain.ValidationError{Field: "channel", Msg: "@" + username + " is not a channel"}
	}
	ch := domain.Channel{
		ID:         domain.ChannelID(raw.ID),
		AccessHash: raw.AccessHash,
		Username:   raw.Username,
		Title:      raw.Title,
	}
	if ch.Username == "" {
		ch.Username = username
	}
	top, err := c.topMessageID(ctx, api, ch)
	if err != nil {
		return domain.Channel{}, err
	}
	ch.Cursor = top
	return ch, nil
}

// topMessageID returns the ID of the newest message in the channel.
func (c *Client) topMessageID(ctx context.Context, api *tg.Client, ch domain.Channel) (int64, error) {
	res, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:  inputPeer(ch),
		Limit: 1,
	})
	if err != nil {
		return 0, mapError(err)
	}
	modified, ok := res.AsModified()
	if !ok {
		return 0, fmt.Errorf("unexpected history response %T", res)
	}
	var top int64
	for _, item := range modified.GetMessages() {
		if id := int64(item.GetID()); id > top {
			top = id
		}
	}
	return top, nil
}

// Fetch returns every message newer than afterID, oldest first, or a
// *domain.BacklogError when there are more than max of them.
func (c *Client) Fetch(ctx context.Context, ch domain.Channel, afterID int64, max int) ([]domain.Message, error) {
	api, err := c.apiClient()
	if err != nil {
		return nil, err
	}
	page := func(ctx context.Context, offsetID, minID, limit int) (tg.ModifiedMessagesMessages, error) {
		res, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:     inputPeer(ch),
			OffsetID: offsetID,
			Limit:    limit,
			MinID:    minID,
		})
		if err != nil {
			return nil, mapError(err)
		}
		modified, ok := res.AsModified()
		if !ok {
			return nil, fmt.Errorf("unexpected history response %T", res)
		}
		return modified, nil
	}
	return collectNew(ctx, ch, page, afterID, max)
}

// Download fetches the bytes of a photo, refreshing the file reference once
// if Telegram reports it as expired.
func (c *Client) Download(ctx context.Context, ref domain.PhotoRef) ([]byte, error) {
	data, err := c.download(ctx, ref)
	if err == nil {
		return data, nil
	}
	if !tgerr.Is(err, "FILE_REFERENCE_EXPIRED", "FILE_REFERENCE_INVALID") {
		return nil, err
	}
	fresh, rerr := c.refreshPhoto(ctx, ref)
	if rerr != nil {
		return nil, fmt.Errorf("refresh file reference: %w", rerr)
	}
	return c.download(ctx, fresh)
}

func (c *Client) download(ctx context.Context, ref domain.PhotoRef) ([]byte, error) {
	api, err := c.apiClient()
	if err != nil {
		return nil, err
	}
	loc := &tg.InputPhotoFileLocation{
		ID:            ref.ID,
		AccessHash:    ref.AccessHash,
		FileReference: ref.FileReference,
		ThumbSize:     ref.SizeType,
	}
	var buf bytes.Buffer
	if _, err := downloader.NewDownloader().Download(api, loc).Stream(ctx, &buf); err != nil {
		return nil, mapError(err)
	}
	return buf.Bytes(), nil
}

// refreshPhoto re-reads the message to obtain a fresh file reference.
func (c *Client) refreshPhoto(ctx context.Context, ref domain.PhotoRef) (domain.PhotoRef, error) {
	api, err := c.apiClient()
	if err != nil {
		return ref, err
	}
	res, err := api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
		Channel: &tg.InputChannel{ChannelID: ref.ChannelID, AccessHash: ref.ChannelAccessHash},
		ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: int(ref.MessageID)}},
	})
	if err != nil {
		return ref, mapError(err)
	}
	modified, ok := res.AsModified()
	if !ok {
		return ref, fmt.Errorf("unexpected messages response %T", res)
	}
	ch := domain.Channel{ID: domain.ChannelID(ref.ChannelID), AccessHash: ref.ChannelAccessHash}
	for _, item := range modified.GetMessages() {
		m, ok := item.(*tg.Message)
		if !ok || int64(m.ID) != ref.MessageID {
			continue
		}
		if fresh := photoRef(ch, m); fresh != nil {
			return *fresh, nil
		}
	}
	return ref, fmt.Errorf("message %d no longer has a photo", ref.MessageID)
}

func inputPeer(ch domain.Channel) tg.InputPeerClass {
	return &tg.InputPeerChannel{ChannelID: int64(ch.ID), AccessHash: ch.AccessHash}
}
