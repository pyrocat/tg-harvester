package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// logMiddleware logs one line per request.
func logMiddleware(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Debug("http request",
			"method", r.Method, "path", r.URL.Path, "status", rec.status, "duration", time.Since(start))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// recoverMiddleware turns a panic into a 500 instead of killing the process.
func recoverMiddleware(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Error("panic in handler", "path", r.URL.Path, "panic", v)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// basicAuthMiddleware protects the UI when credentials are configured.
// The stored Telegram credentials make this worth having by default.
func basicAuthMiddleware(userPass string, next http.Handler) http.Handler {
	if userPass == "" {
		return next
	}
	user, pass, _ := strings.Cut(userPass, ":")
	wantUser, wantPass := sha256.Sum256([]byte(user)), sha256.Sum256([]byte(pass))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, ok := r.BasicAuth()
		if ok {
			gotUserSum, gotPassSum := sha256.Sum256([]byte(gotUser)), sha256.Sum256([]byte(gotPass))
			ok = subtle.ConstantTimeCompare(gotUserSum[:], wantUser[:]) == 1 &&
				subtle.ConstantTimeCompare(gotPassSum[:], wantPass[:]) == 1
		}
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="tg-harvester", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
