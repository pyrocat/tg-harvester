// Package telegram adapts the gotd MTProto client to the ports of the app
// package. All gotd types stay inside this package.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"

	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"

	"tg-harvester/internal/app"
	"tg-harvester/internal/domain"
)

// Client runs a Telegram user session and serves message reads from it.
type Client struct {
	sessions SessionStore
	log      *slog.Logger
	device   telegram.DeviceConfig

	// mu guards api, which is published only while the client is connected.
	mu  sync.RWMutex
	api *tg.Client
}

// New creates a Client. Call Run to connect.
func New(sessions SessionStore, log *slog.Logger) *Client {
	return &Client{
		sessions: sessions,
		log:      log,
		device: telegram.DeviceConfig{
			DeviceModel:    "Desktop",
			SystemVersion:  runtime.GOOS,
			AppVersion:     "tg-harvester 1.0",
			SystemLangCode: "en",
			LangCode:       "en",
		},
	}
}

var _ app.TelegramRunner = (*Client)(nil)

// Run connects, authorizes (prompting p for the code and password when the
// stored session is missing or expired), reports the account through ready
// and then serves API calls until ctx is cancelled. gotd's client is
// single-use, so a fresh one is built on every call.
func (c *Client) Run(ctx context.Context, creds domain.Credentials, p app.AuthPrompter, ready func(string)) error {
	client := telegram.NewClient(creds.APIID, creds.APIHash, telegram.Options{
		SessionStorage: sessionStorage{store: c.sessions},
		Middlewares:    []telegram.Middleware{floodwait.NewSimpleWaiter()},
		Device:         c.device,
		// The harvester polls history; it does not need the update stream.
		NoUpdates: true,
	})

	// gotd re-wraps whatever the callback returns with its own trace, which
	// is far too noisy for the status card, so the inner error is kept and
	// returned as-is.
	var inner error
	err := client.Run(ctx, func(ctx context.Context) error {
		inner = c.serve(ctx, client, creds, p, ready)
		return inner
	})
	switch {
	case inner != nil:
		return inner
	case err != nil && ctx.Err() == nil:
		return fmt.Errorf("telegram connection: %s", firstLine(err.Error()))
	default:
		return err
	}
}

// serve authorizes the account and then keeps the connection open, publishing
// the API for reads, until ctx is cancelled.
func (c *Client) serve(ctx context.Context, client *telegram.Client, creds domain.Credentials, p app.AuthPrompter, ready func(string)) error {
	status, err := client.Auth().Status(ctx)
	if err != nil {
		return fmt.Errorf("auth status: %w", mapError(err))
	}
	if !status.Authorized {
		flow := auth.NewFlow(&authenticator{phone: creds.Phone, prompt: p}, auth.SendCodeOptions{})
		if err := flow.Run(ctx, client.Auth()); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.log.Warn("telegram login failed", "err", err)
			return loginError(err)
		}
	}
	self, err := client.Self(ctx)
	if err != nil {
		return fmt.Errorf("get self: %w", mapError(err))
	}

	c.setAPI(client.API())
	defer c.setAPI(nil)
	ready(userName(self))

	<-ctx.Done()
	return ctx.Err()
}

// Logout terminates the authorization on Telegram's side.
func (c *Client) Logout(ctx context.Context) error {
	api, err := c.apiClient()
	if err != nil {
		return err
	}
	if _, err := api.AuthLogOut(ctx); err != nil {
		return fmt.Errorf("log out: %w", mapError(err))
	}
	return nil
}

func (c *Client) setAPI(api *tg.Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.api = api
}

// apiClient returns the API of the connected client.
func (c *Client) apiClient() (*tg.Client, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.api == nil {
		return nil, domain.ErrNotConnected
	}
	return c.api, nil
}

// authenticator bridges gotd's login flow to the web UI through the prompter.
type authenticator struct {
	phone  string
	prompt app.AuthPrompter
}

var _ auth.UserAuthenticator = (*authenticator)(nil)

func (a *authenticator) Phone(context.Context) (string, error) {
	return a.phone, nil
}

func (a *authenticator) Code(ctx context.Context, sentCode *tg.AuthSentCode) (string, error) {
	return a.prompt.AskCode(ctx, codeHint(sentCode))
}

func (a *authenticator) Password(ctx context.Context) (string, error) {
	return a.prompt.AskPassword(ctx)
}

func (a *authenticator) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error {
	return nil
}

func (a *authenticator) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("this phone number is not registered with Telegram; sign up in a Telegram app first")
}

// codeHint describes where Telegram sent the confirmation code.
func codeHint(sentCode *tg.AuthSentCode) string {
	if sentCode == nil {
		return ""
	}
	switch sentCode.Type.(type) {
	case *tg.AuthSentCodeTypeApp:
		return "Telegram app"
	case *tg.AuthSentCodeTypeSMS:
		return "SMS"
	case *tg.AuthSentCodeTypeCall:
		return "phone call"
	case *tg.AuthSentCodeTypeFlashCall, *tg.AuthSentCodeTypeMissedCall:
		return "missed call"
	case *tg.AuthSentCodeTypeEmailCode:
		return "email"
	default:
		return ""
	}
}
