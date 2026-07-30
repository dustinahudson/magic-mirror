package update

import (
	"strconv"
	"strings"
)

// version is a parsed release tag.
//
// pre holds the dot-separated identifiers of a pre-release suffix —
// "v0.15.0-rc.2" gives base {0,15,0} and pre {"rc","2"}. An empty pre means
// a final release, which by SemVer outranks every pre-release of the same
// base version.
type version struct {
	base [3]int
	pre  []string
}

func (v version) isPrerelease() bool { return len(v.pre) > 0 }

// parseVersion reads a "vMAJOR.MINOR.PATCH[-PRERELEASE][+BUILD]" tag.
//
// The pre-release suffix used to be discarded here, which made every
// v0.15.0-rc equal to each other and equal to v0.15.0. A test device could
// take rc1 and would then refuse rc2 and refuse the final release, because
// none of them compared as newer — the test channel delivered exactly one
// build and then went quiet. Ordering pre-releases properly is what makes it
// usable more than once.
func parseVersion(v string) (version, bool) {
	var out version

	s := strings.TrimPrefix(strings.TrimSpace(v), "v")

	// Build metadata is explicitly not part of precedence.
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}

	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre := s[i+1:]
		s = s[:i]
		if pre == "" {
			return out, false
		}
		out.pre = strings.Split(pre, ".")
	}

	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out.base[i] = n
	}
	return out, true
}

// compareVersion orders two versions, returning -1, 0 or 1.
//
// SemVer precedence, with one deliberate deviation: an identifier mixing
// letters and digits is compared in natural order, so rc2 sorts before rc10.
// Strict SemVer compares those lexically, which puts rc10 before rc2 — a
// trap for anyone who writes "-rc10" rather than the dotted "-rc.10", and
// one that surfaces as a device silently refusing an update.
func compareVersion(a, b version) int {
	for i := range 3 {
		switch {
		case a.base[i] > b.base[i]:
			return 1
		case a.base[i] < b.base[i]:
			return -1
		}
	}

	// A pre-release is always older than the release it leads to.
	switch {
	case !a.isPrerelease() && b.isPrerelease():
		return 1
	case a.isPrerelease() && !b.isPrerelease():
		return -1
	case !a.isPrerelease() && !b.isPrerelease():
		return 0
	}

	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		if c := compareIdentifier(a.pre[i], b.pre[i]); c != 0 {
			return c
		}
	}

	// All shared identifiers equal: the longer set wins, since it is more
	// specific — rc.1 precedes rc.1.2.
	switch {
	case len(a.pre) > len(b.pre):
		return 1
	case len(a.pre) < len(b.pre):
		return -1
	}
	return 0
}

// compareIdentifier orders one pre-release identifier against another.
//
// Numeric identifiers rank below alphanumeric ones, per SemVer. Everything
// else is compared in natural order: digit runs numerically, the rest
// bytewise.
func compareIdentifier(a, b string) int {
	an, aErr := strconv.Atoi(a)
	bn, bErr := strconv.Atoi(b)
	aNum, bNum := aErr == nil, bErr == nil

	switch {
	case aNum && bNum:
		return cmpInt(an, bn)
	case aNum:
		return -1
	case bNum:
		return 1
	}
	return naturalCompare(a, b)
}

// naturalCompare orders two strings treating embedded digit runs as numbers.
func naturalCompare(a, b string) int {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if isDigit(a[i]) && isDigit(b[j]) {
			ai, aEnd := digitRun(a, i)
			bi, bEnd := digitRun(b, j)
			if c := cmpInt(ai, bi); c != 0 {
				return c
			}
			i, j = aEnd, bEnd
			continue
		}
		if a[i] != b[j] {
			return cmpInt(int(a[i]), int(b[j]))
		}
		i++
		j++
	}
	return cmpInt(len(a)-i, len(b)-j)
}

func digitRun(s string, i int) (val, end int) {
	for end = i; end < len(s) && isDigit(s[end]); end++ {
		val = val*10 + int(s[end]-'0')
	}
	return val, end
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func cmpInt(a, b int) int {
	switch {
	case a > b:
		return 1
	case a < b:
		return -1
	}
	return 0
}
