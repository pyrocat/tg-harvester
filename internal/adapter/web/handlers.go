package web

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"tg-harvester/internal/domain"
)

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	page := pageView{
		Account:  s.accountView(r, "", ""),
		Channels: s.channelsView(r, "", "", ""),
		Settings: s.settingsView(r, "", ""),
		Status:   s.statusView(r),
	}
	s.render(w, http.StatusOK, "layout", page)
}

// Account.

func (s *Server) accountView(r *http.Request, errMsg, notice string) accountView {
	v := accountView{Auth: s.deps.Account.Status(), Error: errMsg, Notice: notice}
	creds, err := s.deps.Account.Credentials(r.Context())
	if err != nil {
		s.deps.Log.Error("load credentials", "err", err)
		if v.Error == "" {
			v.Error = "could not load the stored credentials"
		}
		return v
	}
	v.Creds = creds
	return v
}

func (s *Server) renderAccount(w http.ResponseWriter, r *http.Request, errMsg, notice string) {
	s.render(w, http.StatusOK, "account", s.accountView(r, errMsg, notice))
}

func (s *Server) handleAccountPartial(w http.ResponseWriter, r *http.Request) {
	s.renderAccount(w, r, "", "")
}

func (s *Server) handleCredentials(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderAccount(w, r, "could not read the form", "")
		return
	}
	apiID, err := strconv.Atoi(r.FormValue("api_id"))
	if err != nil {
		s.renderAccount(w, r, "API ID must be a number", "")
		return
	}
	creds := domain.Credentials{
		APIID:   apiID,
		APIHash: r.FormValue("api_hash"),
		Phone:   domain.NormalizePhone(r.FormValue("phone")),
	}
	if err := s.deps.Account.Configure(r.Context(), creds); err != nil {
		s.renderAccount(w, r, userMessage(err, "could not start the login"), "")
		return
	}
	s.renderAccount(w, r, "", "Connecting to Telegram…")
}

func (s *Server) handleCode(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Account.SubmitCode(r.FormValue("code")); err != nil {
		s.renderAccount(w, r, userMessage(err, "could not submit the code"), "")
		return
	}
	s.renderAccount(w, r, "", "Signing in…")
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Account.SubmitPassword(r.FormValue("password")); err != nil {
		s.renderAccount(w, r, userMessage(err, "could not submit the password"), "")
		return
	}
	s.renderAccount(w, r, "", "Signing in…")
}

func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Account.Retry(r.Context()); err != nil {
		s.renderAccount(w, r, userMessage(err, "could not restart the login"), "")
		return
	}
	s.renderAccount(w, r, "", "Reconnecting…")
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Account.Logout(r.Context()); err != nil {
		s.renderAccount(w, r, userMessage(err, "could not log out"), "")
		return
	}
	s.renderAccount(w, r, "", "Logged out.")
}

// Channels.

func (s *Server) channelsView(r *http.Request, value, errMsg, notice string) channelsView {
	v := channelsView{
		Authorized: s.deps.Account.Status().State == domain.AuthAuthorized,
		Value:      value,
		Error:      errMsg,
		Notice:     notice,
	}
	channels, err := s.deps.Channels.List(r.Context())
	if err != nil {
		s.deps.Log.Error("list channels", "err", err)
		if v.Error == "" {
			v.Error = "could not load the channel list"
		}
		return v
	}
	v.Channels = channels
	return v
}

func (s *Server) renderChannels(w http.ResponseWriter, r *http.Request, value, errMsg, notice string) {
	s.render(w, http.StatusOK, "channels", s.channelsView(r, value, errMsg, notice))
}

func (s *Server) handleChannelsPartial(w http.ResponseWriter, r *http.Request) {
	s.renderChannels(w, r, "", "", "")
}

func (s *Server) handleAddChannel(w http.ResponseWriter, r *http.Request) {
	ref := r.FormValue("channel")
	ch, err := s.deps.Channels.Add(r.Context(), ref)
	if err != nil {
		s.renderChannels(w, r, ref, userMessage(err, "could not add the channel"), "")
		return
	}
	s.renderChannels(w, r, "", "", "Added "+ch.DisplayName()+"; harvesting starts with its next post.")
}

func (s *Server) handleRemoveChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := s.channelID(w, r)
	if !ok {
		return
	}
	if err := s.deps.Channels.Remove(r.Context(), id); err != nil {
		s.renderChannels(w, r, "", userMessage(err, "could not remove the channel"), "")
		return
	}
	s.renderChannels(w, r, "", "", "Channel removed.")
}

func (s *Server) handleToggleChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := s.channelID(w, r)
	if !ok {
		return
	}
	if err := s.deps.Channels.Toggle(r.Context(), id); err != nil {
		s.renderChannels(w, r, "", userMessage(err, "could not update the channel"), "")
		return
	}
	s.renderChannels(w, r, "", "", "")
}

func (s *Server) handleResetChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := s.channelID(w, r)
	if !ok {
		return
	}
	if err := s.deps.Channels.Reset(r.Context(), id); err != nil {
		s.renderChannels(w, r, "", userMessage(err, "could not reset the channel"), "")
		return
	}
	s.renderChannels(w, r, "", "", "Skipped ahead to the newest post.")
}

func (s *Server) channelID(w http.ResponseWriter, r *http.Request) (domain.ChannelID, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid channel id", http.StatusBadRequest)
		return 0, false
	}
	return domain.ChannelID(id), true
}

// Settings.

func (s *Server) settingsView(r *http.Request, errMsg, notice string) settingsView {
	v := settingsView{Error: errMsg, Notice: notice}
	settings, err := s.deps.Settings.Get(r.Context())
	if err != nil {
		s.deps.Log.Error("load settings", "err", err)
		if v.Error == "" {
			v.Error = "could not load the settings"
		}
		return v
	}
	v.Settings = settings
	v.IntervalSec = int64(settings.PollInterval / time.Second)
	v.SettleSec = int64(settings.SettleDelay / time.Second)
	return v
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, errMsg, notice string) {
	s.render(w, http.StatusOK, "settings", s.settingsView(r, errMsg, notice))
}

func (s *Server) handleSettingsPartial(w http.ResponseWriter, r *http.Request) {
	s.renderSettings(w, r, "", "")
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderSettings(w, r, "could not read the form", "")
		return
	}
	interval, err := strconv.Atoi(r.FormValue("poll_interval"))
	if err != nil {
		s.renderSettings(w, r, "the poll interval must be a number of seconds", "")
		return
	}
	settle, err := strconv.Atoi(r.FormValue("settle_delay"))
	if err != nil {
		s.renderSettings(w, r, "the settle delay must be a number of seconds", "")
		return
	}
	settings := domain.Settings{
		Endpoint: domain.Endpoint{
			URL:         r.FormValue("endpoint_url"),
			HeaderName:  r.FormValue("header_name"),
			HeaderValue: r.FormValue("header_value"),
		},
		PollInterval: time.Duration(interval) * time.Second,
		SettleDelay:  time.Duration(settle) * time.Second,
	}
	if err := s.deps.Settings.Update(r.Context(), settings); err != nil {
		s.renderSettings(w, r, userMessage(err, "could not save the settings"), "")
		return
	}
	s.renderSettings(w, r, "", "Saved.")
}

func (s *Server) handleTestEndpoint(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Settings.SendTest(r.Context()); err != nil {
		s.renderSettings(w, r, userMessage(err, "the endpoint did not accept the test payload"), "")
		return
	}
	s.renderSettings(w, r, "", "The endpoint accepted the test payload.")
}

// Status.

func (s *Server) statusView(r *http.Request) statusView {
	status, err := s.deps.Status.Status(r.Context())
	if err != nil {
		s.deps.Log.Error("load status", "err", err)
	}
	return statusView{Status: status}
}

func (s *Server) handleStatusPartial(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "status", s.statusView(r))
}

func (s *Server) handlePollNow(w http.ResponseWriter, r *http.Request) {
	s.deps.Harvest.Trigger()
	s.render(w, http.StatusOK, "status", s.statusView(r))
}

// userMessage turns an error into text for the UI: validation errors and
// missing configuration are shown verbatim, anything else is generic and
// the detail goes to the log.
func userMessage(err error, fallback string) string {
	var ve *domain.ValidationError
	if errors.As(err, &ve) {
		return ve.Msg
	}
	if errors.Is(err, domain.ErrNotConfigured) {
		return "not configured yet"
	}
	if errors.Is(err, domain.ErrNotConnected) {
		return "Telegram is not connected"
	}
	if err != nil {
		return fallback + ": " + err.Error()
	}
	return fallback
}
