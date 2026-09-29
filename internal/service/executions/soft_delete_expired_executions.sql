-- name: ExecutionsServiceGetExpiredExecutions :many
-- The newest min_copies successful executions of each backup are never
-- returned, however old they are. They are ranked rather than counted because
-- the caller fetches this list once and deletes it in a loop: a count taken
-- here would be stale after the first deletion, while a rank only ever moves
-- further from the protected set as newer backups succeed.
-- Only backups with retention are ranked: the others can never have anything
-- deleted, and their history grows without bound.
WITH successful_executions AS (
  SELECT
    executions.id,
    row_number() OVER (
      PARTITION BY executions.backup_id
      ORDER BY executions.finished_at DESC, executions.id DESC
    ) AS recency_rank
  FROM executions
  JOIN backups ON executions.backup_id = backups.id
  WHERE
    backups.retention_days > 0
    AND executions.status = 'success'
    AND executions.finished_at IS NOT NULL
)
SELECT executions.*
FROM executions
JOIN backups ON executions.backup_id = backups.id
LEFT JOIN successful_executions
  ON successful_executions.id = executions.id
WHERE
  backups.retention_days > 0
  AND executions.status != 'deleted'
  AND executions.finished_at IS NOT NULL
  AND (
    executions.finished_at + (backups.retention_days || ' days')::INTERVAL
  ) < NOW()
  AND (
    executions.status != 'success'
    OR successful_executions.recency_rank > backups.min_copies
  );
