package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"tg-harvester/internal/app"
	"tg-harvester/internal/domain"
)

type fakeAccount struct {
	status    domain.AuthStatus
	creds     domain.Credentials
	configErr error
	codes     []string
	passwords []string
	submitErr error
	loggedOut bool
	retried   bool
}

func (f *fakeAccount) Status() domain.AuthStatus { return f.status }
func (f *fakeAccount) Credentials(context.Context) (domain.Credentials, error) {
	return f.creds, nil
}

func (f *fakeAccount) Configure(_ context.Context, c domain.Credentials) error {
	if f.configErr != nil {
		return f.configErr
	}
	f.creds = c
	f.status.State = domain.AuthConnecting
	return nil
}

func (f *fakeAccount) SubmitCode(code string) error {
	if f.submitErr != nil {
		return f.submitErr
	}
	f.codes = append(f.codes, code)
	return nil
}

func (f *fakeAccount) SubmitPassword(pw string) error {
	if f.submitErr != nil {
		return f.submitErr
	}
	f.passwords = append(f.passwords, pw)
	return nil
}

func (f *fakeAccount) Retry(context.Context) error  { f.retried = true; return nil }
func (f *fakeAccount) Logout(context.Context) error { f.loggedOut = true; return nil }

type fakeChannels struct {
	channels []domain.Channel
	addErr   error
	added    []string
	removed  []domain.ChannelID
	toggled  []domain.ChannelID
	reset    []domain.ChannelID
}

func (f *fakeChannels) List(context.Context) ([]domain.Channel, error) { return f.channels, nil }

func (f *fakeChannels) Add(_ context.Context, ref string) (domain.Channel, error) {
	if f.addErr != nil {
		return domain.Channel{}, f.addErr
	}
	f.added = append(f.added, ref)
	ch := domain.Channel{ID: 1, Username: strings.TrimPrefix(ref, "@"), Title: "Added", Enabled: true}
	f.channels = append(f.channels, ch)
	return ch, nil
}

func (f *fakeChannels) Remove(_ context.Context, id domain.ChannelID) error {
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeChannels) Toggle(_ context.Context, id domain.ChannelID) error {
	f.toggled = append(f.toggled, id)
	return nil
}

func (f *fakeChannels) Reset(_ context.Context, id domain.ChannelID) error {
	f.reset = append(f.reset, id)
	return nil
}

type fakeSettings struct {
	settings domain.Settings
	saved    *domain.Settings
	testErr  error
	tested   bool
}

func (f *fakeSettings) Get(context.Context) (domain.Settings, error) { return f.settings, nil }

func (f *fakeSettings) Update(_ context.Context, s domain.Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	f.settings, f.saved = s, &s
	return nil
}

func (f *fakeSettings) SendTest(context.Context) error {
	f.tested = true
	return f.testErr
}

type fakeStatus struct{ status app.Status }

func (f *fakeStatus) Status(context.Context) (app.Status, error) { return f.status, nil }

type fakeTrigger struct{ triggered int }

func (f *fakeTrigger) Trigger() { f.triggered++ }

type harness struct {
	handler  http.Handler
	account  *fakeAccount
	channels *fakeChannels
	settings *fakeSettings
	status   *fakeStatus
	trigger  *fakeTrigger
}

func newHarness(t *testing.T, basicAuth string) *harness {
	t.Helper()
	h := &harness{
		account:  &fakeAccount{status: domain.AuthStatus{State: domain.AuthNotConfigured}},
		channels: &fakeChannels{},
		settings: &fakeSettings{settings: domain.DefaultSettings()},
		status:   &fakeStatus{status: app.Status{Now: time.Now()}},
		trigger:  &fakeTrigger{},
	}
	handler, err := New(Deps{
		Account:  h.account,
		Channels: h.channels,
		Settings: h.settings,
		Status:   h.status,
		Harvest:  h.trigger,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, basicAuth)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.handler = handler
	return h
}

func (h *harness) do(t *testing.T, method, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func mustContain(t *testing.T, rec *httptest.ResponseRecorder, want ...string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	for _, w := range want {
		if !strings.Contains(rec.Body.String(), w) {
			t.Errorf("body does not contain %q\n%s", w, rec.Body.String())
		}
	}
}

func TestIndexRendersAllCards(t *testing.T) {
	h := newHarness(t, "")
	rec := h.do(t, http.MethodGet, "/", nil)
	mustContain(t, rec, `id="status"`, `id="account"`, `id="channels"`, `id="settings"`, "htmx.min.js")
}

func TestHealthzAndStatic(t *testing.T) {
	h := newHarness(t, "")
	if rec := h.do(t, http.MethodGet, "/healthz", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}
	rec := h.do(t, http.MethodGet, "/static/app.css", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), ".card") {
		t.Fatalf("static css = %d", rec.Code)
	}
}

func TestAccountLoginFlow(t *testing.T) {
	h := newHarness(t, "")

	rec := h.do(t, http.MethodPost, "/account/credentials", url.Values{
		"api_id":   {"12345"},
		"api_hash": {strings.Repeat("a", 32)},
		"phone":    {"+1 (202) 555-0123"},
	})
	mustContain(t, rec, `id="account"`)
	if h.account.creds.APIID != 12345 || h.account.creds.Phone != "+12025550123" {
		t.Fatalf("credentials = %+v; phone must be normalized", h.account.creds)
	}

	rec = h.do(t, http.MethodPost, "/account/credentials", url.Values{"api_id": {"abc"}})
	mustContain(t, rec, "API ID must be a number")

	h.account.status = domain.AuthStatus{State: domain.AuthAwaitingCode, CodeHint: "SMS", Phone: "+12025550123"}
	rec = h.do(t, http.MethodGet, "/partials/account", nil)
	mustContain(t, rec, `name="code"`, "via SMS", "every 2s")

	rec = h.do(t, http.MethodPost, "/account/code", url.Values{"code": {"54321"}})
	mustContain(t, rec, `id="account"`)
	if len(h.account.codes) != 1 || h.account.codes[0] != "54321" {
		t.Fatalf("codes = %v", h.account.codes)
	}

	h.account.status = domain.AuthStatus{State: domain.AuthAwaitingPassword}
	rec = h.do(t, http.MethodPost, "/account/password", url.Values{"password": {"secret"}})
	mustContain(t, rec, `id="account"`)
	if len(h.account.passwords) != 1 {
		t.Fatalf("passwords = %v", h.account.passwords)
	}

	h.account.status = domain.AuthStatus{State: domain.AuthAuthorized, Account: "Jane Doe"}
	rec = h.do(t, http.MethodGet, "/partials/account", nil)
	mustContain(t, rec, "Jane Doe", "Log out")
	if strings.Contains(rec.Body.String(), "every 2s") {
		t.Error("an authorized card must not poll")
	}

	rec = h.do(t, http.MethodPost, "/account/logout", nil)
	mustContain(t, rec, `id="account"`)
	if !h.account.loggedOut {
		t.Error("logout was not called")
	}
}

func TestAccountSubmitErrorIsShown(t *testing.T) {
	h := newHarness(t, "")
	h.account.status = domain.AuthStatus{State: domain.AuthAwaitingCode}
	h.account.submitErr = &domain.ValidationError{Msg: "the login flow is not waiting for this input"}
	rec := h.do(t, http.MethodPost, "/account/code", url.Values{"code": {"1"}})
	mustContain(t, rec, "the login flow is not waiting for this input")
}

func TestChannelsCRUD(t *testing.T) {
	h := newHarness(t, "")
	h.account.status = domain.AuthStatus{State: domain.AuthAuthorized}

	rec := h.do(t, http.MethodPost, "/channels", url.Values{"channel": {"@durov"}})
	mustContain(t, rec, "Added", "durov")
	if len(h.channels.added) != 1 || h.channels.added[0] != "@durov" {
		t.Fatalf("added = %v", h.channels.added)
	}

	// A rejected reference is echoed back so the user can correct it.
	h.channels.addErr = &domain.ValidationError{Field: "channel", Msg: "invite links are not supported"}
	rec = h.do(t, http.MethodPost, "/channels", url.Values{"channel": {"https://t.me/joinchat/AbCdEf"}})
	mustContain(t, rec, "invite links are not supported", "https://t.me/joinchat/AbCdEf")
	h.channels.addErr = nil

	rec = h.do(t, http.MethodPost, "/channels/1/toggle", nil)
	mustContain(t, rec, `id="channels"`)
	rec = h.do(t, http.MethodPost, "/channels/1/reset", nil)
	mustContain(t, rec, "Skipped ahead")
	rec = h.do(t, http.MethodDelete, "/channels/1", nil)
	mustContain(t, rec, "Channel removed")
	if len(h.channels.toggled) != 1 || len(h.channels.reset) != 1 || len(h.channels.removed) != 1 {
		t.Fatalf("toggled=%v reset=%v removed=%v", h.channels.toggled, h.channels.reset, h.channels.removed)
	}

	if rec := h.do(t, http.MethodDelete, "/channels/abc", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id status = %d", rec.Code)
	}
}

func TestChannelsDisabledWhenNotAuthorized(t *testing.T) {
	h := newHarness(t, "")
	rec := h.do(t, http.MethodGet, "/partials/channels", nil)
	mustContain(t, rec, "Sign in to Telegram to add channels", "disabled")
}

func TestSettingsUpdate(t *testing.T) {
	h := newHarness(t, "")
	rec := h.do(t, http.MethodPost, "/settings", url.Values{
		"endpoint_url":  {"https://example.com/hook"},
		"header_name":   {"Authorization"},
		"header_value":  {"Bearer x"},
		"poll_interval": {"120"},
		"settle_delay":  {"5"},
	})
	mustContain(t, rec, "Saved.", "https://example.com/hook")
	if h.settings.saved == nil || h.settings.saved.PollInterval != 2*time.Minute || h.settings.saved.SettleDelay != 5*time.Second {
		t.Fatalf("saved = %+v", h.settings.saved)
	}

	rec = h.do(t, http.MethodPost, "/settings", url.Values{
		"endpoint_url":  {"not-a-url"},
		"poll_interval": {"60"},
		"settle_delay":  {"10"},
	})
	mustContain(t, rec, "must be an absolute http(s) URL")

	rec = h.do(t, http.MethodPost, "/settings", url.Values{"poll_interval": {"x"}, "settle_delay": {"1"}})
	mustContain(t, rec, "poll interval must be a number")
}

func TestSettingsTestEndpoint(t *testing.T) {
	h := newHarness(t, "")
	rec := h.do(t, http.MethodPost, "/settings/test", nil)
	mustContain(t, rec, "accepted the test payload")
	if !h.settings.tested {
		t.Error("SendTest was not called")
	}
	h.settings.testErr = &domain.ValidationError{Field: "endpoint_url", Msg: "configure the endpoint first"}
	rec = h.do(t, http.MethodPost, "/settings/test", nil)
	mustContain(t, rec, "configure the endpoint first")
}

func TestStatusAndPollNow(t *testing.T) {
	h := newHarness(t, "")
	h.status.status = app.Status{
		Now:  time.Now(),
		Auth: domain.AuthStatus{State: domain.AuthAuthorized, Account: "Jane"},
		Poll: domain.PollStatus{LastError: "endpoint refused"},
		Settings: domain.Settings{
			Endpoint: domain.Endpoint{URL: "https://example.com/hook"}, PollInterval: time.Minute,
		},
		Channels: []domain.Channel{{ID: 1, Username: "durov", Title: "Durov", Cursor: 42, LastError: "boom"}},
	}
	rec := h.do(t, http.MethodGet, "/partials/status", nil)
	mustContain(t, rec, "Durov", "42", "boom", "endpoint refused", "https://example.com/hook", "every 5s")

	rec = h.do(t, http.MethodPost, "/poll", nil)
	mustContain(t, rec, `id="status"`)
	if h.trigger.triggered != 1 {
		t.Fatalf("triggered = %d", h.trigger.triggered)
	}
}

func TestBasicAuth(t *testing.T) {
	h := newHarness(t, "admin:hunter2")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("anonymous = %d, %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("admin", "wrong")
	rec = httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("admin", "hunter2")
	rec = httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("correct password = %d", rec.Code)
	}
}

func TestCrossOriginPostRejected(t *testing.T) {
	h := newHarness(t, "")
	req := httptest.NewRequest(http.MethodPost, "/poll", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST = %d, want 403", rec.Code)
	}
	if h.trigger.triggered != 0 {
		t.Error("a cross-origin request must not reach the handler")
	}
}
