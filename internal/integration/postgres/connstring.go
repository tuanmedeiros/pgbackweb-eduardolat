package postgres

import (
	"net/url"
	"os"
	"strings"
)

// connParams are injected into every connection string handed to psql and
// pg_dump, unless the user already configured them.
//
// They bound the other half of the leak: a client waiting on a server that has
// gone away. Killing the client process covers a consumer that stopped reading,
// but a pg_dump blocked on a dead network link is still owed an answer that
// never comes. Without keepalives the socket can sit there for hours.
//
// Worst case detection: keepalives_idle + (keepalives_interval * keepalives_count)
// = 30 + (10 * 5) = 80 seconds.
//
// env is libpq's environment fallback for the parameter, where one exists. Only
// connect_timeout has one; the keepalive parameters are declared with a NULL
// envvar in fe-connect.c and can only come from a connection string or a
// service file.
var connParams = []struct{ key, value, env string }{
	{"connect_timeout", "10", "PGCONNECT_TIMEOUT"},
	{"keepalives", "1", ""},
	{"keepalives_idle", "30", ""},
	{"keepalives_interval", "10", ""},
	{"keepalives_count", "5", ""},
}

// paramsToAdd returns the parameters still worth injecting, given the keywords
// already present in the connection string.
//
// libpq resolves a parameter from the first source that provides it: the
// connection string, then pg_service.conf, then the environment. Everything
// injected here lands in the *first* of those, so it silently outranks
// configuration the user put somewhere else. Each case below is a source that
// must be allowed to win.
func paramsToAdd(existing map[string]bool) []struct{ key, value, env string } {
	// A service name delegates configuration to pg_service.conf, and libpq
	// applies a service file value only when the parameter is not already set
	// ("don't override any previous explicit setting", fe-connect.c). Since the
	// file may set any of these, injecting any of them would silently win, so
	// the whole connection is left to the service.
	if existing["service"] || isEnvSet("PGSERVICE") {
		return nil
	}

	out := []struct{ key, value, env string }{}
	for _, p := range connParams {
		if existing[p.key] || isEnvSet(p.env) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func isEnvSet(name string) bool {
	if name == "" {
		return false
	}
	_, ok := os.LookupEnv(name)
	return ok
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
//
// The trimmed copy is used only to recognise the format. What gets appended to
// is always the original: in a DSN a backslash can escape a trailing space, as
// in "password=secret\ ", and trimming would drop the space while keeping the
// backslash, leaving it to escape the separator added after it and swallow the
// appended parameters into the password.
func addConnectionParams(connString string) string {
	trimmed := strings.TrimSpace(connString)

	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "postgres://") ||
		strings.HasPrefix(lower, "postgresql://") {
		return addURIParams(connString)
	}

	// A keyword/value DSN is only recognisable by containing a "=". A bare
	// dbname must be left alone: "mydb keepalives=1" is not valid libpq input.
	if strings.Contains(trimmed, "=") {
		return addDSNParams(connString)
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

	existing, ok := uriQueryKeys(query)
	if !ok {
		return connString
	}

	additions := []string{}
	for _, p := range paramsToAdd(existing) {
		additions = append(additions, p.key+"="+p.value)
	}
	if len(additions) == 0 {
		return connString
	}

	if query != "" {
		return base + "?" + query + "&" + strings.Join(additions, "&") + fragment
	}
	return base + "?" + strings.Join(additions, "&") + fragment
}

// uriQueryKeys returns the parameter names set in a URI query, reporting false
// if the query is not something libpq would accept.
//
// libpq percent-decodes the parameter *name* as well as its value — "At this
// point both keyword and value are not URI-encoded" (fe-connect.c) — so
// "?connect%5ftimeout=60" sets connect_timeout. Comparing the raw text would
// miss that and append a second connect_timeout, which wins, silently replacing
// the value the user chose.
func uriQueryKeys(query string) (map[string]bool, bool) {
	keys := map[string]bool{}
	if query == "" {
		return keys, true
	}

	for _, pair := range strings.Split(query, "&") {
		if pair == "" {
			continue
		}

		raw, _, found := strings.Cut(pair, "=")
		if !found {
			return nil, false // libpq: "missing key/value separator"
		}

		// PathUnescape rather than QueryUnescape: libpq decodes %XX only, and
		// leaves "+" as a literal plus rather than a space.
		key, err := url.PathUnescape(raw)
		if err != nil {
			return nil, false // libpq: "invalid percent-encoded token"
		}

		keys[strings.ToLower(strings.TrimSpace(key))] = true
	}

	return keys, true
}

// addDSNParams appends missing parameters to a keyword/value DSN.
//
// A malformed DSN is returned untouched: libpq will report the real problem,
// and appending to something already broken only obscures it.
func addDSNParams(connString string) string {
	existing, ok := dsnKeys(connString)
	if !ok {
		return connString
	}

	out := connString
	for _, p := range paramsToAdd(existing) {
		out += " " + p.key + "=" + p.value
	}
	return out
}

// dsnKeys returns the keywords set in a libpq keyword/value DSN, reporting
// false if the string does not parse.
//
// This follows libpq's own grammar rather than splitting on whitespace, because
// libpq allows spaces around the equals sign and quoted values. Reading
// "host=db connect_timeout = 60" as three whitespace-separated tokens would
// hide the connect_timeout the user set, and appending a second one would
// silently win: libpq keeps the last value of a repeated keyword.
func dsnKeys(connString string) (map[string]bool, bool) {
	keys := map[string]bool{}
	runes := []rune(connString)
	i := 0

	skipSpaces := func() {
		for i < len(runes) && isSpace(runes[i]) {
			i++
		}
	}

	for {
		skipSpaces()
		if i >= len(runes) {
			return keys, true
		}

		// Keyword: everything up to a space or the equals sign.
		start := i
		for i < len(runes) && !isSpace(runes[i]) && runes[i] != '=' {
			i++
		}
		if i == start {
			return nil, false // a stray "=" with no keyword
		}
		keys[strings.ToLower(string(runes[start:i]))] = true

		skipSpaces()
		if i >= len(runes) || runes[i] != '=' {
			return nil, false // keyword without a value
		}
		i++ // consume "="
		skipSpaces()

		if !skipDSNValue(runes, &i) {
			return nil, false
		}
	}
}

// skipDSNValue advances past a DSN value, which is either single quoted or
// runs until the next space. A backslash escapes the next character in both
// forms.
func skipDSNValue(runes []rune, i *int) bool {
	if *i < len(runes) && runes[*i] == '\'' {
		*i++
		for *i < len(runes) {
			switch runes[*i] {
			case '\\':
				*i += 2
			case '\'':
				*i++
				return true
			default:
				*i++
			}
		}
		return false // unterminated quote
	}

	for *i < len(runes) && !isSpace(runes[*i]) {
		if runes[*i] == '\\' {
			*i++
		}
		*i++
	}
	return true
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f'
}
