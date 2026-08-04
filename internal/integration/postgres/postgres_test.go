//go:build unix

package postgres

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func skipWithoutShellTools(t *testing.T) {
	t.Helper()

	for _, bin := range []string{"sh", "yes"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}
}

// writeScript writes an executable shell script and returns its path.
func writeScript(t *testing.T, name, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755))
	return path
}

// processAlive reports whether the given pid still exists. Signal 0 performs
// the permission and existence checks without delivering anything.
func processAlive(pid int) bool {
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

// waitForExit polls until the pid is gone or the timeout expires.
func waitForExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// endlessScript builds a script that records its own pid and then streams to
// stdout forever, standing in for a pg_dump on a large database. `exec` keeps
// the pid stable so the caller knows exactly which process to watch.
func endlessScript(t *testing.T) (scriptPath, pidFile string) {
	t.Helper()

	pidFile = filepath.Join(t.TempDir(), "pid")
	scriptPath = writeScript(t, "fake-pg-dump", `echo $$ > "$1"
exec yes pgbackweb-stream-test
`)
	return scriptPath, pidFile
}

// stalledScript builds a script that emits a burst and then blocks forever,
// standing in for a pg_dump waiting on an unresponsive server. Such a process
// never writes again, so closing the pipe alone cannot reach it with a SIGPIPE
// — only killing it works.
func stalledScript(t *testing.T) (scriptPath, pidFile string) {
	t.Helper()

	pidFile = filepath.Join(t.TempDir(), "pid")
	scriptPath = writeScript(t, "stalled-pg-dump", `echo $$ > "$1"
head -c 100000 /dev/zero
exec sleep 600
`)
	return scriptPath, pidFile
}

// readPID waits for the stand-in process to report its pid.
func readPID(t *testing.T, pidFile string) int {
	t.Helper()

	raw, err := os.ReadFile(pidFile)
	require.NoError(t, err)

	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	return pid
}

// consumeSome reads a chunk, which proves the process started and wrote its
// pid file before the test inspects it.
func consumeSome(t *testing.T, r io.Reader) {
	t.Helper()

	buf := make([]byte, 64)
	_, err := io.ReadFull(r, buf)
	require.NoError(t, err, "the stand-in process should produce output")
}

// TestStreamCmdStdoutCloseKillsProcess covers the leak that left pg_dump
// running — and its connection to the source database stuck "idle in
// transaction" — whenever a backup stopped consuming the dump early, for
// example because the upload to the destination failed halfway through.
func TestStreamCmdStdoutCloseKillsProcess(t *testing.T) {
	skipWithoutShellTools(t)

	script, pidFile := endlessScript(t)
	reader := streamCmdStdout(context.Background(), script, []string{pidFile}, "test cmd")
	consumeSome(t, reader)

	pid := readPID(t, pidFile)
	require.True(t, processAlive(pid), "process should be running before close")

	require.NoError(t, reader.Close())

	require.True(
		t, waitForExit(pid, 10*time.Second),
		"closing the reader must terminate the process, otherwise it holds a "+
			"connection open on the source database indefinitely",
	)
}

// TestStreamCmdStdoutCancelKillsProcess verifies the same guarantee through
// context cancellation.
func TestStreamCmdStdoutCancelKillsProcess(t *testing.T) {
	skipWithoutShellTools(t)

	ctx, cancel := context.WithCancel(context.Background())
	script, pidFile := endlessScript(t)
	reader := streamCmdStdout(ctx, script, []string{pidFile}, "test cmd")
	defer reader.Close()
	consumeSome(t, reader)

	pid := readPID(t, pidFile)
	require.True(t, processAlive(pid), "process should be running before cancel")

	cancel()

	require.True(
		t, waitForExit(pid, 10*time.Second),
		"cancelling the context must terminate the process",
	)
}

// TestStreamCmdStdoutCloseKillsStalledProcess is the case that closing the pipe
// cannot fix on its own: a pg_dump blocked waiting on an unresponsive database
// gets no SIGPIPE, because it is not writing. Only killing the process frees
// the connection it is holding.
func TestStreamCmdStdoutCloseKillsStalledProcess(t *testing.T) {
	skipWithoutShellTools(t)

	script, pidFile := stalledScript(t)
	reader := streamCmdStdout(context.Background(), script, []string{pidFile}, "test cmd")
	consumeSome(t, reader)

	pid := readPID(t, pidFile)
	require.True(t, processAlive(pid), "process should be running before close")

	require.NoError(t, reader.Close())

	require.True(
		t, waitForExit(pid, 10*time.Second),
		"Close must terminate a process that has stopped writing, otherwise a "+
			"backup against an unresponsive server leaks a connection",
	)
}

// TestStreamCmdStdoutCloseIsIdempotent makes sure the deferred Close added to
// RunExecution is harmless on the happy path, where the dump was read in full
// and the process already exited on its own.
func TestStreamCmdStdoutCloseIsIdempotent(t *testing.T) {
	skipWithoutShellTools(t)

	script := writeScript(t, "fake-pg-dump", "echo done\n")
	reader := streamCmdStdout(context.Background(), script, nil, "test cmd")

	out, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, "done\n", string(out))

	require.NoError(t, reader.Close())
	require.NoError(t, reader.Close())
}

// TestStreamCmdStdoutReportsStderr checks that a failing command still surfaces
// its stderr through the reader, which is how backup failures reach the UI.
func TestStreamCmdStdoutReportsStderr(t *testing.T) {
	skipWithoutShellTools(t)

	script := writeScript(t, "fake-pg-dump", "echo boom >&2\nexit 1\n")
	reader := streamCmdStdout(
		context.Background(), script, nil, "error running fake pg_dump",
	)
	defer reader.Close()

	_, err := io.ReadAll(reader)
	require.Error(t, err)
	require.Contains(t, err.Error(), "error running fake pg_dump")
	require.Contains(t, err.Error(), "boom")
}

// TestDumpZipCloseKillsPgDump checks the fix through the entry point
// RunExecution actually uses: abandoning the zip stream must take the
// underlying pg_dump down with it, not just the zip goroutine.
func TestDumpZipCloseKillsPgDump(t *testing.T) {
	skipWithoutShellTools(t)

	script, pidFile := endlessScript(t)
	ver := PGVersion{Value: version{Version: "test", PGDump: script}}

	// DumpZip passes the connection string as the first argument, which the
	// stand-in script reads as its pid-file path.
	reader := New().DumpZip(context.Background(), ver, pidFile)
	consumeSome(t, reader)

	pid := readPID(t, pidFile)
	require.True(t, processAlive(pid), "pg_dump should be running")

	require.NoError(t, reader.Close())

	require.True(
		t, waitForExit(pid, 10*time.Second),
		"closing the zip reader must terminate pg_dump",
	)
}

// TestTestHonoursDeadline verifies the connectivity check gives up instead of
// leaving a psql process alive against a host that never answers.
func TestTestHonoursDeadline(t *testing.T) {
	skipWithoutShellTools(t)

	script, pidFile := endlessScript(t)
	ver := PGVersion{Value: version{Version: "test", PSQL: script}}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := New().Test(ctx, ver, pidFile)
	require.Error(t, err)
	require.Less(
		t, time.Since(start), 30*time.Second,
		"Test must honour the caller's deadline instead of hanging",
	)

	pid := readPID(t, pidFile)
	require.True(
		t, waitForExit(pid, 10*time.Second),
		"a timed-out connectivity check must not leave psql running",
	)
}
