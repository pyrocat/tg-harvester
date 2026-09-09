// Package web serves the htmx configuration UI.
package web

import (
	"context"
	"log/slog"
	"net/http"

	"tg-harvester/internal/app"
	"tg-harvester/internal/domain"
)

// AccountService drives the Telegram login.
type AccountService interface {
	Status() domain.AuthStatus
	Credentials(ctx context.Context) (domain.Credentials, error)
	Configure(ctx context.Context, creds domain.Credentials) error
	SubmitCode(code string) error
	SubmitPassword(password string) error
	Retry(ctx context.Context) error
	Logout(ctx context.Context) error
}

// ChannelService manages the harvested channels.
type ChannelService interface {
	List(ctx context.Context) ([]domain.Channel, error)
	Add(ctx context.Context, ref string) (domain.Channel, error)
	Remove(ctx context.Context, id domain.ChannelID) error
	Toggle(ctx context.Context, id domain.ChannelID) error
	Reset(ctx context.Context, id domain.ChannelID) error
}

// SettingsService manages the endpoint and schedule.
type SettingsService interface {
	Get(ctx context.Context) (domain.Settings, error)
	Update(ctx context.Context, s domain.Settings) error
	SendTest(ctx context.Context) error
}

// StatusService reports the dashboard status.
type StatusService interface {
	Status(ctx context.Context) (app.Status, error)
}

// Trigger requests an immediate harvest.
type Trigger interface {
	Trigger()
}

// Deps are the services the handlers depend on.
type Deps struct {
	Account  AccountService
	Channels ChannelService
	Settings SettingsService
	Status   StatusService
	Harvest  Trigger
	Log      *slog.Logger
}

// Server holds the parsed templates and the service dependencies.
type Server struct {
	deps Deps
	tpl  *templates
}

// New builds the HTTP handler for the web UI. basicAuth is "user:password";
// when empty the UI is served without authentication.
func New(deps Deps, basicAuth string) (http.Handler, error) {
	tpl, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	s := &Server{deps: deps, tpl: tpl}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler()))

	mux.HandleFunc("GET /partials/account", s.handleAccountPartial)
	mux.HandleFunc("POST /account/credentials", s.handleCredentials)
	mux.HandleFunc("POST /account/code", s.handleCode)
	mux.HandleFunc("POST /account/password", s.handlePassword)
	mux.HandleFunc("POST /account/retry", s.handleRetry)
	mux.HandleFunc("POST /account/logout", s.handleLogout)

	mux.HandleFunc("GET /partials/channels", s.handleChannelsPartial)
	mux.HandleFunc("POST /channels", s.handleAddChannel)
	mux.HandleFunc("DELETE /channels/{id}", s.handleRemoveChannel)
	mux.HandleFunc("POST /channels/{id}/toggle", s.handleToggleChannel)
	mux.HandleFunc("POST /channels/{id}/reset", s.handleResetChannel)

	mux.HandleFunc("GET /partials/settings", s.handleSettingsPartial)
	mux.HandleFunc("POST /settings", s.handleUpdateSettings)
	mux.HandleFunc("POST /settings/test", s.handleTestEndpoint)

	mux.HandleFunc("GET /partials/status", s.handleStatusPartial)
	mux.HandleFunc("POST /poll", s.handlePollNow)

	var h http.Handler = mux
	h = basicAuthMiddleware(basicAuth, h)
	h = http.NewCrossOriginProtection().Handler(h)
	h = recoverMiddleware(deps.Log, h)
	h = logMiddleware(deps.Log, h)
	return h, nil
}
