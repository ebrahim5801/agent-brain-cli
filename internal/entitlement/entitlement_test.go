package entitlement

import (
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
)

var now = time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

func proConfig(verifiedAgo time.Duration) *config.Config {
	return &config.Config{
		Consent: config.Consents{Memory: &config.Consent{GrantedAt: "2026-07-01T00:00:00.000Z"}},
		Entitlement: &config.Entitlement{
			Tier:       "pro",
			VerifiedAt: now.Add(-verifiedAgo).Format(time.RFC3339),
		},
	}
}

func TestEntitled(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{"nil config", nil, false},
		{"no entitlement", &config.Config{}, false},
		{"free tier", &config.Config{Entitlement: &config.Entitlement{Tier: "free", VerifiedAt: now.Format(time.RFC3339)}}, false},
		{"pro fresh", proConfig(time.Hour), true},
		{"pro at grace edge", proConfig(GracePeriod), true},
		{"pro past grace", proConfig(GracePeriod + time.Minute), false},
		{"pro bad timestamp", &config.Config{Entitlement: &config.Entitlement{Tier: "pro", VerifiedAt: "not-a-time"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Entitled(tc.cfg, now); got != tc.want {
				t.Errorf("Entitled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPaused(t *testing.T) {
	if Paused(proConfig(time.Hour), now) {
		t.Error("fresh pro reported paused")
	}
	if !Paused(proConfig(8*24*time.Hour), now) {
		t.Error("stale pro not reported paused")
	}
	free := &config.Config{Entitlement: &config.Entitlement{Tier: "free", VerifiedAt: now.Format(time.RFC3339)}}
	if Paused(free, now) {
		t.Error("free tier reported paused (it was never entitled)")
	}
}

func TestMemoryActive(t *testing.T) {
	cfg := proConfig(time.Hour)
	if !MemoryActive(cfg, false, now) {
		t.Error("fully enabled state reported inactive")
	}
	if MemoryActive(cfg, true, now) {
		t.Error("disabled project reported active")
	}
	noConsent := proConfig(time.Hour)
	noConsent.Consent.Memory = nil
	if MemoryActive(noConsent, false, now) {
		t.Error("no consent reported active")
	}
	if MemoryActive(proConfig(30*24*time.Hour), false, now) {
		t.Error("stale entitlement reported active")
	}
}

func TestTeamMemoryActive(t *testing.T) {
	signedIn := &config.Config{Token: "tok"}
	teamLink := config.Link{ProjectKey: "pk", TeamMemory: true}

	if !TeamMemoryActive(signedIn, teamLink, false) {
		t.Error("linked team project with capability reported inactive")
	}
	// No Pro, no memory consent required — reading is a membership benefit.
	if !TeamMemoryActive(&config.Config{Token: "tok"}, teamLink, false) {
		t.Error("team memory should not require Pro or sharing consent")
	}
	if TeamMemoryActive(signedIn, teamLink, true) {
		t.Error("project-disabled team link reported active")
	}
	if TeamMemoryActive(signedIn, config.Link{ProjectKey: "pk"}, false) {
		t.Error("link without cached capability reported active")
	}
	if TeamMemoryActive(&config.Config{}, teamLink, false) {
		t.Error("signed-out config reported active")
	}
}

func TestReason(t *testing.T) {
	cases := []struct {
		name     string
		cfg      *config.Config
		disabled bool
		want     string
	}{
		{"active", proConfig(time.Hour), false, ""},
		{"no consent", &config.Config{Entitlement: &config.Entitlement{Tier: "pro", VerifiedAt: now.Format(time.RFC3339)}}, false, "not enabled"},
		{"paused", proConfig(30 * 24 * time.Hour), false, "paused (entitlement unverified > 7 days)"},
		{"free tier", func() *config.Config {
			c := proConfig(time.Hour)
			c.Entitlement.Tier = "free"
			return c
		}(), false, "a Pro feature"},
		{"project disabled", proConfig(time.Hour), true, "disabled for this project"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Reason(tc.cfg, tc.disabled, now); got != tc.want {
				t.Errorf("Reason = %q, want %q", got, tc.want)
			}
		})
	}
}
