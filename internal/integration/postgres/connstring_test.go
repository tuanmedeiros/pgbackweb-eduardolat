package postgres

import (
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
