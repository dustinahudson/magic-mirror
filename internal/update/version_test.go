package update

import "testing"

// The sequence a test device actually walks: it takes rc1, then rc2, then the
// final release. Before pre-release ordering existed, all three compared
// equal, so the device took rc1 and then never moved again — the test channel
// delivered one build and went quiet.
func TestReleaseCandidateSequenceInstalls(t *testing.T) {
	steps := []struct{ from, to string }{
		{"v0.14.0", "v0.15.0-rc.1"},
		{"v0.15.0-rc.1", "v0.15.0-rc.2"},
		{"v0.15.0-rc.2", "v0.15.0"},
		{"v0.15.0", "v0.16.0-rc.1"},
	}
	for _, s := range steps {
		if !ShouldInstall(s.from, s.to) {
			t.Errorf("a device on %s would not take %s", s.from, s.to)
		}
	}
}

func TestShouldInstallRefusesBackwards(t *testing.T) {
	steps := []struct{ from, to string }{
		{"v0.15.0", "v0.15.0-rc.2"}, // the final release outranks its rc
		{"v0.15.0-rc.2", "v0.15.0-rc.1"},
		{"v0.15.0", "v0.14.0"},
		{"v0.15.0", "v0.15.0"},
	}
	for _, s := range steps {
		if ShouldInstall(s.from, s.to) {
			t.Errorf("a device on %s would take %s", s.from, s.to)
		}
	}
}

// Undotted counters are what people actually type. Strict SemVer compares
// them lexically, which puts rc10 before rc2 and silently strands a device.
func TestUndottedCountersSortNaturally(t *testing.T) {
	if !ShouldInstall("v0.15.0-rc2", "v0.15.0-rc10") {
		t.Error("rc10 not treated as newer than rc2")
	}
	if ShouldInstall("v0.15.0-rc10", "v0.15.0-rc2") {
		t.Error("rc2 treated as newer than rc10")
	}
	if !ShouldInstall("v0.15.0-rc9", "v0.15.0") {
		t.Error("the final release does not supersede rc9")
	}
}

// A tag that cannot be ordered must not install. It is how a Circle OS
// release nearly landed on a Linux device.
func TestUnparseableTagsNeverInstall(t *testing.T) {
	for _, tag := range []string{"", "latest", "v1", "v1.2", "v1.2.3.4", "vX.Y.Z", "v0.15.0-"} {
		if ShouldInstall("v0.14.0", tag) {
			t.Errorf("tag %q installed", tag)
		}
	}
}

// Iterating on a device must never have it replace itself underneath you.
func TestDevBuildsNeverUpdate(t *testing.T) {
	for _, cur := range []string{
		"v0.14.0-46-geb8d139-dirty",
		"v0.14.0-46-geb8d139",
		"v0.15.0-rc.1-dirty",
		"dev",
		"",
	} {
		if ShouldInstall(cur, "v9.9.9") {
			t.Errorf("dev build %q updated", cur)
		}
		if LeavingTestChannel(cur, "v0.15.0") {
			t.Errorf("dev build %q took the leave-test-channel path", cur)
		}
	}
}

// Switching a borrowed test device back to stable has to actually bring it
// back, or it sits on the pre-release until the next version ships.
func TestLeavingTestChannel(t *testing.T) {
	if !LeavingTestChannel("v0.15.0-rc.2", "v0.15.0") {
		t.Error("a device on rc.2 cannot return to v0.15.0")
	}
	// Not a general downgrade path.
	if LeavingTestChannel("v0.15.0-rc.2", "v0.14.0") {
		t.Error("allowed a downgrade to an older line")
	}
	if LeavingTestChannel("v0.15.0", "v0.14.0") {
		t.Error("allowed a downgrade from a final release")
	}
	if LeavingTestChannel("v0.15.0-rc.2", "v0.16.0-rc.1") {
		t.Error("allowed a sideways move to another pre-release")
	}
}

func TestIsTestChannel(t *testing.T) {
	for _, c := range []string{"test", "prerelease", "beta", "TEST", " test "} {
		if !IsTestChannel(c) {
			t.Errorf("%q not recognised as the test channel", c)
		}
	}
	// A typo must not silently opt a device into test builds.
	for _, c := range []string{"", "stable", "STABLE", "tset", "unstable", "prod"} {
		if IsTestChannel(c) {
			t.Errorf("%q opted a device into test builds", c)
		}
	}
}

func TestCompareVersionOrdering(t *testing.T) {
	// Ascending. Every pair must compare consistently with its position.
	ordered := []string{
		"v0.14.0",
		"v0.15.0-alpha",
		"v0.15.0-alpha.1",
		"v0.15.0-beta",
		"v0.15.0-rc.1",
		"v0.15.0-rc.2",
		"v0.15.0-rc.10",
		"v0.15.0",
		"v0.15.1",
		"v1.0.0",
	}
	for i := range ordered {
		for j := range ordered {
			a, ok := parseVersion(ordered[i])
			if !ok {
				t.Fatalf("cannot parse %q", ordered[i])
			}
			b, _ := parseVersion(ordered[j])

			got := compareVersion(a, b)
			want := cmpInt(i, j)
			if got != want {
				t.Errorf("compare(%s, %s) = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
}

// Build metadata is not part of precedence.
func TestBuildMetadataIgnored(t *testing.T) {
	a, ok := parseVersion("v0.15.0+20260730")
	if !ok {
		t.Fatal("build metadata made the tag unparseable")
	}
	b, _ := parseVersion("v0.15.0")
	if compareVersion(a, b) != 0 {
		t.Error("build metadata affected ordering")
	}
}
