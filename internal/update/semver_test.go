package update

import (
	"testing"
)

func TestParseVersionAcceptsWhatReleasesLookLike(t *testing.T) {
	cases := []struct {
		in   string
		want Version
	}{
		{"1.0.0", Version{Major: 1}},
		{"v1.0.0", Version{Major: 1}},
		{" 1.2.3 ", Version{Major: 1, Minor: 2, Patch: 3}},
		{"0.0.1", Version{Patch: 1}},
		{"10.20.30", Version{Major: 10, Minor: 20, Patch: 30}},
		{"1.0.0-rc.1", Version{Major: 1, Pre: "rc.1"}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseVersion(tc.in)
			if err != nil {
				t.Fatalf("ParseVersion(%q) = %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseVersion(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseVersionRejectsWhatWouldMislead(t *testing.T) {
	// Every one of these would otherwise end up compared against a real
	// version, and a wrong answer here replaces someone's binaries.
	for _, in := range []string{
		"", "dev", "1.0", "1.0.0.0", "1.x.0", "-1.0.0", "1.0.0-", "latest", "v",
	} {
		t.Run(in, func(t *testing.T) {
			if v, err := ParseVersion(in); err == nil {
				t.Errorf("ParseVersion(%q) = %+v, want an error", in, v)
			}
		})
	}
}

func TestCompareOrdersVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.0.1", -1},
		{"1.1.0", "1.0.9", 1},
		{"2.0.0", "1.99.99", 1},
		// Ten is not "less than" nine, which is the whole reason this is not a
		// string comparison.
		{"1.10.0", "1.9.0", 1},
		{"1.0.10", "1.0.9", 1},
		// A prerelease leads to its release, so it sorts below it.
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0", "1.0.0-rc.1", 1},
		{"1.0.0-rc.2", "1.0.0-rc.1", 1},
		{"1.0.0-rc.1", "1.0.0-rc.1", 0},
	}
	for _, tc := range cases {
		t.Run(tc.a+" vs "+tc.b, func(t *testing.T) {
			a, err := ParseVersion(tc.a)
			if err != nil {
				t.Fatal(err)
			}
			b, err := ParseVersion(tc.b)
			if err != nil {
				t.Fatal(err)
			}
			if got := Compare(a, b); got != tc.want {
				t.Errorf("Compare(%s, %s) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
			if got := Compare(b, a); got != -tc.want {
				t.Errorf("Compare(%s, %s) = %d, want %d", tc.b, tc.a, got, -tc.want)
			}
		})
	}
}

func TestNewerRefusesToGuessAboutAnUnstampedBuild(t *testing.T) {
	if Newer(Dev, "9.9.9") {
		t.Error("a dev build was offered an upgrade; it must never be")
	}
	if Newer("not a version", "9.9.9") {
		t.Error("an unparseable current version was offered an upgrade")
	}
	if Newer("1.0.0", "latest") {
		t.Error("an unparseable candidate was offered")
	}
	if !Newer("1.0.0", "1.0.1") {
		t.Error("1.0.1 should be offered over 1.0.0")
	}
	if Newer("1.0.1", "1.0.0") {
		t.Error("a downgrade was offered")
	}
	if Newer("1.0.0", "1.0.0") {
		t.Error("the running version was offered as an upgrade")
	}
}

func TestStringRoundTrips(t *testing.T) {
	for _, in := range []string{"1.0.0", "0.1.2", "10.20.30", "1.0.0-rc.1"} {
		v, err := ParseVersion(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := v.String(); got != in {
			t.Errorf("String() = %q, want %q", got, in)
		}
	}
}
