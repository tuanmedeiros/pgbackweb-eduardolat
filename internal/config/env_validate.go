package config

import (
	"fmt"
	"time"

	"github.com/eduardolat/pgbackweb/internal/validate"
)

// validateEnv runs additional validations on the environment variables.
func validateEnv(env Env) error {
	if !validate.ListenHost(env.PBW_LISTEN_HOST) {
		return fmt.Errorf("invalid listen address %s", env.PBW_LISTEN_HOST)
	}

	if !validate.Port(env.PBW_LISTEN_PORT) {
		return fmt.Errorf("invalid listen port %s, valid values are 1-65535", env.PBW_LISTEN_PORT)
	}

	if !validate.PathPrefix(env.PBW_PATH_PREFIX) {
		return fmt.Errorf("invalid path prefix %s, must start with / and not end with / (or be empty)", env.PBW_PATH_PREFIX)
	}

	// A non-positive timeout would not mean "no limit": it would fire
	// immediately and break every backup, so it is rejected at startup rather
	// than at the first run.
	timeouts := map[string]time.Duration{
		"PBW_DATABASE_TEST_TIMEOUT":        env.PBW_DATABASE_TEST_TIMEOUT,
		"PBW_BACKUP_STALL_TIMEOUT":         env.PBW_BACKUP_STALL_TIMEOUT,
		"PBW_DESTINATION_WRITE_TIMEOUT":    env.PBW_DESTINATION_WRITE_TIMEOUT,
		"PBW_DESTINATION_RESPONSE_TIMEOUT": env.PBW_DESTINATION_RESPONSE_TIMEOUT,
	}
	for name, value := range timeouts {
		if value <= 0 {
			return fmt.Errorf("invalid %s %s, must be greater than zero", name, value)
		}
	}

	return nil
}
