package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
)

func setLinkOfferDir(t *testing.T) {
	t.Helper()
	t.Setenv("AGENT_BRAIN_CONFIG_DIR", t.TempDir())
}

func signedInConfig(t *testing.T) {
	t.Helper()
	if _, err := config.Update(func(c *config.Config) error {
		c.Token = "ab_pat_test"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// runConfigLinkOffer executes `agent-brain config link-offer <args>` through the
// real command tree and returns its stdout and error.
func runConfigLinkOffer(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newConfigCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"link-offer"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestLinkOfferOffByDefault(t *testing.T) {
	setLinkOfferDir(t)
	signedInConfig(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LinkOfferEnabled() {
		t.Fatal("link offer must be off by default")
	}
	if note := linkOfferNote(nil, t.TempDir()); note != "" {
		t.Errorf("signed-in unlinked project got a note while the offer is off: %q", note)
	}
}

func TestLinkOfferOnInjectsNote(t *testing.T) {
	setLinkOfferDir(t)
	signedInConfig(t)
	if _, err := runConfigLinkOffer(t, "on"); err != nil {
		t.Fatal(err)
	}
	note := linkOfferNote(nil, t.TempDir())
	if !strings.Contains(note, "## agent-brain: project not linked") ||
		!strings.Contains(note, "Use agent-brain cloud for this project?") {
		t.Errorf("offer on should inject the existing note, got %q", note)
	}
}

func TestLinkOfferNoteRequiresSignIn(t *testing.T) {
	setLinkOfferDir(t)
	if _, err := runConfigLinkOffer(t, "on"); err != nil {
		t.Fatal(err)
	}
	if note := linkOfferNote(nil, t.TempDir()); note != "" {
		t.Errorf("signed-out project got a note: %q", note)
	}
}

func TestConfigLinkOfferPersists(t *testing.T) {
	setLinkOfferDir(t)
	out, err := runConfigLinkOffer(t, "on")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Link offer on") {
		t.Errorf("confirmation missing for on: %q", out)
	}
	cfg, _ := config.Load()
	if !cfg.LinkOfferEnabled() {
		t.Fatal("link-offer on did not persist")
	}

	out, err = runConfigLinkOffer(t, "off")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Link offer off") {
		t.Errorf("confirmation missing for off: %q", out)
	}
	cfg, _ = config.Load()
	if cfg.LinkOfferEnabled() {
		t.Fatal("link-offer off did not persist")
	}
	if cfg.LinkOffer == nil {
		t.Fatal("link-offer off should save an explicit false, not clear the field")
	}
}

func TestConfigLinkOfferShowsCurrentValue(t *testing.T) {
	setLinkOfferDir(t)
	out, err := runConfigLinkOffer(t)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "Link offer: off" {
		t.Errorf("default display = %q, want \"Link offer: off\"", out)
	}
	if _, err := runConfigLinkOffer(t, "on"); err != nil {
		t.Fatal(err)
	}
	out, err = runConfigLinkOffer(t)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "Link offer: on" {
		t.Errorf("after on, display = %q, want \"Link offer: on\"", out)
	}
}

func TestConfigLinkOfferRejectsInvalidValue(t *testing.T) {
	setLinkOfferDir(t)
	for _, v := range []string{"yes", "true", "ON", "1"} {
		if _, err := runConfigLinkOffer(t, v); err == nil {
			t.Errorf("link-offer %q was accepted", v)
		}
	}
	cfg, _ := config.Load()
	if cfg.LinkOffer != nil {
		t.Error("invalid value must not write the setting")
	}
}

func TestConfigLinkOfferRejectsExtraArgs(t *testing.T) {
	setLinkOfferDir(t)
	if _, err := runConfigLinkOffer(t, "on", "off"); err == nil {
		t.Error("two arguments were accepted")
	}
	cfg, _ := config.Load()
	if cfg.LinkOffer != nil {
		t.Error("rejected arguments must not write the setting")
	}
}

func TestLinkHasNoOfferFlag(t *testing.T) {
	setLinkOfferDir(t)
	cmd := newLinkCmd()
	cmd.SetArgs([]string{"--offer", "on"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("link accepted --offer; the setting belongs to config link-offer")
	}
	cfg, _ := config.Load()
	if cfg.LinkOffer != nil {
		t.Error("rejected flag must not write the setting")
	}
}
