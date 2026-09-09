// Package forward delivers harvested posts to an HTTP endpoint as JSON.
package forward

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"tg-harvester/internal/domain"
)

// Forwarder posts JSON payloads with base64-encoded photos.
type Forwarder struct {
	client    *http.Client
	userAgent string
}

// New creates a Forwarder using the given HTTP client.
func New(client *http.Client) *Forwarder {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &Forwarder{client: client, userAgent: "tg-harvester/1.0"}
}

// Payload is the JSON document sent to the endpoint.
type Payload struct {
	Channel    Channel   `json:"channel"`
	MessageID  int64     `json:"message_id"`
	MessageIDs []int64   `json:"message_ids"`
	GroupedID  string    `json:"grouped_id,omitempty"`
	Link       string    `json:"link"`
	Date       time.Time `json:"date"`
	Text       string    `json:"text"`
	Author     Author    `json:"author"`
	Photos     []Photo   `json:"photos"`
}

// Channel identifies the source channel.
type Channel struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Username string `json:"username"`
}

// Author identifies who published the post.
type Author struct {
	Name     string `json:"name"`
	Username string `json:"username"`
}

// Photo carries image bytes; encoding/json emits Data as base64.
type Photo struct {
	FileName string `json:"filename"`
	MIME     string `json:"mime"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Data     []byte `json:"data"`
}

// BuildPayload converts a domain post into the wire format.
func BuildPayload(post domain.Post) Payload {
	p := Payload{
		Channel:    Channel{ID: int64(post.Channel.ID), Title: post.Channel.Title, Username: post.Channel.Username},
		MessageID:  post.FirstID(),
		MessageIDs: append([]int64{}, post.MessageIDs...),
		Link:       post.Link(),
		Date:       post.Date.UTC(),
		Text:       post.Text,
		Author:     Author{Name: post.Author.Name, Username: post.Author.Username},
		Photos:     make([]Photo, 0, len(post.Photos)),
	}
	if post.GroupedID != 0 {
		p.GroupedID = strconv.FormatInt(post.GroupedID, 10)
	}
	if p.MessageIDs == nil {
		p.MessageIDs = []int64{}
	}
	for _, ph := range post.Photos {
		p.Photos = append(p.Photos, Photo{FileName: ph.FileName, MIME: ph.MIME, Width: ph.Width, Height: ph.Height, Data: ph.Data})
	}
	return p
}

// Forward POSTs the post to the endpoint. Responses other than 2xx are
// errors; 4xx responses (except 408, 425 and 429) are permanent.
func (f *Forwarder) Forward(ctx context.Context, target domain.Endpoint, post domain.Post) error {
	body, err := json.Marshal(BuildPayload(post))
	if err != nil {
		return &domain.PermanentError{Err: fmt.Errorf("encode payload: %w", err)}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.URL, bytes.NewReader(body))
	if err != nil {
		return &domain.PermanentError{Err: fmt.Errorf("build request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("X-Idempotency-Key", fmt.Sprintf("%d:%d", post.Channel.ID, post.MaxID()))
	if target.HeaderName != "" {
		req.Header.Set(target.HeaderName, target.HeaderValue)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("post to endpoint: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusRequestTimeout,
		resp.StatusCode == http.StatusTooEarly,
		resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode >= 500:
		return fmt.Errorf("endpoint returned %s", resp.Status)
	default:
		return &domain.PermanentError{Err: fmt.Errorf("endpoint returned %s", resp.Status)}
	}
}
