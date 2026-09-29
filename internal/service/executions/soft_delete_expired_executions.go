package executions

import (
	"context"

	"github.com/eduardolat/pgbackweb/internal/database/dbgen"
	"github.com/eduardolat/pgbackweb/internal/logger"
	"github.com/google/uuid"
)

func (s *Service) SoftDeleteExpiredExecutions() {
	ctx := context.Background()

	expiredExecutions, err := s.dbgen.ExecutionsServiceGetExpiredExecutions(ctx)
	if err != nil {
		logger.Error(
			"error soft deleting expired executions",
			logger.KV{"error": err},
		)
		return
	}

	deleted, failed, skipped := softDeleteEach(
		ctx, expiredExecutions, s.SoftDeleteExecution,
	)
	counts := logger.KV{"deleted": deleted, "failed": failed, "skipped": skipped}
	if failed > 0 {
		logger.Warn("some expired executions could not be soft deleted", counts)
		return
	}

	logger.Info("expired executions soft deleted", counts)
}

// softDeleteEach soft deletes the executions one by one. After a failure, the
// rest of the same backup waits for the next run: whatever broke the first
// deletion (a revoked key, a destination that is down) breaks the others too,
// and each attempt can take minutes. Other backups are still deleted.
func softDeleteEach(
	ctx context.Context, executions []dbgen.Execution,
	softDelete func(context.Context, uuid.UUID) error,
) (deleted, failed, skipped int) {
	failedBackups := map[uuid.UUID]bool{}

	for _, execution := range executions {
		if failedBackups[execution.BackupID] {
			skipped++
			continue
		}

		if err := softDelete(ctx, execution.ID); err != nil {
			logger.Error("error soft deleting expired execution", logger.KV{
				"execution_id": execution.ID.String(),
				"backup_id":    execution.BackupID.String(),
				"error":        err,
			})
			failedBackups[execution.BackupID] = true
			failed++
			continue
		}

		deleted++
	}

	return deleted, failed, skipped
}
