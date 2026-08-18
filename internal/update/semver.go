package update

import (
	"strconv"
	"strings"
)

// compareVersions compares two major.minor.patch versions, returning -1 if a<b,
// 0 if equal, 1 if a>b. A leading "v" is stripped and any pre-release/build
// suffix (after the first '-' or '+') is ignored — enough for our own tags, and
// deliberately no third-party semver dependency (constitution). Missing or
// non-numeric components are treated as 0.
func compareVersions(a, b string) int {
	pa := parseVersion(a)
	pb := parseVersion(b)
	for i := 0; i < 3; i++ {
		switch {
		case pa[i] < pb[i]:
			return -1
		case pa[i] > pb[i]:
			return 1
		}
	}
	return 0
}

func parseVersion(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out [3]int
	for i, part := range strings.SplitN(v, ".", 3) {
		if i > 2 {
			break
		}
		out[i], _ = strconv.Atoi(part)
	}
	return out
}
