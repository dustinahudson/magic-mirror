package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// A config the mirror cannot use must not keep it off the screen.
//
// Every case here used to exit non-zero. On the device that means mm-supervise
// restarts into the same bad bytes forever and the panel stays black, which is
// the least debuggable outcome available: the reason is on a card you have to
// unplug the mirror to read.
func TestLoadConfigFallsBackToDefaults(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		// The one that actually happened. An interrupted save left
		// config.json zero-length, and json.Decode reports a bare EOF.
		{"empty file", ""},
		{"truncated object", `{"timezone": "America/Chicago",`},
		{"not json at all", "\x00\x00\x00\x00"},
		{"wrong shape", `["not", "an", "object"]`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}

			cfg, _, err := loadConfig(path, quietLog())
			if err != nil {
				t.Fatalf("loadConfig returned an error, which exits the process "+
					"and blanks the screen: %v", err)
			}
			if len(cfg.Widgets) == 0 {
				t.Error("fell back to an empty config; the mirror would render nothing")
			}
		})
	}
}

// Recovery depends on the unreadable file still being there. Booting on
// defaults is only safe because it does not destroy whatever the user had.
func TestLoadConfigLeavesUnusableFileAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := `{"timezone": "America/Chicago", "widgets": [`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := loadConfig(path, quietLog()); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config file no longer readable: %v", err)
	}
	if string(after) != original {
		t.Errorf("config file was modified\n before: %q\n  after: %q", original, after)
	}
}

func TestLoadConfigMissingFileUsesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")

	cfg, _, err := loadConfig(path, quietLog())
	if err != nil {
		t.Fatalf("a missing config on first boot is normal: %v", err)
	}
	if len(cfg.Widgets) == 0 {
		t.Error("defaults have no widgets")
	}
}

// The fallback must not swallow configs that are merely unusual.
func TestLoadConfigValidFileIsHonoured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{
	  "timezone": "America/Chicago",
	  "widgets": [
	    {"id": "clock", "type": "datetime",
	     "pos": {"col": 0, "row": 0, "colSpan": 3, "rowSpan": 2}}
	  ]
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := loadConfig(path, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Widgets) != 1 || cfg.Widgets[0].ID != "clock" {
		t.Errorf("did not load the file on disk: got %d widgets %+v",
			len(cfg.Widgets), cfg.Widgets)
	}
}
