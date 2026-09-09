// Package domain holds the core entities and rules of tg-harvester.
// It depends on the standard library only.
package domain

import (
	"errors"
	"fmt"
	"time"
)

var (
	// ErrNotFound is returned when a requested record does not exist.
	ErrNotFound = errors.New("not found")
	// ErrNotConnected is returned when the Telegram client is not connected.
	ErrNotConnected = errors.New("telegram client is not connected")
	// ErrUnauthorized is returned when the Telegram session is missing or revoked.
	ErrUnauthorized = errors.New("telegram session is not authorized")
	// ErrNotConfigured is returned when a required configuration is missing.
	ErrNotConfigured = errors.New("not configured")
	// ErrAlreadyExists is returned when a record with the same key exists.
	ErrAlreadyExists = errors.New("already exists")
)

// PermanentError marks a failure that will not succeed if retried.
type PermanentError struct {
	Err error
}

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// IsPermanent reports whether err is wrapped in a PermanentError.
func IsPermanent(err error) bool {
	var pe *PermanentError
	return errors.As(err, &pe)
}

// FloodWaitError reports that Telegram asked the client to back off.
type FloodWaitError struct {
	Wait time.Duration
}

func (e *FloodWaitError) Error() string {
	return fmt.Sprintf("telegram flood wait: retry after %s", e.Wait)
}

// BacklogError reports that a channel has accumulated more unseen messages
// than the harvester is willing to fetch in one go. Delivering only the
// newest part would silently skip the older ones, so nothing is delivered
// until the user skips ahead.
type BacklogError struct {
	Limit int
}

func (e *BacklogError) Error() string {
	return fmt.Sprintf("more than %d unseen messages; use \"Skip to latest\" to resume from the newest post", e.Limit)
}

// ValidationError describes invalid user input.
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return e.Msg
	}
	return e.Field + ": " + e.Msg
}
