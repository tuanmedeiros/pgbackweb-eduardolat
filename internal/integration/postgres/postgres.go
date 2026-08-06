package postgres

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/eduardolat/pgbackweb/internal/util/strutil"
	"github.com/orsinium-labs/enum"
)

/*
	Important:
	Versions supported by PG Back Web must be supported in PostgreSQL Version Policy
	https://www.postgresql.org/support/versioning/

	Backing up a database from an old unsupported version should not be allowed.
*/

type version struct {
	Version string
	PGDump  string
	PSQL    string
}

type PGVersion enum.Member[version]

var (
	PG13 = PGVersion{version{
		Version: "13",
		PGDump:  "/usr/lib/postgresql/13/bin/pg_dump",
		PSQL:    "/usr/lib/postgresql/13/bin/psql",
	}}
	PG14 = PGVersion{version{
		Version: "14",
		PGDump:  "/usr/lib/postgresql/14/bin/pg_dump",
		PSQL:    "/usr/lib/postgresql/14/bin/psql",
	}}
	PG15 = PGVersion{version{
		Version: "15",
		PGDump:  "/usr/lib/postgresql/15/bin/pg_dump",
		PSQL:    "/usr/lib/postgresql/15/bin/psql",
	}}
	PG16 = PGVersion{version{
		Version: "16",
		PGDump:  "/usr/lib/postgresql/16/bin/pg_dump",
		PSQL:    "/usr/lib/postgresql/16/bin/psql",
	}}
	PG17 = PGVersion{version{
		Version: "17",
		PGDump:  "/usr/lib/postgresql/17/bin/pg_dump",
		PSQL:    "/usr/lib/postgresql/17/bin/psql",
	}}
	PG18 = PGVersion{version{
		Version: "18",
		PGDump:  "/usr/lib/postgresql/18/bin/pg_dump",
		PSQL:    "/usr/lib/postgresql/18/bin/psql",
	}}

	PGVersions     = []PGVersion{PG13, PG14, PG15, PG16, PG17, PG18}
	PGVersionsDesc = []PGVersion{PG18, PG17, PG16, PG15, PG14, PG13}
)

type Client struct {
	// testTimeout bounds the psql connectivity check. Without it, a host that
	// silently drops packets keeps a psql process alive for as long as the TCP
	// keepalives take to notice (hours), and every one of those processes holds
	// a connection open on the target database.
	//
	// Exceeding it marks the database unhealthy and fails the backup, so it
	// should only ever catch a truly stuck connection.
	testTimeout time.Duration
}

func New(testTimeout time.Duration) *Client {
	return &Client{testTimeout: testTimeout}
}

// cmdReader is an io.ReadCloser fed by an OS process through an io.Pipe. Its
// Close releases that process.
//
// This matters because io.Pipe writes block until someone reads: if the
// consumer walks away before EOF, the process stays blocked writing forever
// and never releases its connection to the database.
type cmdReader struct {
	reader  *io.PipeReader
	release func()
	once    sync.Once
}

func newCmdReader(reader *io.PipeReader, release func()) *cmdReader {
	return &cmdReader{reader: reader, release: release}
}

func (r *cmdReader) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

// Close unblocks the writing side and then releases the process behind it.
// It is safe to call more than once, and safe to call after reaching EOF.
func (r *cmdReader) Close() error {
	r.once.Do(func() {
		_ = r.reader.Close()
		r.release()
	})
	return nil
}

// ParseVersion returns the PGVersion enum member for the given PostgreSQL
// version as a string.
func (c *Client) ParseVersion(version string) (PGVersion, error) {
	switch version {
	case "13":
		return PG13, nil
	case "14":
		return PG14, nil
	case "15":
		return PG15, nil
	case "16":
		return PG16, nil
	case "17":
		return PG17, nil
	case "18":
		return PG18, nil
	default:
		return PGVersion{}, fmt.Errorf("pg version not allowed: %s", version)
	}
}

// Test tests the connection to the PostgreSQL database.
//
// The check is bounded by testTimeout so an unreachable host cannot leave a
// psql process (and the connection it holds) running indefinitely.
func (c *Client) Test(
	ctx context.Context, version PGVersion, connString string,
) error {
	ctx, cancel := context.WithTimeout(ctx, c.testTimeout)
	defer cancel()

	cmd := exec.CommandContext(
		ctx, version.Value.PSQL, addConnectionParams(connString), "-c", "SELECT 1;",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf(
				"timeout after %s running psql test v%s: %s",
				c.testTimeout, version.Value.Version, output,
			)
		}
		return fmt.Errorf(
			"error running psql test v%s: %s",
			version.Value.Version, output,
		)
	}

	return nil
}

// DumpParams contains the parameters for the pg_dump command
type DumpParams struct {
	// DataOnly (--data-only): Dump only the data, not the schema (data definitions).
	// Table data, large objects, and sequence values are dumped.
	DataOnly bool

	// SchemaOnly (--schema-only): Dump only the object definitions (schema), not data.
	SchemaOnly bool

	// Clean (--clean): Output commands to DROP all the dumped database objects
	// prior to outputting the commands for creating them. This option is useful
	// when the restore is to overwrite an existing database. If any of the
	// objects do not exist in the destination database, ignorable error messages
	// will be reported during restore, unless --if-exists is also specified.
	Clean bool

	// IfExists (--if-exists): Use DROP ... IF EXISTS commands to drop objects in
	// --clean mode. This suppresses “does not exist” errors that might otherwise
	// be reported. This option is not valid unless --clean is also specified.
	IfExists bool

	// Create (--create): Begin the output with a command to create the database
	// itself and reconnect to the created database. (With a script of this form,
	// it doesn't matter which database in the destination installation you
	// connect to before running the script.) If --clean is also specified, the
	// script drops and recreates the target database before reconnecting to it.
	Create bool

	// NoComments (--no-comments): Do not dump comments.
	NoComments bool
}

// Dump runs the pg_dump command with the given parameters. It returns the SQL
// dump as an io.ReadCloser.
//
// The caller MUST close the returned reader. pg_dump holds an open transaction
// on the source database for its whole run, so a reader that is abandoned
// before EOF (an upload that failed halfway, for example) would otherwise
// leave pg_dump blocked on a full pipe and its connection stuck in
// "idle in transaction" until the database is restarted.
func (c *Client) Dump(
	ctx context.Context, version PGVersion, connString string,
	params ...DumpParams,
) io.ReadCloser {
	pickedParams := DumpParams{}
	if len(params) > 0 {
		pickedParams = params[0]
	}

	args := []string{addConnectionParams(connString)}
	if pickedParams.DataOnly {
		args = append(args, "--data-only")
	}
	if pickedParams.SchemaOnly {
		args = append(args, "--schema-only")
	}
	if pickedParams.Clean {
		args = append(args, "--clean")
	}
	if pickedParams.IfExists {
		args = append(args, "--if-exists")
	}
	if pickedParams.Create {
		args = append(args, "--create")
	}
	if pickedParams.NoComments {
		args = append(args, "--no-comments")
	}

	return streamCmdStdout(
		ctx, version.Value.PGDump, args,
		fmt.Sprintf("error running pg_dump v%s", version.Value.Version),
	)
}

// streamCmdStdout runs a command and exposes its stdout as an io.ReadCloser.
//
// Closing the returned reader terminates the process. This is the whole point:
// stdout is delivered through an io.Pipe, whose writes block until someone
// reads, so a process whose output stops being consumed would otherwise hang
// forever holding whatever resources it opened.
func streamCmdStdout(
	ctx context.Context, path string, args []string, errPrefix string,
) io.ReadCloser {
	ctx, cancel := context.WithCancel(ctx)

	errorBuffer := &bytes.Buffer{}
	reader, writer := io.Pipe()

	// cmd.WaitDelay is deliberately left unset: it would also bound the time
	// Wait spends draining output after the process exits, which on a slow
	// upload would truncate the tail of a perfectly good dump.
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout = writer
	cmd.Stderr = errorBuffer

	go func() {
		// Reaching here means the process has exited, so this only releases
		// the context. Killing a still-running process is Close's job.
		defer cancel()

		if err := cmd.Run(); err != nil {
			writer.CloseWithError(fmt.Errorf(
				"%s: %s", errPrefix, errorBuffer.String(),
			))
			return
		}
		writer.Close()
	}()

	return newCmdReader(reader, cancel)
}

// DumpZip runs the pg_dump command with the given parameters and returns the
// ZIP-compressed SQL dump as an io.ReadCloser.
//
// As with Dump, the caller MUST close the returned reader to release the
// underlying pg_dump process and its connection to the source database.
func (c *Client) DumpZip(
	ctx context.Context, version PGVersion, connString string,
	params ...DumpParams,
) io.ReadCloser {
	dumpReader := c.Dump(ctx, version, connString, params...)
	reader, writer := io.Pipe()

	go func() {
		// Whatever happens below, pg_dump must be released. Without this, a zip
		// step that stops early leaves pg_dump blocked writing into a pipe
		// nobody reads.
		defer dumpReader.Close()

		zipWriter := zip.NewWriter(writer)

		fileWriter, err := zipWriter.Create("dump.sql")
		if err != nil {
			writer.CloseWithError(fmt.Errorf("error creating zip file: %w", err))
			return
		}

		if _, err := io.Copy(fileWriter, dumpReader); err != nil {
			writer.CloseWithError(fmt.Errorf("error writing to zip file: %w", err))
			return
		}

		// Closing the zip writer flushes the central directory; skipping this
		// error would produce a truncated archive reported as a success.
		if err := zipWriter.Close(); err != nil {
			writer.CloseWithError(fmt.Errorf("error closing zip file: %w", err))
			return
		}

		writer.Close()
	}()

	return newCmdReader(reader, func() { _ = dumpReader.Close() })
}

// RestoreZip downloads or copies the ZIP from the given url or path, unzips it,
// and runs the psql command to restore the database.
//
// The ZIP file must contain a dump.sql file with the SQL dump to restore.
//
//   - version: PostgreSQL version to use for the restore
//   - connString: connection string to the database
//   - isLocal: whether the ZIP file is local or a URL
//   - zipURLOrPath: URL or path to the ZIP file
func (c *Client) RestoreZip(
	ctx context.Context, version PGVersion, connString string,
	isLocal bool, zipURLOrPath string,
) error {
	workDir, err := os.MkdirTemp("", "pbw-restore-*")
	if err != nil {
		return fmt.Errorf("error creating temp dir: %w", err)
	}
	defer os.RemoveAll(workDir)
	zipPath := strutil.CreatePath(true, workDir, "dump.zip")
	dumpPath := strutil.CreatePath(true, workDir, "dump.sql")

	if isLocal {
		cmd := exec.CommandContext(ctx, "cp", zipURLOrPath, zipPath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("error copying ZIP file to temp dir: %s", output)
		}
	}

	if !isLocal {
		cmd := exec.CommandContext(ctx, "wget", "--no-verbose", "-O", zipPath, zipURLOrPath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("error downloading ZIP file: %s", output)
		}
	}

	if _, err := os.Stat(zipPath); os.IsNotExist(err) {
		return fmt.Errorf("zip file not found: %s", zipPath)
	}

	cmd := exec.CommandContext(ctx, "unzip", "-o", zipPath, "dump.sql", "-d", workDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("error unzipping ZIP file: %s", output)
	}

	if _, err := os.Stat(dumpPath); os.IsNotExist(err) {
		return fmt.Errorf("dump.sql file not found in ZIP file: %s", zipPath)
	}

	cmd = exec.CommandContext(
		ctx, version.Value.PSQL, addConnectionParams(connString), "-f", dumpPath,
	)
	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf(
			"error running psql v%s command: %s",
			version.Value.Version, output,
		)
	}

	return nil
}
