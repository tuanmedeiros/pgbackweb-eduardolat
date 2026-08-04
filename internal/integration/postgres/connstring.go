package postgres

import (
	"strings"
)

// connParams are injected into every connection string handed to psql and
// pg_dump, unless the user already set them.
//
// They bound the other half of the leak: a client waiting on a server that has
// gone away. Killing the client process covers a consumer that stopped reading,
// but a pg_dump blocked on a dead network link is still owed an answer that
// never comes. Without keepalives the socket can sit there for hours.
//
// Worst case detection: keepalives_idle + (keepalives_interval * keepalives_count)
// = 30 + (10 * 5) = 80 seconds.
var connParams = []struct{ key, value string }{
	{"connect_timeout", "10"},
	{"keepalives", "1"},
	{"keepalives_idle", "30"},
	{"keepalives_interval", "10"},
	{"keepalives_count", "5"},
}

// addConnectionParams injects the keepalive and timeout parameters into a
// libpq connection string, leaving any value the user already chose untouched.
//
// libpq accepts two formats, and they need opposite treatment:
//
//   - URIs (postgres://user:pass@host/db), where parameters are query args
//   - keyword/value DSNs (host=x dbname=y), where they are space separated
//
// Anything else — a bare database name, an empty string — is returned
// unchanged, because appending to it would produce something libpq rejects.
func addConnectionParams(connString string) string {
	trimmed := strings.TrimSpace(connString)

	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "postgres://") ||
		strings.HasPrefix(lower, "postgresql://") {
		return addURIParams(trimmed)
	}

	// A keyword/value DSN is only recognisable by containing a "=". A bare
	// dbname must be left alone: "mydb keepalives=1" is not valid libpq input.
	if strings.Contains(trimmed, "=") {
		return addDSNParams(trimmed)
	}

	return connString
}

// addURIParams appends missing parameters to the query string of a URI.
//
// It edits the query textually instead of round-tripping through net/url, so
// that credentials and host are handed to libpq byte for byte as the user
// wrote them.
func addURIParams(connString string) string {
	base, query, hasQuery := strings.Cut(connString, "?")

	// A URI may carry a #fragment; libpq ignores it, but keep it at the end.
	fragment := ""
	if hasQuery {
		if q, f, ok := strings.Cut(query, "#"); ok {
			query, fragment = q, "#"+f
		}
	} else if b, f, ok := strings.Cut(base, "#"); ok {
		base, fragment = b, "#"+f
	}

	existing := map[string]bool{}
	for _, pair := range strings.Split(query, "&") {
		if key, _, ok := strings.Cut(pair, "="); ok {
			existing[strings.ToLower(strings.TrimSpace(key))] = true
		}
	}

	additions := []string{}
	for _, p := range connParams {
		if !existing[p.key] {
			additions = append(additions, p.key+"="+p.value)
		}
	}
	if len(additions) == 0 {
		return connString
	}

	if query != "" {
		return base + "?" + query + "&" + strings.Join(additions, "&") + fragment
	}
	return base + "?" + strings.Join(additions, "&") + fragment
}

// addDSNParams appends missing parameters to a keyword/value DSN.
func addDSNParams(connString string) string {
	existing := map[string]bool{}
	for _, field := range strings.Fields(connString) {
		if key, _, ok := strings.Cut(field, "="); ok {
			existing[strings.ToLower(key)] = true
		}
	}

	out := connString
	for _, p := range connParams {
		if !existing[p.key] {
			out += " " + p.key + "=" + p.value
		}
	}
	return out
}
