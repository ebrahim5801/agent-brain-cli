// Package redact scrubs recognized secrets from memory content before it is
// persisted (constitution II: redaction happens client-side, before storage).
// It is pattern-based: precise token shapes first, then credential-shaped
// assignments, then long opaque runs in credential contexts. Matches are
// replaced with [redacted:<kind>].
package redact

import (
	"fmt"
	"regexp"
)

type rule struct {
	kind string
	re   *regexp.Regexp
	// group is the submatch index to replace; 0 replaces the whole match.
	group int
}

var rules = []rule{
	{kind: "private-key", re: regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(-----END [A-Z ]*PRIVATE KEY-----|\z)`)},
	{kind: "aws-key", re: regexp.MustCompile(`\b(?:AKIA|ASIA|ABIA|ACCA)[0-9A-Z]{16}\b`)},
	{kind: "github-token", re: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`)},
	{kind: "gitlab-token", re: regexp.MustCompile(`\bglpat-[A-Za-z0-9_\-]{20,}\b`)},
	{kind: "slack-token", re: regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`)},
	{kind: "stripe-key", re: regexp.MustCompile(`\b[sr]k_(?:live|test)_[A-Za-z0-9]{16,}\b`)},
	{kind: "google-key", re: regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`)},
	// Anthropic must precede the OpenAI rule: sk-ant-… also satisfies the
	// broader sk-… shape, and whichever runs first consumes the match.
	{kind: "anthropic-key", re: regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{20,}`)},
	{kind: "openai-key", re: regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_\-]{20,}`)},
	{kind: "npm-token", re: regexp.MustCompile(`\bnpm_[A-Za-z0-9]{30,}\b`)},
	{kind: "huggingface-token", re: regexp.MustCompile(`\bhf_[A-Za-z0-9]{30,}\b`)},
	{kind: "sendgrid-key", re: regexp.MustCompile(`\bSG\.[A-Za-z0-9_\-]{16,}\.[A-Za-z0-9_\-]{16,}`)},
	{kind: "mailgun-key", re: regexp.MustCompile(`\bkey-[0-9a-f]{32}\b`)},
	{kind: "jwt", re: regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\b`)},
	{kind: "bearer", re: regexp.MustCompile(`(?i)\b(?:bearer|authorization:\s*bearer)\s+([A-Za-z0-9_\-.~+/=]{16,})`), group: 1},
	{kind: "credential", re: regexp.MustCompile(`(?i)\b(password|passwd|secret|api[_-]?key|access[_-]?key|auth[_-]?token|client[_-]?secret|token)\b\s*[:=]+\s*["']?([^\s"']{8,})["']?`), group: 2},
	{kind: "url-credential", re: regexp.MustCompile(`\b([a-z][a-z0-9+.-]*://[^/\s:@]+):([^@\s]{4,})@`), group: 2},
	// Long opaque runs only in credential-shaped contexts, to keep prose and
	// commit hashes intact: 40-hex (git SHAs) are deliberately not matched.
	{kind: "opaque", re: regexp.MustCompile(`(?i)\b(?:key|token|secret|credential)\S*\s+(?:is|was|=)?\s*([A-Za-z0-9+/=_\-]{32,})\b`), group: 1},
}

// Apply replaces recognized secrets in s with [redacted:<kind>] markers.
func Apply(s string) string {
	for _, r := range rules {
		marker := fmt.Sprintf("[redacted:%s]", r.kind)
		if r.group == 0 {
			s = r.re.ReplaceAllString(s, marker)
			continue
		}
		s = r.re.ReplaceAllStringFunc(s, func(m string) string {
			idx := r.re.FindStringSubmatchIndex(m)
			g := 2 * r.group
			if idx == nil || len(idx) <= g+1 || idx[g] < 0 {
				return m
			}
			return m[:idx[g]] + marker + m[idx[g+1]:]
		})
	}
	return s
}
