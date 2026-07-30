// Package update polls GitHub releases and installs new builds.
//
// Two tiers, both with rollback:
//
//	app  mm.current  keep mm.previous; mm-supervise reverts after 3
//	                 starts that never reach healthy
//	os   kernel.img  keep kernel.prev.img
//
// The rules that distinguish this from v1's UpdateService
// (src/services/update_service.cpp):
//
//   - Verify before installing. v1 checked only that the download was
//     non-empty (line 201).
//   - Never delete what you are replacing. v1 called f_unlink(KERNEL_IMG)
//     *before* the rename (line 211), so a bad kernel left nothing to fall
//     back to and the device was recoverable only by pulling the card.
//   - Download to a temp file on the same filesystem and rename, so a
//     power cut mid-download cannot leave a half-written binary in place.
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
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Tier is which artifact an update replaces.
type Tier string

const (
	// TierApp replaces the Go binary. Frequent, cheap, no reboot.
	TierApp Tier = "app"

	// TierOS replaces the kernel and its initramfs. Rare, needs a reboot.
	TierOS Tier = "os"
)

// Asset names expected on a release.
const (
	AssetApp       = "magicmirror-armv6"
	AssetOS        = "kernel.img"
	AssetChecksums = "SHA256SUMS"
)

// Update channels.
//
// A test build is published as a GitHub pre-release, and the flag is what
// keeps it away from everyone else. That is not only this updater's rule:
// the v1 Circle OS firmware still in the field polls /releases/latest, which
// GitHub defines as excluding pre-releases, so the same flag hides a build
// from v1 devices too. It matters more there, because v1 unlinks its kernel
// before writing the new one and takes whichever asset the API lists first —
// a stable v2 release would hand a v1 device the wrong file and leave it
// with nothing to boot.
const (
	ChannelStable = "stable"
	ChannelTest   = "test"
)

// IsTestChannel reports whether c opts a device into test builds.
//
// "prerelease" is accepted as well as "test" because that is what the field
// was originally called, and a config file on a device outlives the name we
// happened to give a setting.
func IsTestChannel(c string) bool {
	switch strings.ToLower(strings.TrimSpace(c)) {
	case ChannelTest, "prerelease", "beta":
		return true
	}
	return false
}

// ShouldInstall reports whether release tag is a genuine upgrade from the
// running version.
//
// v1 asked only whether the tag *differed* (update_service.cpp:283), which
// is wrong in a way that showed up immediately on hardware: a development
// build reported itself as "v0.14.0-21-gd1c3a8f-dirty", the newest published
// release was the old bare-metal v0.14.0, the strings differed, and the
// updater cheerfully set about installing a Circle OS kernel onto a Linux
// device. Only the checksum requirement stopped it.
//
// Two rules:
//
//   - A development build never updates. If the running version is not a
//     clean release tag, someone is iterating on this device and having it
//     replace itself underneath them is never what they want.
//   - Only a strictly newer version installs. Not different — newer.
func ShouldInstall(current, tag string) bool {
	if IsDevBuild(current) {
		return false
	}
	cur, ok := parseVersion(current)
	if !ok {
		return false
	}
	next, ok := parseVersion(tag)
	if !ok {
		return false
	}
	return compareVersion(next, cur) > 0
}

// IsDevBuild reports whether a version string came from `git describe`
// rather than a clean tag — either dirty, or some commits past a tag.
func IsDevBuild(v string) bool {
	if v == "" || v == "dev" {
		return true
	}
	if strings.HasSuffix(v, "-dirty") {
		return true
	}
	// "v0.14.0-21-gd1c3a8f" — a tag, a commit count, and a hash.
	parts := strings.Split(v, "-")
	if len(parts) >= 3 && strings.HasPrefix(parts[len(parts)-1], "g") {
		return true
	}
	return false
}

// LeavingTestChannel reports whether tag is the release a device should take
// to get back off the test channel.
//
// A device that tried a test build sits ahead of stable: v0.15.0-rc.2 is
// newer than v0.15.0's predecessor, so switching the channel back to stable
// leaves it stranded on the pre-release until the next version ships. That
// is the wrong end state for a device someone borrowed for testing, so the
// matching final release is allowed to install over a pre-release of the
// same base version even though it is not, by precedence, an upgrade.
//
// Only over a pre-release, and only at the same base version: this is a
// device leaving a test build behind, not a general downgrade path.
func LeavingTestChannel(current, tag string) bool {
	if IsDevBuild(current) {
		return false
	}
	cur, ok := parseVersion(current)
	if !ok || !cur.isPrerelease() {
		return false
	}
	next, ok := parseVersion(tag)
	if !ok || next.isPrerelease() {
		return false
	}
	return next.base == cur.base
}

// Release is a GitHub release.
type Release struct {
	Tag        string
	Prerelease bool
	Assets     map[string]string // name -> download URL
}

// Options configures an Updater.
type Options struct {
	// Repo is "owner/name".
	Repo string

	// StateDir is the FAT partition, where the binaries and kernel live.
	StateDir string

	// Version is what is currently running.
	Version string

	// Channel is ChannelStable or ChannelTest. Anything unrecognised is
	// treated as stable, so a typo cannot silently opt a device into test
	// builds.
	Channel string

	// Interval between checks.
	Interval time.Duration

	// AllowOS enables kernel updates. Off by default: an OS update needs a
	// reboot and carries more risk than an app swap, so it should be a
	// deliberate choice.
	AllowOS bool
}

// Updater polls for and installs releases.
type Updater struct {
	opts   Options
	log    *slog.Logger
	client *http.Client

	// apiBase is the GitHub API root, overridable so the install and
	// verification paths can be tested against a fake release without
	// reaching the network.
	apiBase string

	// RequestRestart is called after an app update installs. Exiting cleanly
	// lets init respawn the new binary, which is the whole restart
	// mechanism — no reboot involved.
	RequestRestart func(reason string)
}

// New returns an Updater.
func New(opts Options, log *slog.Logger) *Updater {
	if opts.Interval <= 0 {
		opts.Interval = time.Hour
	}
	return &Updater{
		opts:    opts,
		log:     log,
		client:  &http.Client{Timeout: 5 * time.Minute},
		apiBase: "https://api.github.com",
	}
}

// Run polls until ctx is cancelled.
func (u *Updater) Run(ctx context.Context) {
	// A short initial delay so an update check never competes with the
	// first render or the initial data fetches.
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Minute):
	}

	for {
		if err := u.CheckAndInstall(ctx); err != nil {
			// Never fatal. A failed update check is the least important
			// thing this device does.
			u.log.Warn("update check failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(u.opts.Interval):
		}
	}
}

// CheckAndInstall performs one poll.
func (u *Updater) CheckAndInstall(ctx context.Context) error {
	rel, err := u.latest(ctx)
	if err != nil {
		return err
	}

	// Either a genuine upgrade, or a device stepping back off a test build
	// onto the matching stable release.
	if !ShouldInstall(u.opts.Version, rel.Tag) &&
		!(!IsTestChannel(u.opts.Channel) && LeavingTestChannel(u.opts.Version, rel.Tag)) {
		u.log.Debug("no update needed", "current", u.opts.Version, "latest", rel.Tag)
		return nil
	}
	u.log.Info("update available", "current", u.opts.Version, "latest", rel.Tag)

	sums, err := u.checksums(ctx, rel)
	if err != nil {
		// Refusing to install unverified code is the point. v1 would have
		// installed anything non-empty.
		return fmt.Errorf("no verifiable checksums for %s: %w", rel.Tag, err)
	}

	if u.opts.AllowOS {
		if url, ok := rel.Assets[AssetOS]; ok {
			if err := u.install(ctx, TierOS, url, sums[AssetOS],
				filepath.Join(u.opts.StateDir, "kernel.img"),
				filepath.Join(u.opts.StateDir, "kernel.prev.img")); err != nil {
				return fmt.Errorf("os update: %w", err)
			}
			u.log.Info("kernel updated; a reboot is required to run it", "version", rel.Tag)
		}
	}

	url, ok := rel.Assets[AssetApp]
	if !ok {
		return fmt.Errorf("release %s has no %s asset", rel.Tag, AssetApp)
	}
	if err := u.install(ctx, TierApp, url, sums[AssetApp],
		filepath.Join(u.opts.StateDir, "mm.current"),
		filepath.Join(u.opts.StateDir, "mm.previous")); err != nil {
		return fmt.Errorf("app update: %w", err)
	}

	u.log.Info("update installed; restarting", "version", rel.Tag)
	if u.RequestRestart != nil {
		u.RequestRestart("updated to " + rel.Tag)
	}
	return nil
}

// install downloads, verifies, and swaps a single artifact.
//
// The order is what makes this safe: download to a temp file beside the
// target, verify the checksum, keep the existing file as the rollback copy,
// and only then rename the new one into place.
func (u *Updater) install(ctx context.Context, tier Tier, url, wantSum, target, backup string) error {
	if wantSum == "" {
		return fmt.Errorf("%s: no checksum published for this asset", tier)
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), ".update-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	sum, err := u.download(ctx, url, tmp)
	tmp.Close()
	if err != nil {
		return err
	}

	if !strings.EqualFold(sum, wantSum) {
		return fmt.Errorf("%s: checksum mismatch (got %s, want %s)", tier, sum, wantSum)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	// Keep the outgoing artifact as the rollback copy. Note this is a copy
	// to a *different* name rather than a delete: at no point is there no
	// working artifact on the card.
	if _, err := os.Stat(target); err == nil {
		if err := copyFile(target, backup); err != nil {
			return fmt.Errorf("could not preserve rollback copy: %w", err)
		}
	}

	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("install: %w", err)
	}
	syncDir(filepath.Dir(target))
	return nil
}

func (u *Updater) download(ctx context.Context, url string, w io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "magic-mirror/2")

	resp, err := u.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, h), resp.Body); err != nil {
		return "", fmt.Errorf("download body: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// latest fetches the newest release matching the configured channel.
func (u *Updater) latest(ctx context.Context) (Release, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/releases", u.apiBase, u.opts.Repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "magic-mirror/2")

	resp, err := u.client.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("github releases: HTTP %d", resp.StatusCode)
	}

	var raw []struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&raw); err != nil {
		return Release{}, fmt.Errorf("decode releases: %w", err)
	}

	// Highest version wins, not first listed. The API returns releases
	// newest-created first, which is usually the same order but not when a
	// fix is tagged on an older line, or when a test build is published
	// after the stable release it was leading up to.
	var best Release
	var bestV version
	found := false

	wantPre := IsTestChannel(u.opts.Channel)
	for _, r := range raw {
		if r.Draft {
			continue
		}
		if r.Prerelease && !wantPre {
			continue
		}
		v, ok := parseVersion(r.TagName)
		if !ok {
			// A tag we cannot order is a tag we cannot safely install.
			u.log.Warn("ignoring release with unparseable tag", "tag", r.TagName)
			continue
		}
		if found && compareVersion(v, bestV) <= 0 {
			continue
		}

		best = Release{Tag: r.TagName, Prerelease: r.Prerelease, Assets: map[string]string{}}
		for _, a := range r.Assets {
			best.Assets[a.Name] = a.URL
		}
		bestV, found = v, true
	}

	if !found {
		return Release{}, fmt.Errorf("no release found on channel %q", u.opts.Channel)
	}
	return best, nil
}

// checksums fetches and parses the SHA256SUMS asset.
func (u *Updater) checksums(ctx context.Context, rel Release) (map[string]string, error) {
	url, ok := rel.Assets[AssetChecksums]
	if !ok {
		return nil, fmt.Errorf("release has no %s asset", AssetChecksums)
	}

	var buf strings.Builder
	if _, err := u.download(ctx, url, &buf); err != nil {
		return nil, err
	}

	out := map[string]string{}
	for _, line := range strings.Split(buf.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		// sha256sum writes "<hash>  <name>", with a leading * for binary.
		out[strings.TrimPrefix(fields[1], "*")] = fields[0]
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s contained no usable entries", AssetChecksums)
	}
	return out, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// syncDir flushes a directory entry so a rename survives power loss.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	_ = d.Sync()
}
