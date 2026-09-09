package domain

import (
	"strings"
	"time"
)

// Author is who published a message: a signed post author, the user who
// posted in a discussion group, or the channel itself.
type Author struct {
	Name     string
	Username string
}

// PhotoRef is an opaque handle to a photo that the message source can
// download. It also carries what is needed to refresh an expired file
// reference: the channel the photo was posted in and the message ID.
type PhotoRef struct {
	ChannelID         int64
	ChannelAccessHash int64
	MessageID         int64
	ID                int64
	AccessHash        int64
	FileReference     []byte
	SizeType          string
	Width             int
	Height            int
}

// Photo is a downloaded image.
type Photo struct {
	FileName string
	MIME     string
	Width    int
	Height   int
	Data     []byte
}

// Message is a single Telegram message as fetched from a channel.
type Message struct {
	ID        int64
	GroupedID int64
	Date      time.Time
	Text      string
	Author    Author
	Photo     *PhotoRef
}

// Post is what gets forwarded: one message, or one album of messages merged.
type Post struct {
	Channel    Channel
	MessageIDs []int64
	GroupedID  int64
	Date       time.Time
	Text       string
	Author     Author
	PhotoRefs  []PhotoRef
	Photos     []Photo
}

// FirstID returns the lowest message ID of the post.
func (p Post) FirstID() int64 {
	if len(p.MessageIDs) == 0 {
		return 0
	}
	return p.MessageIDs[0]
}

// MaxID returns the highest message ID of the post; the cursor advances to it.
func (p Post) MaxID() int64 {
	if len(p.MessageIDs) == 0 {
		return 0
	}
	return p.MessageIDs[len(p.MessageIDs)-1]
}

// Link returns the public URL of the post.
func (p Post) Link() string {
	return p.Channel.MessageLink(p.FirstID())
}

// GroupPosts merges adjacent messages that share a non-zero GroupedID into a
// single Post. msgs must be sorted ascending by ID and must contain every
// message of each album it includes: the caller fetches the whole unseen
// range, so the only album that can be incomplete is one still being
// uploaded, which the settle delay keeps out.
func GroupPosts(ch Channel, msgs []Message) []Post {
	var posts []Post
	for i := 0; i < len(msgs); {
		first := msgs[i]
		j := i + 1
		if first.GroupedID != 0 {
			for j < len(msgs) && msgs[j].GroupedID == first.GroupedID {
				j++
			}
		}
		posts = append(posts, buildPost(ch, msgs[i:j]))
		i = j
	}
	return posts
}

func buildPost(ch Channel, group []Message) Post {
	post := Post{
		Channel:   ch,
		GroupedID: group[0].GroupedID,
		Date:      group[0].Date,
		Author:    group[0].Author,
	}
	var texts []string
	for _, m := range group {
		post.MessageIDs = append(post.MessageIDs, m.ID)
		if t := strings.TrimSpace(m.Text); t != "" {
			texts = append(texts, t)
		}
		if m.Photo != nil {
			post.PhotoRefs = append(post.PhotoRefs, *m.Photo)
		}
		if post.Author.Name == "" && m.Author.Name != "" {
			post.Author = m.Author
		}
	}
	post.Text = strings.Join(texts, "\n\n")
	return post
}
