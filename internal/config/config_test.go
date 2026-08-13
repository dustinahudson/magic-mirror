package config

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	want := Default()
	want.Timezone = "America/Chicago"
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}

	got, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Timezone != want.Timezone {
		t.Errorf("timezone = %q, want %q", got.Timezone, want.Timezone)
	}
	if len(got.Widgets) != len(want.Widgets) {
		t.Errorf("widgets = %d, want %d", len(got.Widgets), len(want.Widgets))
	}
}

// Save writes through a temp file. If one is ever left behind, the boot
// partition slowly fills with .config-*.json droppings.
func TestSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	for range 3 {
		if err := Save(path, Default()); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	// The config and its last-known-good copy, and nothing else.
	sort.Strings(names)
	if want := []string{"config.json", "config.prev.json"}; !slices.Equal(names, want) {
		t.Errorf("directory holds %v, want %v", names, want)
	}
}

// The backup is the whole point: a lost config.json must cost the last edit,
// not every setting the mirror had.
func TestSaveKeepsTheOutgoingConfigAsBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	first := Default()
	first.Hostname = "first"
	if err := Save(path, first); err != nil {
		t.Fatal(err)
	}
	// Nothing to back up on the very first save.
	if _, err := os.Stat(BackupPath(path)); err == nil {
		t.Error("a backup appeared before there was anything to back up")
	}

	second := Default()
	second.Hostname = "second"
	if err := Save(path, second); err != nil {
		t.Fatal(err)
	}

	got, _, err := Load(BackupPath(path))
	if err != nil {
		t.Fatalf("backup is not loadable: %v", err)
	}
	if got.Hostname != "first" {
		t.Errorf("backup hostname = %q, want the outgoing config %q", got.Hostname, "first")
	}
}

// A config that cannot be parsed must never become the last-known-good copy,
// or the fallback inherits the corruption it exists to survive.
func TestSaveDoesNotBackUpAnUnusableConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	good, err := os.ReadFile(BackupPath(path))
	if os.IsNotExist(err) {
		// Establish a good backup by saving twice.
		if err := Save(path, Default()); err != nil {
			t.Fatal(err)
		}
		good, err = os.ReadFile(BackupPath(path))
	}
	if err != nil {
		t.Fatal(err)
	}

	// Simulate the failure this device actually produces.
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(BackupPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) == 0 || string(after) != string(good) {
		t.Error("a zero-length config was promoted into the backup")
	}
}

// An invalid config must not take the working one with it. Save validates
// before it touches the file so the mirror keeps running what it has.
func TestSaveRejectsInvalidWithoutTouchingTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	bad := Default()
	bad.Widgets = []Instance{
		{ID: "a", Type: "datetime"},
		{ID: "a", Type: "datetime"},
	}
	if err := Save(path, bad); err == nil {
		t.Fatal("saved a config with duplicate widget ids")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("a rejected save modified the config file")
	}
}

// Save must survive the directory it writes into being the one it fsyncs.
// A missing directory is an error, not a panic.
func TestSaveIntoMissingDirectoryErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "config.json")
	if err := Save(path, Default()); err == nil {
		t.Fatal("expected an error saving into a directory that does not exist")
	}
}

// The zero-length file that took the mirror down must read back as a clean
// parse failure rather than an empty-but-valid config.
func TestParseEmptyInputIsAnError(t *testing.T) {
	if _, _, err := Parse(nil); err == nil {
		t.Fatal("empty config parsed as valid")
	}
}

// System updates are opt-in, and staying opt-in across a save is the point:
// a mirror that never asked to replace its kernel must not acquire the
// setting by round-tripping through the config UI.
func TestAllowOSDefaultsOffAndRoundTrips(t *testing.T) {
	if Default().Update.AllowOS {
		t.Error("AllowOS defaults on; it should take saying so")
	}

	// A config written before the setting existed must read as off, not as
	// whatever a missing field might otherwise imply.
	old := `{"update": {"enabled": true, "repo": "o/r", "channel": "stable"}}`
	c, _, err := Parse([]byte(old))
	if err != nil {
		t.Fatal(err)
	}
	if c.Update.AllowOS {
		t.Error("a config predating the setting came back with it on")
	}

	path := filepath.Join(t.TempDir(), "config.json")
	c.Update.AllowOS = true
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Update.AllowOS {
		t.Error("AllowOS did not survive a save and load")
	}
}
