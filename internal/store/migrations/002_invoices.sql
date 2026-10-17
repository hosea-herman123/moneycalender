-- Clients and invoices are scoped to the owning business.
CREATE TABLE clients (
    id          INTEGER PRIMARY KEY,
    business_id INTEGER NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
    name        TEXT NOT NULL COLLATE NOCASE,
    email       TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (business_id, name)
);

CREATE TABLE invoices (
    id          INTEGER PRIMARY KEY,
    business_id INTEGER NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
    client_id   INTEGER NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    amount      INTEGER NOT NULL CHECK (amount > 0),
    issue_date  TEXT NOT NULL,
    due_date    TEXT NOT NULL,
    paid_date   TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX invoices_business_due ON invoices(business_id, due_date, paid_date);

CREATE TABLE client_reminder_log (
    id         INTEGER PRIMARY KEY,
    invoice_id INTEGER NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    sent_on    TEXT NOT NULL,
    UNIQUE (invoice_id, sent_on)
);
