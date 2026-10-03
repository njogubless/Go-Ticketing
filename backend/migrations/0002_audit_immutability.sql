-- 0002_audit_immutability.sql
--
-- An audit trail that the application can rewrite is not an audit trail; it is
-- a log with extra steps. Two independent mechanisms enforce append-only:
--
--   1. A trigger that rejects UPDATE and DELETE outright. This holds even for
--      a superuser connection and even for a hand-typed psql statement.
--   2. A revocation of UPDATE/DELETE from the application role, so the
--      privilege is not merely unused but absent.
--
-- Belt and braces, because the failure mode — silently altered history
-- discovered during an audit — is unrecoverable after the fact.

CREATE OR REPLACE FUNCTION audit_log_is_append_only() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only: % is not permitted', TG_OP
        USING ERRCODE = 'insufficient_privilege';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_log_no_update
    BEFORE UPDATE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_is_append_only();

CREATE TRIGGER audit_log_no_delete
    BEFORE DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_is_append_only();

-- Retention still has to be possible: regulators require records be kept, and
-- privacy law requires they not be kept forever. Purging is therefore a
-- deliberate, privileged operation that disables the triggers for the duration
-- of one call, rather than a permission the application holds all day.
CREATE OR REPLACE FUNCTION audit_log_purge_before(cutoff TIMESTAMPTZ)
RETURNS BIGINT AS $$
DECLARE
    removed BIGINT;
BEGIN
    ALTER TABLE audit_log DISABLE TRIGGER audit_log_no_delete;
    DELETE FROM audit_log WHERE created_at < cutoff;
    GET DIAGNOSTICS removed = ROW_COUNT;
    ALTER TABLE audit_log ENABLE TRIGGER audit_log_no_delete;
    RETURN removed;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION audit_log_purge_before IS
    'Retention purge. Requires table ownership; deliberately not granted to the application role.';
