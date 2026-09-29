package executions

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	"github.com/eduardolat/pgbackweb/internal/database/dbgen"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// These tests run ExecutionsServiceGetExpiredExecutions against a real
// PostgreSQL, because what they check is the SQL itself. They need a database
// migrated with `task goose -- up`, named in PBW_TEST_POSTGRES_CONN_STRING,
// and are skipped without one. Each case runs in a transaction that is rolled
// back, so the database is left as it was.

type expiryFixture struct {
	label    string
	status   string
	ageDays  int
	finished bool
}

func TestGetExpiredExecutions(t *testing.T) {
	connString := os.Getenv("PBW_TEST_POSTGRES_CONN_STRING")
	if connString == "" {
		t.Skip("PBW_TEST_POSTGRES_CONN_STRING not set")
	}

	db, err := sql.Open("postgres", connString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Ping())

	tests := []struct {
		name          string
		retentionDays int16
		minCopies     int16
		executions    []expiryFixture
		wantExpired   []string
	}{
		{
			// The scenario from the issue: backups stop succeeding and every
			// remaining copy is past retention.
			name:          "keeps the newest copies when all are past retention",
			retentionDays: 2,
			minCopies:     3,
			executions: []expiryFixture{
				{"failed-1d", "failed", 1, true},
				{"failed-2d", "failed", 2, true},
				{"success-3d", "success", 3, true},
				{"success-4d", "success", 4, true},
				{"success-5d", "success", 5, true},
				{"success-6d", "success", 6, true},
				{"success-7d", "success", 7, true},
			},
			wantExpired: []string{"success-6d", "success-7d"},
		},
		{
			name:          "backups within retention count toward the minimum",
			retentionDays: 7,
			minCopies:     2,
			executions: []expiryFixture{
				{"success-1d", "success", 1, true},
				{"success-2d", "success", 2, true},
				{"success-3d", "success", 3, true},
				{"success-10d", "success", 10, true},
				{"success-11d", "success", 11, true},
			},
			wantExpired: []string{"success-10d", "success-11d"},
		},
		{
			name:          "failed executions are still removed by age",
			retentionDays: 2,
			minCopies:     3,
			executions: []expiryFixture{
				{"failed-3d", "failed", 3, true},
				{"success-5d", "success", 5, true},
				{"failed-10d", "failed", 10, true},
			},
			wantExpired: []string{"failed-3d", "failed-10d"},
		},
		{
			name:          "a minimum of zero deletes by age alone",
			retentionDays: 2,
			minCopies:     0,
			executions: []expiryFixture{
				{"success-1d", "success", 1, true},
				{"success-3d", "success", 3, true},
				{"success-5d", "success", 5, true},
			},
			wantExpired: []string{"success-3d", "success-5d"},
		},
		{
			name:          "a minimum above the number of copies keeps them all",
			retentionDays: 1,
			minCopies:     10,
			executions: []expiryFixture{
				{"success-5d", "success", 5, true},
				{"success-6d", "success", 6, true},
			},
			wantExpired: nil,
		},
		{
			name:          "zero retention days never deletes",
			retentionDays: 0,
			minCopies:     0,
			executions: []expiryFixture{
				{"success-100d", "success", 100, true},
				{"failed-100d", "failed", 100, true},
			},
			wantExpired: nil,
		},
		{
			// A deleted copy has no file left, and an unfinished one has no
			// file yet, so neither may stand in for a real copy.
			name:          "deleted and unfinished executions do not count as copies",
			retentionDays: 1,
			minCopies:     1,
			executions: []expiryFixture{
				{"deleted-2d", "deleted", 2, true},
				{"running-3d", "running", 3, false},
				{"success-5d", "success", 5, true},
				{"success-6d", "success", 6, true},
			},
			wantExpired: []string{"success-6d"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			tx, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = tx.Rollback() })

			backupID := insertExpiryBackup(t, tx, tt.retentionDays, tt.minCopies)
			labels := map[uuid.UUID]string{}
			for _, fixture := range tt.executions {
				labels[insertExpiryExecution(t, tx, backupID, fixture)] = fixture.label
			}

			queries := dbgen.New(tx)
			expired := expiredIDs(t, queries, labels)
			var expiredLabels []string
			for _, id := range expired {
				expiredLabels = append(expiredLabels, labels[id])
			}
			require.ElementsMatch(t, tt.wantExpired, expiredLabels)

			// The caller fetches the list once and deletes it in a loop.
			// Deleting all of it must not expose further copies: the next run
			// has to find nothing more to delete.
			for _, id := range expired {
				require.NoError(t, queries.ExecutionsServiceSoftDeleteExecution(ctx, id))
			}
			require.Empty(t, expiredIDs(t, queries, labels))
		})
	}
}

func insertExpiryBackup(
	t *testing.T, tx *sql.Tx, retentionDays int16, minCopies int16,
) uuid.UUID {
	t.Helper()

	var databaseID uuid.UUID
	err := tx.QueryRow(
		`INSERT INTO databases (name, connection_string, pg_version)
		VALUES ($1, 'unused'::BYTEA, '16')
		RETURNING id`,
		"expiry-test-"+uuid.NewString(),
	).Scan(&databaseID)
	require.NoError(t, err)

	var backupID uuid.UUID
	err = tx.QueryRow(
		`INSERT INTO backups (
			database_id, is_local, name, cron_expression, time_zone, dest_dir,
			retention_days, min_copies
		)
		VALUES ($1, TRUE, 'expiry-test', '0 0 * * *', 'UTC', '/expiry-test', $2, $3)
		RETURNING id`,
		databaseID, retentionDays, minCopies,
	).Scan(&backupID)
	require.NoError(t, err)

	return backupID
}

func insertExpiryExecution(
	t *testing.T, tx *sql.Tx, backupID uuid.UUID, fixture expiryFixture,
) uuid.UUID {
	t.Helper()

	age := fmt.Sprintf("%d days", fixture.ageDays)
	var id uuid.UUID
	err := tx.QueryRow(
		`INSERT INTO executions (backup_id, status, path, started_at, finished_at)
		VALUES (
			$1, $2, $3, NOW() - $4::INTERVAL,
			CASE WHEN $5::BOOLEAN THEN NOW() - $4::INTERVAL END
		)
		RETURNING id`,
		backupID, fixture.status, "/expiry-test/"+fixture.label+".zip", age,
		fixture.finished,
	).Scan(&id)
	require.NoError(t, err)

	return id
}

// expiredIDs runs the query and keeps only the executions this case created,
// so the test also works against a database holding other data.
func expiredIDs(
	t *testing.T, queries *dbgen.Queries, labels map[uuid.UUID]string,
) []uuid.UUID {
	t.Helper()

	expired, err := queries.ExecutionsServiceGetExpiredExecutions(
		context.Background(),
	)
	require.NoError(t, err)

	var ids []uuid.UUID
	for _, execution := range expired {
		if _, ok := labels[execution.ID]; ok {
			ids = append(ids, execution.ID)
		}
	}

	return ids
}
