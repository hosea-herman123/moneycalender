-- Core account, business, rule and reminder tables. Client and invoice tables
-- are added by 002_invoices.sql.

CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE sessions (
    token      TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TEXT NOT NULL            -- RFC3339, UTC
);

-- One business per user for the MVP.
CREATE TABLE businesses (
    id             INTEGER PRIMARY KEY,
    user_id        INTEGER NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    business_type  TEXT NOT NULL,
    county         TEXT NOT NULL,
    vat_registered INTEGER NOT NULL DEFAULT 0,
    has_employees  INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Mirror of internal/rules/data/obligations.yaml (the YAML is the source of truth).
CREATE TABLE obligation_rules (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    agency        TEXT NOT NULL,
    frequency     TEXT NOT NULL,
    due_rule      TEXT NOT NULL,
    penalty       TEXT NOT NULL,
    note          TEXT NOT NULL,
    link          TEXT NOT NULL,
    last_verified TEXT NOT NULL
);

-- The scheduler's durable send queue.
-- "to" is a reserved word in SQL, so the column is to_addr.
-- send_at is RFC3339 in UTC so string comparison orders correctly.
-- dedupe_key makes scheduling idempotent (INSERT OR IGNORE).
CREATE TABLE reminders (
    id          INTEGER PRIMARY KEY,
    business_id INTEGER REFERENCES businesses(id) ON DELETE CASCADE,
    send_at     TEXT NOT NULL,
    channel     TEXT NOT NULL DEFAULT 'email',
    to_addr     TEXT NOT NULL,
    subject     TEXT NOT NULL,
    body        TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending',   -- pending | sent | failed
    dedupe_key  TEXT UNIQUE,
    error       TEXT,
    sent_at     TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX reminders_due ON reminders(status, send_at);
