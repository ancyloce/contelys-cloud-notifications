-- Notifications' ledger (Notifications' detailed design, section 04): each notification with its
-- frozen inputs, one notification delivery for each recipient, each send attempt with what the mail
-- server answered, and the audit of the operator commands. The inbox, the subscription's position
-- and River's tables belong to the shared service library, which migrates them itself. Later
-- migrations expand before they contract, because old and new instances run side by side during a
-- rolling upgrade (ADR 0032).

-- +goose Up
CREATE TABLE notification (
    id               uuid        PRIMARY KEY,
    -- producer is the event's ce_source, and event_id its ce_id, which a replay keeps.
    producer         text        NOT NULL,
    event_id         text        NOT NULL,
    event_type       text        NOT NULL,
    template_key     text        NOT NULL,
    template_version integer     NOT NULL,
    template_locale  text        NOT NULL,
    template_digest  text        NOT NULL,
    -- variables are deleted once the retention of personal data has passed.
    variables        jsonb,
    accepted_at      timestamptz NOT NULL,
    UNIQUE (producer, event_id)
);

CREATE TABLE notification_delivery (
    id              uuid        PRIMARY KEY,
    notification_id uuid        NOT NULL REFERENCES notification ON DELETE CASCADE,
    recipient_kind  text        NOT NULL CHECK (recipient_kind IN ('PRINCIPAL', 'INVITATION')),
    recipient_id    text        NOT NULL,
    -- address is deleted by an erasure, or once the retention of personal data has passed.
    address         text,
    channel         text        NOT NULL CHECK (channel IN ('EMAIL')),
    state           text        NOT NULL CHECK (state IN ('READY', 'SENDING', 'ACCEPTED', 'REFUSED', 'UNCERTAIN', 'SUPPRESSED', 'EXPIRED')),
    revision        bigint      NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL,
    expires_at      timestamptz NOT NULL,
    UNIQUE (notification_id, recipient_kind, recipient_id)
);

CREATE TABLE send_attempt (
    notification_delivery_id uuid        NOT NULL REFERENCES notification_delivery ON DELETE CASCADE,
    attempt                  integer     NOT NULL,
    started_at               timestamptz NOT NULL,
    ended_at                 timestamptz,
    smtp_phase               text,
    reply_code               integer,
    reply_text               text,
    outcome                  text CHECK (outcome IN ('ACCEPTED', 'REFUSED_TEMPORARILY', 'REFUSED_PERMANENTLY', 'UNCERTAIN')),
    message_id               text,
    PRIMARY KEY (notification_delivery_id, attempt)
);

-- At most one attempt without an outcome for each notification delivery.
CREATE UNIQUE INDEX send_attempt_one_open ON send_attempt (notification_delivery_id) WHERE outcome IS NULL;

CREATE TABLE audit (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    operator    text        NOT NULL,
    command     text        NOT NULL,
    arguments   jsonb       NOT NULL,
    reason      text        NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE audit;
DROP TABLE send_attempt;
DROP TABLE notification_delivery;
DROP TABLE notification;
