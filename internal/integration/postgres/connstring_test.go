package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAddConnectionParams(t *testing.T) {
	all := []string{
		"connect_timeout=10", "keepalives=1", "keepalives_idle=30",
		"keepalives_interval=10", "keepalives_count=5",
	}

	tests := []struct {
		name        string
		connString  string
		wantContain []string
		wantExact   string
		wantPrefix  string
	}{
		{
			name:        "uri without query gets every parameter",
			connString:  "postgres://user:pass@localhost:5432/mydb",
			wantContain: all,
			wantPrefix:  "postgres://user:pass@localhost:5432/mydb?",
		},
		{
			name:        "postgresql scheme is also recognised",
			connString:  "postgresql://user@host/db",
			wantContain: all,
		},
		{
			name:        "existing query is preserved and extended",
			connString:  "postgres://user@host/db?sslmode=require",
			wantContain: append([]string{"sslmode=require"}, all...),
			wantPrefix:  "postgres://user@host/db?sslmode=require&",
		},
		{
			name:       "user values are never overridden",
			connString: "postgres://user@host/db?connect_timeout=60&keepalives_idle=5",
			wantContain: []string{
				"connect_timeout=60", "keepalives_idle=5",
				"keepalives=1", "keepalives_interval=10", "keepalives_count=5",
			},
		},
		{
			name: "nothing to add leaves the string untouched",
			connString: "postgres://h/db?connect_timeout=1&keepalives=0" +
				"&keepalives_idle=1&keepalives_interval=1&keepalives_count=1",
			wantExact: "postgres://h/db?connect_timeout=1&keepalives=0" +
				"&keepalives_idle=1&keepalives_interval=1&keepalives_count=1",
		},
		{
			name:        "keyword value dsn gets space separated parameters",
			connString:  "host=localhost port=5432 dbname=mydb user=me",
			wantContain: append([]string{"host=localhost", "dbname=mydb"}, all...),
			wantPrefix:  "host=localhost port=5432 dbname=mydb user=me ",
		},
		{
			name:       "dsn user values are never overridden",
			connString: "host=localhost connect_timeout=99",
			wantContain: []string{
				"host=localhost", "connect_timeout=99", "keepalives=1",
			},
		},
		{
			// Appending here would produce input libpq rejects.
			name:       "bare database name is left alone",
			connString: "mydb",
			wantExact:  "mydb",
		},
		{
			name:       "empty string is left alone",
			connString: "",
			wantExact:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := addConnectionParams(tt.connString)

			if tt.wantExact != "" || tt.connString == "" {
				require.Equal(t, tt.wantExact, got)
			}
			for _, want := range tt.wantContain {
				require.Contains(t, got, want)
			}
			if tt.wantPrefix != "" {
				require.True(
					t, strings.HasPrefix(got, tt.wantPrefix),
					"got %q, want prefix %q", got, tt.wantPrefix,
				)
			}
		})
	}
}

// TestAddConnectionParamsRespectsSpacedDSNAssignments covers libpq's optional
// whitespace around the equals sign. Missing it would append a second
// connect_timeout, and libpq keeps the last value of a repeated keyword, so a
// user's explicit 60s would silently become 10s.
func TestAddConnectionParamsRespectsSpacedDSNAssignments(t *testing.T) {
	tests := []struct {
		name       string
		connString string
		absent     string
		present    string
	}{
		{
			name:       "space before and after equals",
			connString: "host=db connect_timeout = 60",
			absent:     "connect_timeout=10",
			present:    "keepalives=1",
		},
		{
			name:       "space only before equals",
			connString: "host=db keepalives_idle =300",
			absent:     "keepalives_idle=30",
			present:    "keepalives=1",
		},
		{
			name:       "space only after equals",
			connString: "host=db keepalives= 0",
			absent:     "keepalives=1",
			present:    "connect_timeout=10",
		},
		{
			name:       "quoted value containing spaces",
			connString: "host=db password='a b c' connect_timeout = 90",
			absent:     "connect_timeout=10",
			present:    "keepalives=1",
		},
		{
			name:       "escaped quote inside quoted value",
			connString: `host=db password='a\'b' connect_timeout = 90`,
			absent:     "connect_timeout=10",
			present:    "keepalives=1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := addConnectionParams(tt.connString)

			require.NotContains(t, got, tt.absent, "must not override a user value")
			require.Contains(t, got, tt.present)
			require.True(t, strings.HasPrefix(got, tt.connString))
		})
	}
}

// TestAddConnectionParamsLeavesMalformedDSNAlone checks that a DSN libpq would
// reject is handed over untouched, so the real error surfaces.
func TestAddConnectionParamsLeavesMalformedDSNAlone(t *testing.T) {
	for _, conn := range []string{
		"host=db dbname",           // keyword without a value
		"host=db password='未closed", // unterminated quote
		"= nokeyword",              // value without a keyword
	} {
		require.Equal(t, conn, addConnectionParams(conn), "conn=%q", conn)
	}
}

// TestAddConnectionParamsDefersToServiceFile covers connections that delegate
// their settings to pg_service.conf.
//
// libpq applies a service file value only when the parameter is not already set
// ("don't override any previous explicit setting", fe-connect.c), so anything
// injected inline silently beats centrally managed configuration. Since the
// service file may set any of these parameters, none may be injected.
func TestAddConnectionParamsDefersToServiceFile(t *testing.T) {
	for _, conn := range []string{
		"service=production",
		"service=production dbname=mydb",
		"dbname=mydb service=production",
		"postgres://host/db?service=production",
		"postgres://host/db?sslmode=require&service=production",
	} {
		require.Equal(t, conn, addConnectionParams(conn), "conn=%q", conn)
	}
}

// TestAddConnectionParamsDefersToPGSERVICE covers the same delegation reached
// through the environment: libpq falls back to $PGSERVICE when the connection
// string names no service.
func TestAddConnectionParamsDefersToPGSERVICE(t *testing.T) {
	t.Setenv("PGSERVICE", "production")

	for _, conn := range []string{
		"host=db dbname=mydb",
		"postgres://host/db",
	} {
		require.Equal(t, conn, addConnectionParams(conn), "conn=%q", conn)
	}
}

// TestAddConnectionParamsDefersToPGCONNECTTIMEOUT covers libpq's environment
// fallback, which is also outranked by anything set inline.
//
// Only connect_timeout has such a fallback; the keepalive parameters are
// declared with a NULL envvar, so they are still injected.
func TestAddConnectionParamsDefersToPGCONNECTTIMEOUT(t *testing.T) {
	t.Setenv("PGCONNECT_TIMEOUT", "45")

	got := addConnectionParams("postgres://host/db")

	require.NotContains(t, got, "connect_timeout")
	require.Contains(t, got, "keepalives=1")
	require.Contains(t, got, "keepalives_idle=30")
}

// TestAddConnectionParamsInjectsWhenEnvIsAbsent is the control for the two
// tests above: without those variables the defaults are still applied.
func TestAddConnectionParamsInjectsWhenEnvIsAbsent(t *testing.T) {
	t.Setenv("PGSERVICE", "")
	t.Setenv("PGCONNECT_TIMEOUT", "")
	require.NoError(t, os.Unsetenv("PGSERVICE"))
	require.NoError(t, os.Unsetenv("PGCONNECT_TIMEOUT"))

	got := addConnectionParams("postgres://host/db")

	require.Contains(t, got, "connect_timeout=10")
	require.Contains(t, got, "keepalives=1")
}

// TestAddConnectionParamsDoesNotCorruptCredentials guards the reason this
// builds the string textually instead of round-tripping through net/url:
// re-encoding could silently change a password that already works.
func TestAddConnectionParamsDoesNotCorruptCredentials(t *testing.T) {
	conn := "postgres://user:p%40ss%2Fw0rd@host:5432/db"

	got := addConnectionParams(conn)

	require.True(t, strings.HasPrefix(got, conn+"?"), "got %q", got)
	require.Contains(t, got, "keepalives=1")
}

// TestAddConnectionParamsKeepsFragment checks a URI fragment stays at the end,
// where libpq expects to be able to ignore it.
func TestAddConnectionParamsKeepsFragment(t *testing.T) {
	require.True(
		t, strings.HasSuffix(addConnectionParams("postgres://h/db#frag"), "#frag"),
	)
	require.True(
		t, strings.HasSuffix(addConnectionParams("postgres://h/db?a=b#frag"), "#frag"),
	)
}

// TestAddConnectionParamsIsIdempotent matters because Test and Dump both run
// against the same stored connection string.
func TestAddConnectionParamsIsIdempotent(t *testing.T) {
	for _, conn := range []string{
		"postgres://user@host/db",
		"postgres://user@host/db?sslmode=require",
		"host=localhost dbname=mydb",
		"mydb",
		"",
	} {
		once := addConnectionParams(conn)
		require.Equal(t, once, addConnectionParams(once), "conn=%q", conn)
	}
}
