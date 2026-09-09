package domain

import "time"

// AuthState is the state of the Telegram login state machine.
type AuthState int

const (
	AuthNotConfigured AuthState = iota
	AuthConnecting
	AuthAwaitingCode
	AuthAwaitingPassword
	AuthAuthorized
	AuthFailed
	AuthStopped
)

func (s AuthState) String() string {
	switch s {
	case AuthNotConfigured:
		return "not configured"
	case AuthConnecting:
		return "connecting"
	case AuthAwaitingCode:
		return "awaiting code"
	case AuthAwaitingPassword:
		return "awaiting password"
	case AuthAuthorized:
		return "authorized"
	case AuthFailed:
		return "failed"
	case AuthStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

// Busy reports whether the login flow is in progress and the UI should poll.
func (s AuthState) Busy() bool {
	return s == AuthConnecting || s == AuthAwaitingCode || s == AuthAwaitingPassword
}

// AuthStatus describes the current Telegram session.
type AuthStatus struct {
	State    AuthState
	Phone    string
	Account  string
	CodeHint string
	Err      string
}

// PollStatus describes the harvesting scheduler.
type PollStatus struct {
	LastPollAt  time.Time
	NextPollAt  time.Time
	Polling     bool
	LastError   string
	PausedUntil time.Time
}
