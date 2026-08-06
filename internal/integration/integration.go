package integration

import (
	"github.com/eduardolat/pgbackweb/internal/config"
	"github.com/eduardolat/pgbackweb/internal/integration/postgres"
	"github.com/eduardolat/pgbackweb/internal/integration/storage"
)

type Integration struct {
	PGClient      *postgres.Client
	StorageClient *storage.Client
}

// New wires the configured timeouts into the clients. The clients take plain
// durations rather than the environment itself, so they stay independent of how
// the values are configured and can be built directly in tests.
func New(env config.Env) *Integration {
	pgClient := postgres.New(env.PBW_DATABASE_TEST_TIMEOUT)
	storageClient := storage.New(
		env.PBW_DESTINATION_WRITE_TIMEOUT,
		env.PBW_DESTINATION_RESPONSE_TIMEOUT,
	)

	return &Integration{
		PGClient:      pgClient,
		StorageClient: storageClient,
	}
}
