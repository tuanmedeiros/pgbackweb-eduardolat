package config

import (
	"sync"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

type Env struct {
	PBW_ENCRYPTION_KEY       string `env:"PBW_ENCRYPTION_KEY,required"`
	PBW_POSTGRES_CONN_STRING string `env:"PBW_POSTGRES_CONN_STRING,required"`
	PBW_LISTEN_HOST          string `env:"PBW_LISTEN_HOST" envDefault:"0.0.0.0"`
	PBW_LISTEN_PORT          string `env:"PBW_LISTEN_PORT" envDefault:"8085"`
	PBW_PATH_PREFIX          string `env:"PBW_PATH_PREFIX" envDefault:""`

	// The timeouts below stop a stuck operation from holding resources
	// forever. Their defaults suit ordinary setups; a very large database or a
	// slow link may need more room.
	//
	// Each one bounds a *lack of progress*, never the total duration of a
	// backup, so raising them is only necessary when something legitimately
	// pauses for longer than the default allows.

	// PBW_DATABASE_TEST_TIMEOUT bounds the connectivity check made against each
	// database. Exceeding it marks that database unhealthy.
	PBW_DATABASE_TEST_TIMEOUT time.Duration `env:"PBW_DATABASE_TEST_TIMEOUT" envDefault:"60s"`

	// PBW_BACKUP_STALL_TIMEOUT is how long a destination may go without
	// consuming any of the dump before the backup is abandoned. It has to
	// tolerate a destination pausing while its in-flight parts finish.
	PBW_BACKUP_STALL_TIMEOUT time.Duration `env:"PBW_BACKUP_STALL_TIMEOUT" envDefault:"30m"`

	// PBW_DESTINATION_WRITE_TIMEOUT is how long a single write to a destination
	// may block. It is refreshed on every write, so an upload that keeps moving
	// is never cut off, however long it takes overall.
	PBW_DESTINATION_WRITE_TIMEOUT time.Duration `env:"PBW_DESTINATION_WRITE_TIMEOUT" envDefault:"2m"`

	// PBW_DESTINATION_RESPONSE_TIMEOUT is how long a destination may take to
	// start answering once a request has been sent. Completing a large
	// multipart upload legitimately takes a while, so this is generous.
	PBW_DESTINATION_RESPONSE_TIMEOUT time.Duration `env:"PBW_DESTINATION_RESPONSE_TIMEOUT" envDefault:"5m"`
}

var (
	getEnvRes  Env
	getEnvErr  error
	getEnvOnce sync.Once
)

// GetEnv returns the environment variables.
//
// If there is an error, it will log it and exit the program.
func GetEnv(disableLogs ...bool) (Env, error) {
	getEnvOnce.Do(func() {
		_ = godotenv.Load()

		parsedEnv, err := env.ParseAs[Env]()
		if err != nil {
			getEnvErr = err
			return
		}

		if err := validateEnv(parsedEnv); err != nil {
			getEnvErr = err
			return
		}

		getEnvRes = parsedEnv
	})

	return getEnvRes, getEnvErr
}
