# tg-harvester

A single-binary web service that watches Telegram channels with a **user
account** (MTProto, not a bot), and forwards every new post to an HTTP
endpoint as JSON with the text, the author, and the photos as bytes.

Everything is configured from a small htmx web UI: the Telegram credentials
and interactive login, the list of channels, the destination endpoint and the
poll interval.

## Quick start

```sh
# 1. Get an API ID and hash for your account at https://my.telegram.org/apps
# 2. Run the service
make run                       # or: go run ./cmd/tg-harvester
# 3. Open http://127.0.0.1:8080 and:
#      - enter the API ID, API hash and phone number, then the code Telegram sends
#      - set the endpoint URL
#      - add one or more public channels (e.g. @durov)
```

New posts are forwarded from the moment a channel is added; existing history
is never backfilled.

## Configuration

Runtime settings (credentials, channels, endpoint, interval) live in the
database and are edited in the UI. The process itself reads only environment
variables:

| Variable | Default | Meaning |
| --- | --- | --- |
| `TGH_LISTEN_ADDR` | `127.0.0.1:8080` | Listen address. Use `:8080` to expose it beyond localhost. |
| `TGH_DB_PATH` | `data/tg-harvester.db` | SQLite file. Created with mode 0600. |
| `TGH_BASIC_AUTH` | *(empty)* | `user:password` for the UI. Unset means no authentication. |
| `TGH_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `TGH_LOG_FORMAT` | `text` | `text` or `json`. |
| `TGH_HTTP_TIMEOUT` | `60s` | Timeout for one delivery to the endpoint. |

The database holds the Telegram authorization key and the endpoint's auth
header in plaintext: keep the file private, and set `TGH_BASIC_AUTH` (plus a
TLS-terminating proxy) whenever the UI is reachable from a network.

## Payload

One `POST` per post, `Content-Type: application/json`. A Telegram album
(several photos posted together) is delivered as a **single** payload.

```json
{
  "channel":     {"id": 1234567890, "title": "Example", "username": "example"},
  "message_id":  4567,
  "message_ids": [4567, 4568],
  "grouped_id":  "13802394",
  "link":        "https://t.me/example/4567",
  "date":        "2026-09-08T19:00:00Z",
  "text":        "post text or album caption",
  "author":      {"name": "Jane Doe", "username": "janedoe"},
  "photos": [
    {"filename": "example_4567_1.jpg", "mime": "image/jpeg", "width": 1280, "height": 720, "data": "<base64>"}
  ]
}
```

- `author` is the posting user when the channel exposes one, otherwise the
  signature of a signed post, otherwise the channel itself.
- `photos[].data` is base64 (standard `encoding/json` encoding of `[]byte`).
  Only photos are forwarded; videos and documents are ignored, though the
  text of such a message is still delivered.
- Every request carries `X-Idempotency-Key: <channel_id>:<max_message_id>`,
  plus the auth header configured in the UI.

**Delivery is at-least-once.** The cursor advances only after the endpoint
answers `2xx`, so a crash between delivery and the cursor write re-sends the
post. Deduplicate on `channel.id` + `message_id`.

A `2xx` means accepted. `408`, `425`, `429` and `5xx` are retried three times
with backoff; any other `4xx` is permanent, stops that channel, and is shown
in the UI — use **Skip to latest** to move past a post the endpoint will
never accept.

The auth header you configure cannot be named `Content-Type`,
`X-Idempotency-Key` or another header the forwarder owns; those names are
rejected when you save the settings.

## Behaviour notes

- **Albums** are merged by `grouped_id`. A "settle delay" (10s by default)
  keeps a post that is still being uploaded from being split across polls.
- **Backlogs** are delivered in full, oldest first: each poll reads the whole
  range between the cursor and the newest message. If a channel accumulates
  more than 5000 unseen messages the service refuses to deliver a partial
  slice — it reports the backlog and waits for you to press **Skip to
  latest**, so nothing is ever skipped silently.
- Each channel gets at most 5 minutes per cycle, so one slow or huge channel
  cannot starve the others; it simply continues on the next poll.
- **Rate limits**: Telegram `FLOOD_WAIT` is honoured automatically, and the
  scheduler pauses for the requested period. Keep the interval at 30s or more.
- **Private channels** work only if the account is already a member; joining
  by invite link is not implemented.
- Only public channels can be added, by `@name`, `name`, or a `t.me` link.

## Development

```sh
make test     # go test -race ./...
make lint     # go vet + gofmt check
make build    # static binary in bin/
```

Layout follows clean architecture; dependencies point inward:

```
cmd/tg-harvester      composition root: config, wiring, graceful shutdown
internal/domain       entities and rules (no I/O, stdlib only)
internal/app          use cases and the ports they consume
internal/adapter/…    telegram (gotd), sqlite, forward (HTTP), web (htmx UI)
internal/config       environment → Config
```

`internal/app` never imports an adapter, and every gotd type stays inside
`internal/adapter/telegram`.

## Docker

```sh
docker build -t tg-harvester .
docker run -p 8080:8080 -v tg-harvester-data:/data \
  -e TGH_LISTEN_ADDR=:8080 -e TGH_DB_PATH=/data/tg-harvester.db \
  -e TGH_BASIC_AUTH=admin:choose-a-password tg-harvester
```
