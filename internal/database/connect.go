package database

import (
	"database/sql"
	"time"

	"github.com/eduardolat/pgbackweb/internal/config"
	"github.com/eduardolat/pgbackweb/internal/logger"
	_ "github.com/lib/pq"
)

func Connect(env config.Env) *sql.DB {
	db, err := sql.Open("postgres", env.PBW_POSTGRES_CONN_STRING)
	if err != nil {
		logger.FatalError(
			"could not connect to DB",
			logger.KV{
				"error": err,
			},
		)
	}

	err = db.Ping()
	if err != nil {
		logger.FatalError(
			"could not ping DB",
			logger.KV{
				"error": err,
			},
		)
	}

	db.SetMaxOpenConns(10)
	// Match idle to open so bursts don't churn connections, but bound how long
	// an idle one is kept: a connection parked for hours is the one a pooler,
	// firewall or cloud provider silently drops, and the app only finds out on
	// the next query.
	db.SetMaxIdleConns(10)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(1 * time.Hour)
	logger.Info("connected to DB")

	return db
}
