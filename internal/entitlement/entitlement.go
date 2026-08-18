// Package entitlement decides whether memory capture and serving are active
// on this machine. The tier itself is decided server-side and cached in
// global config; this package only honors that cached decision within a
// bounded grace window (constitution: tier limits enforced server-side —
// the client never invents a tier, and never extends VerifiedAt locally).
package entitlement

import (
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
)

// GracePeriod is how long a verified Pro entitlement keeps working offline.
const GracePeriod = 7 * 24 * time.Hour

const TierPro = "pro"

// Entitled reports whether the cached entitlement is Pro and recently
// verified. A missing or unparsable cache is never entitled.
func Entitled(cfg *config.Config, now time.Time) bool {
	if cfg == nil || cfg.Entitlement == nil || cfg.Entitlement.Tier != TierPro {
		return false
	}
	verified, err := time.Parse(time.RFC3339, cfg.Entitlement.VerifiedAt)
	if err != nil {
		return false
	}
	return now.Sub(verified) <= GracePeriod
}

// Paused reports the specific "was Pro, verification went stale" state, used
// for messaging (paused vs never-enabled).
func Paused(cfg *config.Config, now time.Time) bool {
	if cfg == nil || cfg.Entitlement == nil || cfg.Entitlement.Tier != TierPro {
		return false
	}
	return !Entitled(cfg, now)
}

// MemoryActive is the single gate evaluated before every capture and serve:
// server-verified Pro within grace, memory consent granted, project not
// disabled.
func MemoryActive(cfg *config.Config, projectDisabled bool, now time.Time) bool {
	return Entitled(cfg, now) && cfg.MemoryConsented() && !projectDisabled
}

// TeamMemoryActive gates memory on an org-linked project. Unlike personal
// memory it requires neither Pro nor sharing consent: reading team memory is a
// membership benefit (the org paid per seat), and the memory_share grant gates
// contribution exclusively (contracts/memory-serving.md §Gating). The cached
// capability is a UX hint only — the server re-authorizes every network call
// and a 403 clears it.
func TeamMemoryActive(cfg *config.Config, link config.Link, projectDisabled bool) bool {
	return cfg != nil && cfg.SignedIn() && link.ProjectKey != "" && link.TeamMemory && !projectDisabled
}

// Reason explains why memory is inactive, in the vocabulary of the MCP
// gating contract; empty when active.
func Reason(cfg *config.Config, projectDisabled bool, now time.Time) string {
	switch {
	case cfg == nil || !cfg.MemoryConsented():
		return "not enabled"
	case Paused(cfg, now):
		return "paused (entitlement unverified > 7 days)"
	case !Entitled(cfg, now):
		return "a Pro feature"
	case projectDisabled:
		return "disabled for this project"
	default:
		return ""
	}
}
