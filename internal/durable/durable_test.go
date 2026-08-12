package durable

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileCreatesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := WriteFile(path, []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "first\n" {
		t.Errorf("got %q, want %q", got, "first\n")
	}

	if err := WriteFile(path, []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "second\n" {
		t.Errorf("got %q, want %q", got, "second\n")
	}
}

// The boot partition is small and user-visible. A failed or successful write
// must not leave dot-files scattered next to the real ones.
func TestWriteFileLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clock")

	for range 5 {
		if err := WriteFile(path, []byte("202608122111.33\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "clock" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected only 'clock', got: %s", strings.Join(names, ", "))
	}
}

// wpa_supplicant.conf holds a password and must not be world-readable, even
// briefly. CreateTemp makes files 0600, so the mode has to be set explicitly
// before the rename rather than after.
func TestWriteFileHonoursPermissions(t *testing.T) {
	dir := t.TempDir()

	for _, perm := range []os.FileMode{0o600, 0o644} {
		path := filepath.Join(dir, "f")
		if err := WriteFile(path, []byte("x"), perm); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != perm {
			t.Errorf("mode = %v, want %v", info.Mode().Perm(), perm)
		}
	}
}

// A write that cannot start must leave whatever was there untouched, rather
// than truncating it on the way to failing.
func TestWriteFileFailureLeavesExistingFileIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := WriteFile(path, []byte("good\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A directory that cannot be written to means CreateTemp fails, which is
	// the closest reachable stand-in for the disk giving up mid-write.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	if err := WriteFile(path, []byte("replacement\n"), 0o644); err == nil {
		t.Fatal("expected an error writing into a read-only directory")
	}
	if got := read(t, path); got != "good\n" {
		t.Errorf("existing file was damaged: got %q, want %q", got, "good\n")
	}
}

func TestWriteFileIntoMissingDirectoryErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope", "clock")
	if err := WriteFile(path, []byte("x"), 0o644); err == nil {
		t.Fatal("expected an error")
	}
}

func TestSyncDirOnMissingDirectoryErrors(t *testing.T) {
	if err := SyncDir(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected an error")
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
