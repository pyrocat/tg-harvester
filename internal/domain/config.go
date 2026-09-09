package domain

import (
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	hexHashRe    = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	phoneRe      = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)
	headerNameRe = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_` + "`" + `|~-]+$`)

	// reservedHeaders are owned by the forwarder; letting the user set them
	// would corrupt the request.
	reservedHeaders = map[string]bool{
		"content-type":      true,
		"content-length":    true,
		"host":              true,
		"transfer-encoding": true,
		"connection":        true,
		"x-idempotency-key": true,
	}
)

// Credentials identify the Telegram application and the user account.
type Credentials struct {
	APIID   int
	APIHash string
	Phone   string
}

// IsSet reports whether all credential fields are present.
func (c Credentials) IsSet() bool {
	return c.APIID != 0 && c.APIHash != "" && c.Phone != ""
}

// Validate checks the credentials for well-formedness.
func (c Credentials) Validate() error {
	if c.APIID <= 0 {
		return &ValidationError{Field: "api_id", Msg: "must be a positive integer"}
	}
	if !hexHashRe.MatchString(c.APIHash) {
		return &ValidationError{Field: "api_hash", Msg: "must be 32 hexadecimal characters"}
	}
	if !phoneRe.MatchString(c.Phone) {
		return &ValidationError{Field: "phone", Msg: "must be in international format, e.g. +12025550123"}
	}
	return nil
}

// NormalizePhone strips separators and ensures a leading plus sign.
func NormalizePhone(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "+" + b.String()
}

// Endpoint is the HTTP destination for harvested posts.
type Endpoint struct {
	URL         string
	HeaderName  string
	HeaderValue string
}

// IsSet reports whether a destination URL is configured.
func (e Endpoint) IsSet() bool {
	return e.URL != ""
}

// Validate checks the endpoint configuration.
func (e Endpoint) Validate() error {
	u, err := url.Parse(e.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return &ValidationError{Field: "endpoint_url", Msg: "must be an absolute http(s) URL"}
	}
	if (e.HeaderName == "") != (e.HeaderValue == "") {
		return &ValidationError{Field: "endpoint_header", Msg: "header name and value must both be set or both be empty"}
	}
	if e.HeaderName != "" && !headerNameRe.MatchString(e.HeaderName) {
		return &ValidationError{Field: "endpoint_header", Msg: "header name contains invalid characters"}
	}
	if reservedHeaders[strings.ToLower(e.HeaderName)] {
		return &ValidationError{Field: "endpoint_header", Msg: "header " + e.HeaderName + " is set by the forwarder and cannot be overridden"}
	}
	if strings.ContainsAny(e.HeaderValue, "\r\n") {
		return &ValidationError{Field: "endpoint_header", Msg: "header value must not contain line breaks"}
	}
	return nil
}

// Limits and defaults for the harvesting schedule.
const (
	MinPollInterval     = 10 * time.Second
	DefaultPollInterval = 60 * time.Second
	MaxPollInterval     = 24 * time.Hour
	DefaultSettleDelay  = 10 * time.Second
	MaxSettleDelay      = 10 * time.Minute
)

// Settings are the user-editable runtime settings.
type Settings struct {
	Endpoint     Endpoint
	PollInterval time.Duration
	// SettleDelay is how old a message must be before it is harvested,
	// so that albums still being uploaded are not split across polls.
	SettleDelay time.Duration
}

// DefaultSettings returns the settings used before the user changes anything.
func DefaultSettings() Settings {
	return Settings{PollInterval: DefaultPollInterval, SettleDelay: DefaultSettleDelay}
}

// Validate checks the settings. An empty endpoint is allowed so that the
// interval can be configured before the destination is known.
func (s Settings) Validate() error {
	if s.PollInterval < MinPollInterval || s.PollInterval > MaxPollInterval {
		return &ValidationError{Field: "poll_interval", Msg: "must be between " + MinPollInterval.String() + " and " + MaxPollInterval.String()}
	}
	if s.SettleDelay < 0 || s.SettleDelay > MaxSettleDelay {
		return &ValidationError{Field: "settle_delay", Msg: "must be between 0s and " + MaxSettleDelay.String()}
	}
	if s.Endpoint.IsSet() {
		return s.Endpoint.Validate()
	}
	return nil
}
