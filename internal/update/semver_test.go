package update

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"v1.0.0", "1.0.0", 0},
		{"1.2.3", "1.2.4", -1},
		{"1.2.4", "1.2.3", 1},
		{"1.3.0", "1.2.9", 1},
		{"2.0.0", "1.9.9", 1},
		{"1.0", "1.0.0", 0},
		{"1", "1.0.0", 0},
		{"v1.4.0-rc1", "1.4.0", 0},   // pre-release suffix ignored
		{"1.4.0+build5", "1.4.0", 0}, // build suffix ignored
		{"1.10.0", "1.9.0", 1},       // numeric, not lexical
		{"", "0.0.0", 0},
		{"1.0.1", "1.0.0", 1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
