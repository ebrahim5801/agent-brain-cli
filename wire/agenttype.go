package wire

// MaxAgentTypeLen bounds the agent_type wire field. The value is a subagent
// type slug; anything longer is not a slug and must not ride the one
// content-free field the sub-session schema added.
const MaxAgentTypeLen = 64

func agentTypeRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == ' ', r == '_', r == '.', r == ':', r == '-':
		return true
	}
	return false
}

// ValidAgentType reports whether s is an acceptable agent type slug:
// 1..MaxAgentTypeLen bytes of letters, digits, space, '_', '.', ':', '-'.
func ValidAgentType(s string) bool {
	if len(s) == 0 || len(s) > MaxAgentTypeLen {
		return false
	}
	for _, r := range s {
		if !agentTypeRune(r) {
			return false
		}
	}
	return true
}

// SanitizeAgentType reduces an untrusted agent type (it originates in a
// repo-controlled meta.json) to slug form: disallowed runes are dropped and
// the result is capped at MaxAgentTypeLen bytes on a rune boundary.
func SanitizeAgentType(s string) string {
	out := make([]byte, 0, min(len(s), MaxAgentTypeLen))
	for _, r := range s {
		if !agentTypeRune(r) {
			continue
		}
		if len(out)+1 > MaxAgentTypeLen {
			break
		}
		out = append(out, byte(r))
	}
	return string(out)
}
