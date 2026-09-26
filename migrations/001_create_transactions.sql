-- Amounts are stored in minor units (centavos) to keep money arithmetic exact.
CREATE TABLE IF NOT EXISTS transactions (
    id                      UUID         PRIMARY KEY,
    account_id              VARCHAR(64)  NOT NULL,
    type                    TEXT         NOT NULL CHECK (type IN ('CREDIT', 'DEBIT')),
    amount_cents            BIGINT       NOT NULL CHECK (amount_cents > 0),
    currency                CHAR(3)      NOT NULL,
    description             VARCHAR(255) NOT NULL DEFAULT '',
    status                  TEXT         NOT NULL CHECK (status IN ('PENDING', 'EXECUTED', 'REJECTED', 'FAILED')),
    provider_transaction_id TEXT,
    balance_after_cents     BIGINT,
    failure_code            TEXT,
    failure_message         TEXT,
    created_at              TIMESTAMPTZ  NOT NULL,
    updated_at              TIMESTAMPTZ  NOT NULL
);

-- Each index matches a GET /transactions filter plus its newest-first ordering,
-- so a page is read straight off the index without sorting.
CREATE INDEX IF NOT EXISTS transactions_account_created_idx
    ON transactions (account_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS transactions_status_created_idx
    ON transactions (status, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS transactions_created_idx
    ON transactions (created_at DESC, id DESC);
