package wire

import (
	"strings"
	"testing"
)

func TestValidAgentType(t *testing.T) {
	valid := []string{"general-purpose", "Security Engineer", "plugin:skill", "v2.1_beta", strings.Repeat("a", MaxAgentTypeLen)}
	for _, s := range valid {
		if !ValidAgentType(s) {
			t.Errorf("ValidAgentType(%q) = false, want true", s)
		}
	}
	invalid := []string{"", strings.Repeat("a", MaxAgentTypeLen+1), "a\x1b[2Jb", "a\nb", "<script>", "路径", "a/b"}
	for _, s := range invalid {
		if ValidAgentType(s) {
			t.Errorf("ValidAgentType(%q) = true, want false", s)
		}
	}
}

func TestSanitizeAgentType(t *testing.T) {
	cases := []struct{ in, want string }{
		{"general-purpose", "general-purpose"},
		{"a\x1b[2Jb", "a2Jb"},
		{"<Security> Engineer\n", "Security Engineer"},
		{strings.Repeat("x", 200), strings.Repeat("x", MaxAgentTypeLen)},
		{"路径", ""},
	}
	for _, c := range cases {
		if got := SanitizeAgentType(c.in); got != c.want {
			t.Errorf("SanitizeAgentType(%q) = %q, want %q", c.in, got, c.want)
		}
		if got := SanitizeAgentType(c.in); got != "" && !ValidAgentType(got) {
			t.Errorf("SanitizeAgentType(%q) = %q is not valid", c.in, got)
		}
	}
}
