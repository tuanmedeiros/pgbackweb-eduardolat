-- Upstream v0.5.2 ships this as 20260828054515. This fork renames it to run
-- after its own 20260925000001: goose refuses an unapplied migration older than
-- the newest applied one, so under the upstream name a database already on the
-- fork would not start. IF NOT EXISTS lets a database that came from upstream
-- v0.5.2, where the index already exists, apply it too. See UPSTREAM.md.

-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
  IF (SELECT COUNT(*) FROM users) > 1 THEN
    RAISE EXCEPTION E'CRITICAL SECURITY ALERT: PG Back Web found more than one user in the database.\n\nThis instance was probably compromised through the create-first-user endpoint vulnerability, fixed in v0.5.2. PG Back Web refuses to start until the database is cleaned.\n\nTo fix this:\n\n1. List all users to identify the legitimate one:\n\n   SELECT id, name, email, created_at FROM users ORDER BY created_at;\n\n2. Delete every user except the legitimate one (replace the email below with yours):\n\n   DELETE FROM users WHERE email NOT IN (''your-email@example.com'');\n\n3. Rotate ALL credentials stored in PG Back Web: PostgreSQL connection strings and S3 destination keys.\n\n4. Start PG Back Web again.\n\nDeleting a user automatically deletes their sessions too.';
  END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS users_single_user_invariant_idx ON users ((true));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- On a database that came from upstream v0.5.2, 20260828054515 created the index
-- and is still recorded as applied, so the index is not this migration's to drop.
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM goose_db_version
    WHERE version_id = 20260828054515 AND is_applied
  ) THEN
    DROP INDEX IF EXISTS users_single_user_invariant_idx;
  END IF;
END $$;
-- +goose StatementEnd
