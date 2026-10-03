-- 0001_init.sql — schema baseline.
--
-- Conventions used throughout:
--   * Every tenant-owned table carries organization_id and a composite index
--     leading with it, because every query filters on it. A trailing
--     organization_id in a composite index is a decoration; a leading one is
--     the difference between an index seek and a sequential scan.
--   * Enum-like columns are TEXT with a CHECK constraint rather than a
--     Postgres ENUM type. Adding a value to a Postgres ENUM is a DDL migration
--     that cannot run inside some transactions; changing a CHECK is trivial.
--   * Timestamps are TIMESTAMPTZ, always. TIMESTAMP without a zone in a system
--     that computes deadlines across timezones is a bug waiting for the first
--     overseas customer.

CREATE TABLE organizations (
    id              UUID PRIMARY KEY,
    name            TEXT        NOT NULL,
    slug            TEXT        NOT NULL UNIQUE,
    timezone        TEXT        NOT NULL,
    ticket_prefix   TEXT        NOT NULL,
    -- ticket_seq is the per-tenant counter behind human-facing references.
    -- Incremented with UPDATE ... RETURNING, which takes a row lock and is
    -- therefore gap-free and race-free. A global sequence would leak total
    -- system volume to every customer via their own ticket numbers.
    ticket_seq      BIGINT      NOT NULL DEFAULT 0,
    active          BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL
);

CREATE TABLE teams (
    id              UUID PRIMARY KEY,
    organization_id UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name            TEXT        NOT NULL,
    description     TEXT        NOT NULL DEFAULT '',
    escalates_to    UUID        REFERENCES teams(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL,
    UNIQUE (organization_id, name)
);
CREATE INDEX teams_org_idx ON teams (organization_id);

CREATE TABLE users (
    id              UUID PRIMARY KEY,
    organization_id UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    -- Email is globally unique, not per-tenant: login happens before we know
    -- the tenant, so the address must resolve to exactly one account.
    email           TEXT        NOT NULL UNIQUE,
    password_hash   TEXT        NOT NULL,
    full_name       TEXT        NOT NULL,
    role            TEXT        NOT NULL CHECK (role IN ('requester','agent','manager','admin')),
    team_id         UUID        REFERENCES teams(id) ON DELETE SET NULL,
    active          BOOLEAN     NOT NULL DEFAULT TRUE,
    last_login_at   TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL
);
CREATE INDEX users_org_idx  ON users (organization_id);
CREATE INDEX users_team_idx ON users (organization_id, team_id) WHERE team_id IS NOT NULL;

CREATE TABLE refresh_tokens (
    token_hash      TEXT PRIMARY KEY,
    user_id         UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    organization_id UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    -- family_id ties a rotation chain together so a detected replay can revoke
    -- every descendant token at once.
    family_id       UUID        NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    consumed_at     TIMESTAMPTZ,
    revoked_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL
);
CREATE INDEX refresh_tokens_family_idx  ON refresh_tokens (family_id);
CREATE INDEX refresh_tokens_user_idx    ON refresh_tokens (user_id);
CREATE INDEX refresh_tokens_expiry_idx  ON refresh_tokens (expires_at);

CREATE TABLE sla_calendars (
    id              UUID PRIMARY KEY,
    organization_id UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name            TEXT        NOT NULL,
    timezone        TEXT        NOT NULL,
    -- Working windows and holidays are JSONB rather than child tables: they
    -- are read as a unit, written as a unit, never queried by their parts, and
    -- rarely change. A child table here would buy a join and nothing else.
    windows         JSONB       NOT NULL,
    holidays        JSONB       NOT NULL DEFAULT '[]'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX sla_calendars_org_idx ON sla_calendars (organization_id);

CREATE TABLE sla_policies (
    id                     UUID PRIMARY KEY,
    organization_id        UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name                   TEXT        NOT NULL,
    priority               TEXT        CHECK (priority IS NULL OR priority IN ('P1','P2','P3','P4')),
    kind                   TEXT        CHECK (kind IS NULL OR kind IN ('incident','service_request','change','problem')),
    team_id                UUID        REFERENCES teams(id) ON DELETE CASCADE,
    calendar_id            UUID        NOT NULL REFERENCES sla_calendars(id) ON DELETE RESTRICT,
    -- Budgets are stored in seconds: an integer that survives every driver and
    -- client without an interval-parsing dependency.
    first_response_seconds BIGINT      NOT NULL CHECK (first_response_seconds > 0),
    resolution_seconds     BIGINT      NOT NULL CHECK (resolution_seconds > 0),
    escalate_after_seconds BIGINT      CHECK (escalate_after_seconds IS NULL OR escalate_after_seconds > 0),
    active                 BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at             TIMESTAMPTZ NOT NULL,
    updated_at             TIMESTAMPTZ NOT NULL,
    CHECK (first_response_seconds <= resolution_seconds)
);
CREATE INDEX sla_policies_org_idx ON sla_policies (organization_id) WHERE active;

CREATE TABLE assets (
    id              UUID PRIMARY KEY,
    organization_id UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    tag             TEXT        NOT NULL,
    name            TEXT        NOT NULL,
    kind            TEXT        NOT NULL CHECK (kind IN ('laptop','desktop','mobile','server','network','printer','software','service','license','other')),
    status          TEXT        NOT NULL CHECK (status IN ('in_stock','assigned','maintenance','retired')),
    criticality     TEXT        NOT NULL CHECK (criticality IN ('low','medium','high')),
    manufacturer    TEXT        NOT NULL DEFAULT '',
    model           TEXT        NOT NULL DEFAULT '',
    serial_number   TEXT        NOT NULL DEFAULT '',
    location        TEXT        NOT NULL DEFAULT '',
    owner_id        UUID        REFERENCES users(id) ON DELETE SET NULL,
    parent_id       UUID        REFERENCES assets(id) ON DELETE SET NULL,
    purchased_at    TIMESTAMPTZ,
    warranty_until  TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL,
    UNIQUE (organization_id, tag)
);
CREATE INDEX assets_org_status_idx ON assets (organization_id, status);
CREATE INDEX assets_owner_idx      ON assets (organization_id, owner_id) WHERE owner_id IS NOT NULL;

CREATE TABLE tickets (
    id                  UUID PRIMARY KEY,
    organization_id     UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    reference           TEXT        NOT NULL,
    kind                TEXT        NOT NULL CHECK (kind IN ('incident','service_request','change','problem')),
    subject             TEXT        NOT NULL,
    description         TEXT        NOT NULL,
    category            TEXT        NOT NULL DEFAULT '',
    tags                TEXT[]      NOT NULL DEFAULT '{}',
    status              TEXT        NOT NULL CHECK (status IN ('new','triaged','pending_approval','in_progress','pending_requester','resolved','closed','cancelled')),
    impact              TEXT        NOT NULL CHECK (impact  IN ('low','medium','high')),
    urgency             TEXT        NOT NULL CHECK (urgency IN ('low','medium','high')),
    priority            TEXT        NOT NULL CHECK (priority IN ('P1','P2','P3','P4')),

    requester_id        UUID        NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    assignee_id         UUID        REFERENCES users(id) ON DELETE SET NULL,
    team_id             UUID        REFERENCES teams(id) ON DELETE SET NULL,

    resolution          TEXT,
    resolved_at         TIMESTAMPTZ,
    closed_at           TIMESTAMPTZ,
    first_response_at   TIMESTAMPTZ,
    reopen_count        INTEGER     NOT NULL DEFAULT 0,

    sla_policy_id       UUID        REFERENCES sla_policies(id) ON DELETE SET NULL,
    first_response_due  TIMESTAMPTZ,
    resolution_due      TIMESTAMPTZ,
    paused_since        TIMESTAMPTZ,
    first_response_met  BOOLEAN,
    resolution_met      BOOLEAN,
    breach_notified_at  TIMESTAMPTZ,

    created_at          TIMESTAMPTZ NOT NULL,
    updated_at          TIMESTAMPTZ NOT NULL,

    -- A stored generated column rather than a trigger-maintained one: Postgres
    -- keeps it correct by construction, so it cannot drift when a migration or
    -- a manual fix bypasses the trigger. Weights let the ranking prefer a
    -- subject match over a body match.
    search_vector       TSVECTOR GENERATED ALWAYS AS (
        setweight(to_tsvector('english', coalesce(reference, '')), 'A') ||
        setweight(to_tsvector('english', coalesce(subject, '')),   'A') ||
        setweight(to_tsvector('english', coalesce(category, '')),  'B') ||
        setweight(to_tsvector('english', coalesce(description,'')),'C')
    ) STORED,

    UNIQUE (organization_id, reference)
);

-- The hot path: "tickets for this org, filtered by status, newest first".
-- id DESC rather than created_at DESC because ids are UUIDv7 (time-ordered),
-- so one index serves both ordering and keyset pagination.
CREATE INDEX tickets_org_status_idx   ON tickets (organization_id, status, id DESC);
CREATE INDEX tickets_org_team_idx     ON tickets (organization_id, team_id, status, id DESC);
CREATE INDEX tickets_org_assignee_idx ON tickets (organization_id, assignee_id, status, id DESC) WHERE assignee_id IS NOT NULL;
CREATE INDEX tickets_org_requester_idx ON tickets (organization_id, requester_id, id DESC);
CREATE INDEX tickets_search_idx       ON tickets USING GIN (search_vector);
CREATE INDEX tickets_tags_idx         ON tickets USING GIN (tags);

-- Partial index for the breach worker. The predicate matches the worker's
-- query exactly, so the index holds only the few thousand rows that could ever
-- breach rather than every ticket ever filed.
CREATE INDEX tickets_breach_sweep_idx ON tickets (resolution_due)
    WHERE status NOT IN ('resolved','closed','cancelled','pending_requester','pending_approval')
      AND resolution_due IS NOT NULL
      AND breach_notified_at IS NULL;

CREATE TABLE ticket_messages (
    id              UUID PRIMARY KEY,
    organization_id UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    ticket_id       UUID        NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    author_id       UUID        REFERENCES users(id) ON DELETE SET NULL,
    body            TEXT        NOT NULL,
    visibility      TEXT        NOT NULL CHECK (visibility IN ('public','internal')),
    system          BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL
);
-- visibility is in the index because the requester-facing read always filters
-- on it; without it, every thread load scans internal notes it will discard.
CREATE INDEX ticket_messages_thread_idx ON ticket_messages (organization_id, ticket_id, visibility, created_at);

CREATE TABLE attachments (
    id              UUID PRIMARY KEY,
    organization_id UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    message_id      UUID        NOT NULL REFERENCES ticket_messages(id) ON DELETE CASCADE,
    filename        TEXT        NOT NULL,
    content_type    TEXT        NOT NULL,
    size_bytes      BIGINT      NOT NULL CHECK (size_bytes > 0),
    -- The object-storage key, never a public URL. Downloads are mediated by a
    -- short-lived signed URL issued only after the permission check.
    storage_key     TEXT        NOT NULL,
    uploaded_by     UUID        NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at      TIMESTAMPTZ NOT NULL
);
CREATE INDEX attachments_message_idx ON attachments (organization_id, message_id);

CREATE TABLE ticket_assets (
    organization_id UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    ticket_id       UUID        NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    asset_id        UUID        NOT NULL REFERENCES assets(id)  ON DELETE CASCADE,
    linked_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (ticket_id, asset_id)
);
CREATE INDEX ticket_assets_asset_idx ON ticket_assets (organization_id, asset_id);

CREATE TABLE approvals (
    id              UUID PRIMARY KEY,
    organization_id UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    ticket_id       UUID        NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    approver_id     UUID        NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    requested_by    UUID        NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    decision        TEXT        NOT NULL CHECK (decision IN ('pending','approved','rejected')),
    comment         TEXT        NOT NULL DEFAULT '',
    decided_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL,
    -- One approval slot per approver per ticket. Without this, asking the same
    -- person twice would let a single yes satisfy a two-approver requirement.
    UNIQUE (ticket_id, approver_id),
    -- Self-approval is blocked in the domain; enforced here too, because a
    -- control that exists only in application code is one bug away from gone.
    CHECK (approver_id <> requested_by)
);
CREATE INDEX approvals_ticket_idx  ON approvals (organization_id, ticket_id);
CREATE INDEX approvals_pending_idx ON approvals (organization_id, approver_id) WHERE decision = 'pending';

CREATE TABLE audit_log (
    id              UUID PRIMARY KEY,
    organization_id UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    ticket_id       UUID        NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    actor_id        UUID        REFERENCES users(id) ON DELETE SET NULL,
    actor_label     TEXT        NOT NULL,
    action          TEXT        NOT NULL,
    from_value      TEXT,
    to_value        TEXT,
    metadata        JSONB,
    created_at      TIMESTAMPTZ NOT NULL
);
CREATE INDEX audit_log_ticket_idx ON audit_log (organization_id, ticket_id, created_at DESC);

CREATE TABLE saved_views (
    id              UUID PRIMARY KEY,
    organization_id UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    owner_id        UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name            TEXT        NOT NULL,
    shared          BOOLEAN     NOT NULL DEFAULT FALSE,
    -- The stored filter never contains a visibility scope; scope is always
    -- recomputed from the viewing actor. See app/view_service.go.
    filter          JSONB       NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL,
    UNIQUE (organization_id, owner_id, name)
);
CREATE INDEX saved_views_owner_idx ON saved_views (organization_id, owner_id);
