package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// fakeRelease serves a GitHub-shaped releases API plus assets.
type fakeRelease struct {
	tag     string
	app     []byte
	kernel  []byte
	badSums bool
	noSums  bool
}

func (f *fakeRelease) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/assets/"+AssetApp, func(w http.ResponseWriter, r *http.Request) {
		w.Write(f.app)
	})
	mux.HandleFunc("/assets/"+AssetOS, func(w http.ResponseWriter, r *http.Request) {
		w.Write(f.kernel)
	})
	mux.HandleFunc("/assets/"+AssetChecksums, func(w http.ResponseWriter, r *http.Request) {
		appSum := sum(f.app)
		if f.badSums {
			appSum = sum([]byte("something else entirely"))
		}
		fmt.Fprintf(w, "%s  %s\n%s  %s\n", appSum, AssetApp, sum(f.kernel), AssetOS)
	})

	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		assets := []map[string]string{
			{"name": AssetApp, "browser_download_url": srv.URL + "/assets/" + AssetApp},
			{"name": AssetOS, "browser_download_url": srv.URL + "/assets/" + AssetOS},
		}
		if !f.noSums {
			assets = append(assets, map[string]string{
				"name": AssetChecksums, "browser_download_url": srv.URL + "/assets/" + AssetChecksums,
			})
		}
		json.NewEncoder(w).Encode([]map[string]any{{
			"tag_name": f.tag, "draft": false, "prerelease": false, "assets": assets,
		}})
	})
	return srv
}

func testUpdater(t *testing.T, f *fakeRelease, stateDir string) (*Updater, *httptest.Server) {
	t.Helper()
	srv := f.server(t)

	u := New(Options{
		Repo:     "test/repo",
		StateDir: stateDir,
		Version:  "v0.0.1",
		Channel:  "stable",
		AllowOS:  true,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Point the GitHub client at the fake.
	u.client = srv.Client()
	u.apiBase = srv.URL
	return u, srv
}

func TestInstallsAndKeepsRollbackCopy(t *testing.T) {
	dir := t.TempDir()
	old := []byte("the currently running binary")
	if err := os.WriteFile(filepath.Join(dir, "mm.current"), old, 0o755); err != nil {
		t.Fatal(err)
	}

	f := &fakeRelease{tag: "v1.0.0", app: []byte("a shiny new binary"), kernel: []byte("a kernel")}
	u, _ := testUpdater(t, f, dir)

	restarted := ""
	u.RequestRestart = func(reason string) { restarted = reason }

	if err := u.CheckAndInstall(context.Background()); err != nil {
		t.Fatalf("CheckAndInstall: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "mm.current"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(f.app) {
		t.Errorf("mm.current = %q, want the new binary", got)
	}

	// The rollback copy is the whole safety net. v1 deleted the old kernel
	// before installing the new one, leaving nothing to revert to.
	prev, err := os.ReadFile(filepath.Join(dir, "mm.previous"))
	if err != nil {
		t.Fatalf("no rollback copy kept: %v", err)
	}
	if string(prev) != string(old) {
		t.Errorf("mm.previous = %q, want the outgoing binary", prev)
	}

	if restarted == "" {
		t.Error("no restart requested after installing an update")
	}
}

// The load-bearing safety property: a corrupted or tampered download must
// not be installed, and must leave the running binary untouched.
func TestChecksumMismatchInstallsNothing(t *testing.T) {
	dir := t.TempDir()
	old := []byte("the currently running binary")
	target := filepath.Join(dir, "mm.current")
	if err := os.WriteFile(target, old, 0o755); err != nil {
		t.Fatal(err)
	}

	f := &fakeRelease{
		tag: "v1.0.0", app: []byte("a corrupted download"),
		kernel: []byte("a kernel"), badSums: true,
	}
	u, _ := testUpdater(t, f, dir)

	err := u.CheckAndInstall(context.Background())
	if err == nil {
		t.Fatal("installed an asset whose checksum did not match")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error = %v, want a checksum mismatch", err)
	}

	got, _ := os.ReadFile(target)
	if string(got) != string(old) {
		t.Errorf("mm.current was modified despite a failed verification: %q", got)
	}
}

// No checksums published means no install. v1 checked only that the
// downloaded file was non-empty.
func TestMissingChecksumsRefusesToInstall(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "mm.current")
	os.WriteFile(target, []byte("original"), 0o755)

	f := &fakeRelease{tag: "v1.0.0", app: []byte("new"), kernel: []byte("k"), noSums: true}
	u, _ := testUpdater(t, f, dir)

	if err := u.CheckAndInstall(context.Background()); err == nil {
		t.Fatal("installed without any published checksums")
	}

	got, _ := os.ReadFile(target)
	if string(got) != "original" {
		t.Error("mm.current was modified despite unverifiable assets")
	}
}

func TestSameVersionIsNoOp(t *testing.T) {
	dir := t.TempDir()
	f := &fakeRelease{tag: "v0.0.1", app: []byte("new"), kernel: []byte("k")}
	u, _ := testUpdater(t, f, dir)

	if err := u.CheckAndInstall(context.Background()); err != nil {
		t.Fatalf("CheckAndInstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mm.current")); err == nil {
		t.Error("installed something when already on the latest version")
	}
}

func TestOSTierKeepsPreviousKernel(t *testing.T) {
	dir := t.TempDir()
	oldKernel := []byte("the running kernel")
	os.WriteFile(filepath.Join(dir, "kernel.img"), oldKernel, 0o755)
	os.WriteFile(filepath.Join(dir, "mm.current"), []byte("app"), 0o755)

	f := &fakeRelease{tag: "v2.0.0", app: []byte("new app"), kernel: []byte("new kernel")}
	u, _ := testUpdater(t, f, dir)

	if err := u.CheckAndInstall(context.Background()); err != nil {
		t.Fatalf("CheckAndInstall: %v", err)
	}

	prev, err := os.ReadFile(filepath.Join(dir, "kernel.prev.img"))
	if err != nil {
		t.Fatalf("no previous kernel kept: %v", err)
	}
	if string(prev) != string(oldKernel) {
		t.Errorf("kernel.prev.img = %q, want the outgoing kernel", prev)
	}
}

// Regression test for a hazard seen on real hardware.
//
// A device running "v0.14.0-21-gd1c3a8f-dirty" saw the newest published
// release — the old bare-metal v0.14.0 — decided it was an upgrade because
// the strings differed, and set about installing a Circle OS kernel onto a
// Linux device. Only the missing SHA256SUMS asset stopped it.
func TestShouldInstall(t *testing.T) {
	cases := []struct {
		current, tag string
		want         bool
		why          string
	}{
		// The case that actually happened.
		{"v0.14.0-21-gd1c3a8f-dirty", "v0.14.0", false, "dev build must never update"},

		{"v1.0.0", "v1.0.1", true, "patch bump is an upgrade"},
		{"v1.0.0", "v1.1.0", true, "minor bump is an upgrade"},
		{"v1.0.0", "v2.0.0", true, "major bump is an upgrade"},

		{"v1.0.0", "v1.0.0", false, "same version is not an upgrade"},
		{"v1.2.0", "v1.1.9", false, "older version must never install"},
		{"v2.0.0", "v0.14.0", false, "much older version must never install"},

		{"dev", "v1.0.0", false, "unversioned build must not update"},
		{"", "v1.0.0", false, "empty version must not update"},
		{"v1.0.0", "not-a-version", false, "unparseable tag must not install"},

		{"v1.0.0-3-gabc1234", "v1.1.0", false, "commits past a tag is still a dev build"},
	}

	for _, c := range cases {
		if got := ShouldInstall(c.current, c.tag); got != c.want {
			t.Errorf("ShouldInstall(%q, %q) = %v, want %v — %s",
				c.current, c.tag, got, c.want, c.why)
		}
	}
}

func TestIsDevBuild(t *testing.T) {
	dev := []string{"", "dev", "v1.0.0-dirty", "v0.14.0-21-gd1c3a8f", "v0.14.0-21-gd1c3a8f-dirty"}
	rel := []string{"v1.0.0", "v0.14.0", "v2.13.4", "v1.0.0-rc1"}

	for _, v := range dev {
		if !IsDevBuild(v) {
			t.Errorf("IsDevBuild(%q) = false, want true", v)
		}
	}
	for _, v := range rel {
		if IsDevBuild(v) {
			t.Errorf("IsDevBuild(%q) = true, want false", v)
		}
	}
}
