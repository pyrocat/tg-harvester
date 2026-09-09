package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"time"

	"tg-harvester/internal/app"
	"tg-harvester/internal/domain"
)

//go:embed templates/*.html templates/partials/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// staticHandler serves the embedded assets.
func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		fileServer.ServeHTTP(w, r)
	})
}

type templates struct {
	tpl *template.Template
}

func parseTemplates() (*templates, error) {
	tpl, err := template.New("").Funcs(templateFuncs()).ParseFS(templateFS,
		"templates/*.html", "templates/partials/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return &templates{tpl: tpl}, nil
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"timeOr": func(t time.Time, fallback string) string {
			if t.IsZero() {
				return fallback
			}
			return t.Local().Format("15:04:05")
		},
		"ago": func(now, t time.Time) string {
			if t.IsZero() {
				return ""
			}
			d := now.Sub(t).Truncate(time.Second)
			if d < 0 {
				return "in " + (-d).String()
			}
			return d.String() + " ago"
		},
		"seconds": func(d time.Duration) int64 { return int64(d / time.Second) },
	}
}

// render writes a template, buffering first so a failure cannot emit a
// half-written page.
func (s *Server) render(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := s.tpl.tpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.deps.Log.Error("render template", "template", name, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// View models. Each partial is rendered on its own, so each one carries
// everything its template needs.

type accountView struct {
	Auth   domain.AuthStatus
	Creds  domain.Credentials
	Error  string
	Notice string
}

type channelsView struct {
	Channels   []domain.Channel
	Authorized bool
	Value      string
	Error      string
	Notice     string
}

type settingsView struct {
	Settings    domain.Settings
	IntervalSec int64
	SettleSec   int64
	Error       string
	Notice      string
}

type statusView struct {
	Status app.Status
}

type pageView struct {
	Account  accountView
	Channels channelsView
	Settings settingsView
	Status   statusView
}
