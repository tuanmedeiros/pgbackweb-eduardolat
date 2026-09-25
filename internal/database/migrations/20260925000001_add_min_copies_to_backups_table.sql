-- +goose Up
-- +goose StatementBegin
ALTER TABLE backups
  ADD COLUMN min_copies SMALLINT NOT NULL DEFAULT 3,
  ADD CONSTRAINT backups_min_copies_check CHECK (min_copies >= 0);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE backups
  DROP CONSTRAINT IF EXISTS backups_min_copies_check,
  DROP COLUMN IF EXISTS min_copies;
-- +goose StatementEnd
