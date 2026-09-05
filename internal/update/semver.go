// Package update keeps an installed copy current: it asks GitHub what the
// latest release is, stages the installer, checks it against the published
// checksums, and — only when someone asks — runs it.
//
// It deliberately does not apply anything on its own. A monitor that restarts
// itself unannounced is a monitor with a gap in its history that nobody
// ordered, so the last step is always a button.
package update

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a release version: the three numbers, plus an optional prerelease
// tag. Build metadata is not accepted — nothing here publishes it, and quietly
// ignoring a suffix is how two different builds come to look like one version.
type Version struct {
	Major, Minor, Patch int

	// Pre is the prerelease tag without its leading hyphen, or "" for a
	// release. A prerelease sorts *below* the release it leads to.
	Pre string
}

// Dev is the version an unstamped build reports. It parses as nothing, which is
// what stops a developer's build from ever being told it is out of date.
const Dev = "dev"

// ParseVersion reads "1.2.3" or "1.2.3-rc.1", with or without a leading v.
func ParseVersion(s string) (Version, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return Version{}, fmt.Errorf("empty version")
	}

	var pre string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s, pre = s[:i], s[i+1:]
		if pre == "" {
			return Version{}, fmt.Errorf("empty prerelease tag")
		}
	}

	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("want major.minor.patch, got %q", s)
	}
	var v Version
	for i, dst := range []*int{&v.Major, &v.Minor, &v.Patch} {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("%q is not a version number", parts[i])
		}
		*dst = n
	}
	v.Pre = pre
	return v, nil
}

// String renders the version back, without the v.
func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Compare orders two versions: -1 if a is older, 0 if they are the same, 1 if a
// is newer.
//
// Prerelease ordering follows semver only as far as this project needs it:
// 1.0.0-rc.1 is older than 1.0.0, and two prereleases of the same version are
// compared as strings. Nothing here publishes prereleases today — /releases/
// latest excludes them — so this exists to be correct if that changes rather
// than to be exercised now.
func Compare(a, b Version) int {
	for _, p := range [][2]int{
		{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch},
	} {
		if p[0] != p[1] {
			return sign(p[0] - p[1])
		}
	}
	switch {
	case a.Pre == b.Pre:
		return 0
	case a.Pre == "":
		return 1
	case b.Pre == "":
		return -1
	}
	return sign(strings.Compare(a.Pre, b.Pre))
}

// Newer reports whether candidate is a version worth offering over current.
//
// An unparseable current version — "dev", or a build somebody stamped by hand —
// means no. Offering an upgrade to someone running a build this cannot reason
// about would be a guess, and the thing being guessed at replaces their
// binaries.
func Newer(current, candidate string) bool {
	cur, err := ParseVersion(current)
	if err != nil {
		return false
	}
	cand, err := ParseVersion(candidate)
	if err != nil {
		return false
	}
	return Compare(cand, cur) > 0
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
