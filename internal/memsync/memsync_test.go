package memsync

import (
	"errors"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
)

func TestMemoryBaseURLEmptyFallsBackToVendor(t *testing.T) {
	cfg := &config.Config{ServerURL: "https://cloud.example"}
	got, err := MemoryBaseURL(cfg, config.Link{ProjectKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://cloud.example" {
		t.Errorf("empty endpoint base = %q, want the vendor cloud", got)
	}
}

func TestMemoryBaseURLUsesPerLinkEndpoint(t *testing.T) {
	cfg := &config.Config{ServerURL: "https://cloud.example"}
	got, err := MemoryBaseURL(cfg, config.Link{MemoryEndpoint: "https://brain.acme.internal/"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://brain.acme.internal" {
		t.Errorf("base = %q, want the trimmed self-hosted URL", got)
	}
}

func TestMemoryBaseURLRefusesNonHTTPS(t *testing.T) {
	cfg := &config.Config{ServerURL: "https://cloud.example"}
	for _, ep := range []string{"http://brain.acme.internal", "ftp://x", "brain.acme.internal", "://bad"} {
		if _, err := MemoryBaseURL(cfg, config.Link{MemoryEndpoint: ep}); !errors.Is(err, ErrEndpointInsecure) {
			t.Errorf("endpoint %q err = %v, want ErrEndpointInsecure", ep, err)
		}
	}
}

// The routing decision is per link: within one config, an org link with a
// self-hosted endpoint and a personal link with none resolve to different bases —
// the mixed-machine guarantee (FR-004/FR-005).
func TestMemoryBaseURLIsPerLink(t *testing.T) {
	cfg := &config.Config{ServerURL: "https://cloud.example"}
	orgBase, err := MemoryBaseURL(cfg, config.Link{MemoryEndpoint: "https://brain.acme.internal"})
	if err != nil {
		t.Fatal(err)
	}
	personalBase, err := MemoryBaseURL(cfg, config.Link{})
	if err != nil {
		t.Fatal(err)
	}
	if orgBase == personalBase {
		t.Fatal("org and personal links resolved to the same base; routing is not per-link")
	}
	if orgBase != "https://brain.acme.internal" || personalBase != "https://cloud.example" {
		t.Errorf("org=%q personal=%q", orgBase, personalBase)
	}
}
