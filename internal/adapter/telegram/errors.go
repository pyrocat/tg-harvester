package telegram

import (
	"fmt"
	"strings"

	"github.com/gotd/td/tgerr"

	"tg-harvester/internal/domain"
)

// authError marks a failure of the interactive login flow. It unwraps to
// domain.ErrUnauthorized so the session manager stops and waits for the user
// instead of reconnecting in a loop, while its message stays short enough to
// show in the UI.
type authError struct {
	msg string
	err error
}

func (e *authError) Error() string   { return e.msg }
func (e *authError) Unwrap() []error { return []error{e.err, domain.ErrUnauthorized} }

// loginError turns a login-flow failure into a message worth showing to the
// user. gotd wraps errors several layers deep and appends a stack trace, so
// the underlying Telegram error is extracted where there is one.
func loginError(err error) error {
	msg := "login failed"
	switch rpc, ok := tgerr.As(err); {
	case ok:
		msg = fmt.Sprintf("Telegram rejected the login: %s (code %d)", rpc.Message, rpc.Code)
	case err != nil:
		msg = "login failed: " + firstLine(err.Error())
	}
	return &authError{msg: msg, err: err}
}

// firstLine keeps the summary of a multi-line wrapped error.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

// mapError translates Telegram RPC errors into domain errors.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if wait, ok := tgerr.AsFloodWait(err); ok {
		return &domain.FloodWaitError{Wait: wait}
	}
	if tgerr.IsCode(err, 401) {
		return &authError{msg: "the Telegram session is no longer valid; log in again", err: err}
	}
	if tgerr.Is(err, "CHANNEL_PRIVATE") {
		return fmt.Errorf("the account is not a member of this private channel: %w", err)
	}
	if tgerr.Is(err, "USERNAME_NOT_OCCUPIED", "USERNAME_INVALID") {
		return fmt.Errorf("no such public channel: %w", err)
	}
	if rpc, ok := tgerr.As(err); ok {
		return fmt.Errorf("telegram: %s (code %d)", rpc.Message, rpc.Code)
	}
	return err
}
