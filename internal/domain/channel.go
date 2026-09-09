package domain

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ChannelID is the Telegram identifier of a channel.
type ChannelID int64

// Channel is a Telegram channel the service harvests.
type Channel struct {
	ID         ChannelID
	AccessHash int64
	Username   string
	Title      string
	// Cursor is the ID of the last message that was forwarded successfully.
	Cursor       int64
	Enabled      bool
	LastPolledAt time.Time
	LastError    string
	AddedAt      time.Time
}

// Link returns the public URL of the channel.
func (c Channel) Link() string {
	if c.Username != "" {
		return "https://t.me/" + c.Username
	}
	return "https://t.me/c/" + strconv.FormatInt(int64(c.ID), 10)
}

// MessageLink returns the public URL of a message in the channel.
func (c Channel) MessageLink(messageID int64) string {
	return c.Link() + "/" + strconv.FormatInt(messageID, 10)
}

// DisplayName returns the best human-readable name of the channel.
func (c Channel) DisplayName() string {
	if c.Title != "" {
		return c.Title
	}
	if c.Username != "" {
		return "@" + c.Username
	}
	return strconv.FormatInt(int64(c.ID), 10)
}

var usernameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{3,31}$`)

// ParseChannelRef extracts a normalized channel username from user input such as
// "@name", "name", "t.me/name", "https://t.me/s/name" or "https://t.me/name/123".
func ParseChannelRef(ref string) (string, error) {
	s := strings.TrimSpace(ref)
	if s == "" {
		return "", &ValidationError{Field: "channel", Msg: "is required"}
	}
	lower := strings.ToLower(s)
	for _, prefix := range []string{"https://", "http://"} {
		lower = strings.TrimPrefix(lower, prefix)
	}
	lower = strings.TrimPrefix(lower, "www.")
	for _, host := range []string{"t.me/", "telegram.me/", "telegram.dog/"} {
		if strings.HasPrefix(lower, host) {
			lower = strings.TrimPrefix(lower, host)
			lower = strings.TrimPrefix(lower, "s/")
			break
		}
	}
	if strings.HasPrefix(lower, "+") || strings.HasPrefix(lower, "joinchat/") {
		return "", &ValidationError{Field: "channel", Msg: "invite links are not supported; use a public @username"}
	}
	if i := strings.IndexAny(lower, "/?#"); i >= 0 {
		lower = lower[:i]
	}
	lower = strings.TrimPrefix(lower, "@")
	if !usernameRe.MatchString(lower) {
		return "", &ValidationError{Field: "channel", Msg: "must be a public channel username (letters, digits, underscores, 4–32 chars)"}
	}
	return lower, nil
}
