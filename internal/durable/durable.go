// Package durable writes files that survive losing power mid-write.
//
// The mirror has no safe shutdown. It is either plugged in or it is not, and
// it is deployed in houses where nobody is going to run a command before
// pulling the plug. Every write to the boot partition therefore has to assume
// the power will go out during it — not as an edge case, but as the normal way
// the device stops.
//
// The FAT partition makes that harder than usual. Writing a file means
// changing two things: the data clusters, and the directory entry that names
// them. Syncing only the first is the trap, because it looks correct and
// tests clean. What it produces after a power cut is a zero-length file whose
// contents are stranded in orphaned clusters — the file is still listed, still
// opens, and reads as empty.
//
// That is not hypothetical. It emptied a mirror's config.json after a web UI
// edit, and the device spent 56 restarts failing to parse it while showing a
// blank screen.
package durable

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile writes data to path so that a power cut leaves either the previous
// contents or the new ones, never a truncated file and never an empty one.
//
// Write to a temp file beside the target, fsync it, rename it into place, then
// fsync the directory. The last step is the one that is easy to leave out and
// impossible to notice: rename only changes the directory, so without it the
// name can still be pointing nowhere when the power goes.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	// Removing a name that has already been renamed away is harmless, and on
	// every failure below it is what keeps temp files off the card.
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}

	// CreateTemp always makes the file 0600, so anything else has to be set
	// here — before the rename, so the file is never briefly visible at the
	// target name with the wrong mode.
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install %s: %w", path, err)
	}
	return SyncDir(dir)
}

// SyncDir flushes a directory's own entries, which is what makes a completed
// rename durable. Call it after renaming a file into place.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open dir %s: %w", dir, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync dir %s: %w", dir, err)
	}
	return nil
}
