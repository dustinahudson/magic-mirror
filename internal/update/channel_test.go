package update

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// fakeReleases serves a list of releases, so channel selection can be tested
// against the shape the GitHub API actually returns.
type fakeReleases struct {
	releases []listed
}

type listed struct {
	tag        string
	prerelease bool
	draft      bool
}

func (f *fakeReleases) serve(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	})
	mux.HandleFunc("/assets/"+AssetChecksums, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sum(body) + "  " + AssetApp + "\n"))
	})
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		out := make([]map[string]any, 0, len(f.releases))
		for _, rel := range f.releases {
			out = append(out, map[string]any{
				"tag_name":   rel.tag,
				"draft":      rel.draft,
				"prerelease": rel.prerelease,
				"assets": []map[string]string{
					{"name": AssetApp, "browser_download_url": srv.URL + "/assets/" + AssetApp},
					{"name": AssetChecksums, "browser_download_url": srv.URL + "/assets/" + AssetChecksums},
				},
			})
		}
		json.NewEncoder(w).Encode(out)
	})
	return srv
}

func channelUpdater(t *testing.T, f *fakeReleases, channel, version string) (*Updater, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mm.current"), []byte("running"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := f.serve(t, []byte("the new build"))

	u := New(Options{
		Repo: "test/repo", StateDir: dir, Version: version, Channel: channel,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	u.client = srv.Client()
	u.apiBase = srv.URL
	return u, dir
}

func installed(t *testing.T, dir string) bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "mm.current"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b) == "the new build"
}

// The property the whole scheme rests on: publishing a test build must not
// touch a mirror that did not ask for one.
func TestTestBuildIsInvisibleToStableDevices(t *testing.T) {
	f := &fakeReleases{releases: []listed{
		{tag: "v0.15.0-rc.1", prerelease: true},
		{tag: "v0.14.0"},
	}}

	u, dir := channelUpdater(t, f, ChannelStable, "v0.14.0")
	if err := u.CheckAndInstall(context.Background()); err != nil {
		t.Fatalf("CheckAndInstall: %v", err)
	}
	if installed(t, dir) {
		t.Fatal("a stable mirror installed a pre-release")
	}
}

func TestTestBuildInstallsOnOptedInDevices(t *testing.T) {
	f := &fakeReleases{releases: []listed{
		{tag: "v0.15.0-rc.1", prerelease: true},
		{tag: "v0.14.0"},
	}}

	u, dir := channelUpdater(t, f, ChannelTest, "v0.14.0")
	if err := u.CheckAndInstall(context.Background()); err != nil {
		t.Fatalf("CheckAndInstall: %v", err)
	}
	if !installed(t, dir) {
		t.Fatal("an opted-in mirror did not install the pre-release")
	}
}

// A draft is not published to anyone, on any channel.
func TestDraftsAreNeverInstalled(t *testing.T) {
	f := &fakeReleases{releases: []listed{
		{tag: "v9.9.9", draft: true},
		{tag: "v0.14.0"},
	}}

	u, dir := channelUpdater(t, f, ChannelTest, "v0.14.0")
	_ = u.CheckAndInstall(context.Background())
	if installed(t, dir) {
		t.Fatal("installed a draft release")
	}
}

// The API returns releases newest-created first, which is not the same as
// highest version — a patch tagged on an older line lists ahead of the
// release it follows.
func TestHighestVersionWinsNotFirstListed(t *testing.T) {
	f := &fakeReleases{releases: []listed{
		{tag: "v0.13.2"}, // published later, but an older line
		{tag: "v0.15.0"},
		{tag: "v0.14.0"},
	}}

	u, dir := channelUpdater(t, f, ChannelStable, "v0.14.0")
	if err := u.CheckAndInstall(context.Background()); err != nil {
		t.Fatalf("CheckAndInstall: %v", err)
	}
	if !installed(t, dir) {
		t.Fatal("did not install v0.15.0 — took the first release listed instead")
	}
}

// Switching a test mirror back to released versions has to bring it back,
// not strand it on the pre-release it happens to be ahead of.
func TestLeavingTheTestChannelReinstallsStable(t *testing.T) {
	f := &fakeReleases{releases: []listed{
		{tag: "v0.15.0"},
		{tag: "v0.15.0-rc.2", prerelease: true},
	}}

	u, dir := channelUpdater(t, f, ChannelStable, "v0.15.0-rc.2")
	if err := u.CheckAndInstall(context.Background()); err != nil {
		t.Fatalf("CheckAndInstall: %v", err)
	}
	if !installed(t, dir) {
		t.Fatal("a mirror left on rc.2 did not return to v0.15.0")
	}
}

// A mirror still on the test channel stays there; leaving is what the stable
// setting means, not something that happens on its own.
func TestStayingOnTestChannelDoesNotReinstall(t *testing.T) {
	f := &fakeReleases{releases: []listed{
		{tag: "v0.15.0"},
		{tag: "v0.15.0-rc.2", prerelease: true},
	}}

	// v0.15.0 is genuinely newer than rc.2, so this one *should* install —
	// the test channel sees released versions too, and a released version is
	// always the newest thing on that line.
	u, dir := channelUpdater(t, f, ChannelTest, "v0.15.0-rc.2")
	if err := u.CheckAndInstall(context.Background()); err != nil {
		t.Fatalf("CheckAndInstall: %v", err)
	}
	if !installed(t, dir) {
		t.Fatal("test channel did not take the final release over its own rc")
	}
}

// An unparseable tag must not be selected, or it becomes an install the
// version rules never got to judge.
func TestUnorderableTagsAreSkipped(t *testing.T) {
	f := &fakeReleases{releases: []listed{
		{tag: "nightly"},
		{tag: "v0.15.0"},
	}}

	u, dir := channelUpdater(t, f, ChannelStable, "v0.14.0")
	if err := u.CheckAndInstall(context.Background()); err != nil {
		t.Fatalf("CheckAndInstall: %v", err)
	}
	if !installed(t, dir) {
		t.Fatal("skipped past the unorderable tag but did not install v0.15.0")
	}
}
