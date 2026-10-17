ALTER TABLE businesses ADD COLUMN cash_balance INTEGER NOT NULL DEFAULT 0;
ALTER TABLE businesses ADD COLUMN cash_configured INTEGER NOT NULL DEFAULT 0;

CREATE TABLE obligation_estimates (
    business_id      INTEGER NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
    rule_id          TEXT NOT NULL,
    estimated_amount INTEGER NOT NULL DEFAULT 0 CHECK (estimated_amount >= 0),
    PRIMARY KEY (business_id, rule_id)
);
