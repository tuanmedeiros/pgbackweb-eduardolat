package auth

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/eduardolat/pgbackweb/internal/database/dbgen"
	"github.com/eduardolat/pgbackweb/internal/service"
	"github.com/eduardolat/pgbackweb/internal/service/users"
	"github.com/labstack/echo/v4"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// TestCreateFirstUserHandler runs the handler against a real PostgreSQL,
// because the guard it checks is a count of the users table. It needs a
// database migrated with `task goose -- up`, named in
// PBW_TEST_POSTGRES_CONN_STRING, and is skipped without one. Each case runs in
// a transaction that is rolled back, so the database is left as it was.
func TestCreateFirstUserHandler(t *testing.T) {
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
		existingUsers int
		wantStatus    int
		wantLocation  string
		wantEmails    []string
	}{
		{
			// The v0.5.1 hole: anyone could add an account to a running
			// instance and read its connection strings and S3 keys.
			name:          "refuses and redirects to login when a user exists",
			existingUsers: 1,
			wantStatus:    http.StatusFound,
			wantLocation:  "/auth/login",
			wantEmails:    []string{"owner@example.com"},
		},
		{
			name:          "creates the user when there is none",
			existingUsers: 0,
			wantStatus:    http.StatusOK,
			wantEmails:    []string{"new@example.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := db.Begin()
			require.NoError(t, err)
			t.Cleanup(func() { _ = tx.Rollback() })

			_, err = tx.Exec(`DELETE FROM users`)
			require.NoError(t, err)
			if tt.existingUsers > 0 {
				_, err = tx.Exec(`
					INSERT INTO users (name, email, password)
					VALUES ('Owner', 'owner@example.com', 'x')
				`)
				require.NoError(t, err)
			}

			h := handlers{servs: &service.Service{
				UsersService: users.New(dbgen.New(tx)),
			}}

			form := url.Values{
				"name":                  {"New"},
				"email":                 {"new@example.com"},
				"password":              {"secret123"},
				"password_confirmation": {"secret123"},
			}
			req := httptest.NewRequest(
				http.MethodPost, "/auth/create-first-user",
				strings.NewReader(form.Encode()),
			)
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
			rec := httptest.NewRecorder()
			c := echo.New().NewContext(req, rec)

			require.NoError(t, h.createFirstUserHandler(c))
			require.Equal(t, tt.wantStatus, rec.Code)
			require.Equal(t, tt.wantLocation, rec.Header().Get(echo.HeaderLocation))

			var gotEmails []string
			rows, err := tx.Query(`SELECT email FROM users ORDER BY email`)
			require.NoError(t, err)
			defer rows.Close()
			for rows.Next() {
				var email string
				require.NoError(t, rows.Scan(&email))
				gotEmails = append(gotEmails, email)
			}
			require.NoError(t, rows.Err())
			require.Equal(t, tt.wantEmails, gotEmails)
		})
	}
}
