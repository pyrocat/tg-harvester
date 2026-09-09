# AGENTS.md

Guidance for AI agents and new contributors working on tg-harvester.

## What this is

A single-binary Go service that logs into Telegram with a **user account**
(MTProto via `github.com/gotd/td`, not a bot), polls a set of channels for new
posts, and forwards each post to an HTTP endpoint as JSON (text, nested
`author`, photos as base64). Configuration happens in an htmx web UI backed by
one SQLite file. See `README.md` for the user-facing contract.

## Commands

```sh
make run      # go run ./cmd/tg-harvester  → http://127.0.0.1:8080
make test     # go test -race ./...
make lint     # go vet ./... + gofmt check
make build    # static binary in bin/
make tidy     # go mod tidy
```

Run `make test` and `make lint` before considering a change done. The Go
toolchain on this machine lives in `~/.local/go` (symlinked into
`~/.local/bin`); `go.mod` requires Go 1.26 because gotd/td does.

## Architecture (clean architecture; dependencies point inward)

```
cmd/tg-harvester      composition root only: env → config, wiring, errgroup, shutdown
internal/domain       entities and rules; stdlib only, no I/O, no logging
internal/app          use cases + the ports (interfaces) they consume
internal/adapter/
  telegram            gotd client: login flow bridge, session storage, fetch/download
  sqlite              modernc.org/sqlite store + embedded migrations
  forward             HTTP forwarder (JSON payload, retries)
  web                 net/http handlers, html/template + htmx 4 UI
internal/config       environment → Config (used only by cmd)
```

Rules that keep this honest:

- `domain` imports nothing from the module. `app` imports only `domain`.
  Adapters import `domain` and the `app` package whose port they implement.
  Only `cmd` imports everything.
- **Ports are declared next to their consumer**, in `internal/app`, and stay
  small (1–4 methods). Do not create a central `ports` package.
- **No gotd type leaves `internal/adapter/telegram`.** `domain.PhotoRef` is the
  one Telegram-shaped struct in the domain; treat it as an opaque handle.
- Interfaces the web layer needs are declared in `internal/adapter/web/server.go`
  so handlers are testable with fakes.

## Conventions

- Wrap errors with context: `fmt.Errorf("fetch after %d: %w", cursor, err)`.
  Sentinels and typed errors live in `internal/domain/errors.go`
  (`ErrNotFound`, `ErrNotConnected`, `ErrUnauthorized`, `ErrNotConfigured`,
  `*FloodWaitError`, `*ValidationError`, `*PermanentError`).
- `*domain.ValidationError` is the only error shown verbatim in the UI; other
  errors get a generic prefix (see `userMessage` in `web/handlers.go`).
- Logging is `log/slog`, passed in explicitly; never a package-level logger.
  Do not bridge gotd's logger to slog — leave `telegram.Options.Logger` unset.
- Context is the first parameter everywhere; long-lived goroutines exit on
  `ctx.Done()`.
- Time is injected (`now func() time.Time`) where tests need determinism.
- Keep `go.mod` lean. Prefer the standard library; a new dependency needs a
  reason that outweighs the cost.

## Testing

- Table-driven tests, standard `testing` package only — no assertion libs.
- Use cases are tested with hand-written fakes for their ports
  (`internal/app/*_test.go`). Timing-dependent code (scheduler, session
  backoff) uses `testing/synctest` for deterministic virtual time.
- Adapters: `httptest` for the forwarder and web handlers, a temp-dir SQLite
  file for the store, and pure-function tests for the Telegram converters.
- Nothing talks to Telegram in tests. Live login and harvesting have to be
  verified manually with real API credentials.

## Behaviour that must not regress

- **At-least-once delivery.** The per-channel cursor advances only after the
  endpoint answers 2xx (`Harvester.PollChannel`). Never advance it earlier.
- **Whole-range fetching.** `collectNew` (`adapter/telegram/history.go`) walks
  history backwards from the newest message down to the cursor, so the
  harvester always holds the complete unseen range. Never replace it with a
  window anchored at the cursor: if Telegram returned the newest slice of a
  larger backlog, advancing the cursor past it would silently skip older
  posts. A backlog above `Harvester.MaxBacklog` is refused with
  `*domain.BacklogError` rather than partially delivered.
- **Album integrity.** Messages sharing a `grouped_id` are delivered as one
  post. `domain.GroupPosts` may assume every album it sees is complete,
  precisely because the whole unseen range is fetched; the settle delay keeps
  out an album that is still uploading.
- **One owner for flood waits.** The gotd middleware sleeps through waits up
  to `maxInlineFloodWait` only; longer ones surface as
  `*domain.FloodWaitError` so the scheduler can pause visibly. Do not make
  the middleware retry unboundedly — a long wait would block every other
  channel while the UI still showed "polling".
- **One slow channel must not starve the others.** `Harvester.pollBounded`
  gives each channel its own `ChannelTimeout`; only the caller's own
  cancellation aborts the whole cycle.
- **Login failures wait for the user; connection failures reconnect with
  backoff.** The distinction is `errors.Is(err, domain.ErrUnauthorized)`
  (`SessionManager.supervise`). Wrap new auth-type failures in
  `telegram.authError` so they take the first path.
- **Poison posts stop only their channel**, other channels keep going;
  "Skip to latest" in the UI is the escape hatch.
- The SQLite file is created with mode `0600` because it holds the MTProto
  authorization key.

## UI notes (htmx 4)

- htmx 4 is vendored at `internal/adapter/web/static/htmx.min.js`. Attribute
  inheritance is **explicit** in htmx 4, so every element sets its own
  `hx-target`/`hx-swap` (or use the `:inherited` suffix deliberately).
- Each card is a `<section id="…">` swapped with `outerHTML`; a partial route
  (`GET /partials/<card>`) and every mutating route render that same card.
- Validation errors are rendered inline with HTTP 200 so the swap always
  happens. Mutating routes are protected by `http.NewCrossOriginProtection`
  and optional basic auth (`TGH_BASIC_AUTH`).

## Known gaps

- Harvesting has been exercised only against a simulated history
  (`history_test.go`). A live channel with a real backlog, deleted-ID gaps and
  an album crossing a page boundary has not been run yet.
- Only photos are forwarded; videos and documents are ignored.
- Private channels require the account to already be a member; joining by
  invite link is not implemented.
