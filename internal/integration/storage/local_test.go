package storage

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestLocalDelete(t *testing.T) {
	tests := []struct {
		name    string
		create  string // a file created before deleting, relative to the test dir
		target  string // what is deleted, relative to the test dir
		wantErr bool
	}{
		{
			// The file of a backup that failed before writing it, or of a
			// volume that was recreated. What the caller wants, no file,
			// is already true.
			name:   "a missing file counts as deleted",
			target: "missing.zip",
		},
		{
			name:   "an existing file is removed",
			create: "dump.zip",
			target: "dump.zip",
		},
		{
			// Only "does not exist" became success; every other failure
			// is still reported.
			name:    "a directory that is not empty is still an error",
			create:  "dir/dump.zip",
			target:  "dir",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A missing file needs no writable backups directory, so that
			// case runs everywhere.
			dir := "pbw-test-" + uuid.NewString()
			if tt.create != "" {
				dir = createLocalBackupFile(t, tt.create)
			}
			path := dir + "/" + tt.target

			err := newTestClient().LocalDelete(path)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			_, err = os.Stat(newTestClient().LocalGetFullPath(path))
			require.ErrorIs(t, err, fs.ErrNotExist)
		})
	}
}

// createLocalBackupFile creates a file, and the directories leading to it,
// inside a new directory in the local backups directory, and returns that
// directory relative to it. It skips the test when the local backups directory
// is not writable, as on a host outside the dev image.
func createLocalBackupFile(t *testing.T, relativeFilePath string) string {
	t.Helper()

	dir, err := os.MkdirTemp(localBackupsDir, "pbw-test-")
	if err != nil {
		t.Skipf("%s is not writable: %v", localBackupsDir, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	fullPath := filepath.Join(dir, relativeFilePath)
	require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0o755))
	require.NoError(t, os.WriteFile(fullPath, []byte("backup"), 0o644))

	return filepath.Base(dir)
}
