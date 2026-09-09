CREATE TABLE settings (
    id                    INTEGER PRIMARY KEY CHECK (id = 1),
    api_id                INTEGER NOT NULL DEFAULT 0,
    api_hash              TEXT    NOT NULL DEFAULT '',
    phone                 TEXT    NOT NULL DEFAULT '',
    endpoint_url          TEXT    NOT NULL DEFAULT '',
    endpoint_header_name  TEXT    NOT NULL DEFAULT '',
    endpoint_header_value TEXT    NOT NULL DEFAULT '',
    poll_interval_sec     INTEGER NOT NULL DEFAULT 60,
    settle_delay_sec      INTEGER NOT NULL DEFAULT 10,
    updated_at            TEXT    NOT NULL
);

INSERT INTO settings (id, updated_at) VALUES (1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

CREATE TABLE telegram_session (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    data       BLOB NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE channels (
    id             INTEGER PRIMARY KEY,
    access_hash    INTEGER NOT NULL,
    username       TEXT    NOT NULL DEFAULT '',
    title          TEXT    NOT NULL DEFAULT '',
    cursor         INTEGER NOT NULL DEFAULT 0,
    enabled        INTEGER NOT NULL DEFAULT 1,
    last_polled_at TEXT,
    last_error     TEXT    NOT NULL DEFAULT '',
    added_at       TEXT    NOT NULL
);
