package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func setDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AGENT_BRAIN_CONFIG_DIR", dir)
	return dir
}

func TestLoadMissingReturnsEmpty(t *testing.T) {
	setDir(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SignedIn() || cfg.TelemetryConsented() || len(cfg.Links) != 0 {
		t.Errorf("empty config not empty: %+v", cfg)
	}
}

func TestSaveIsAtomicAndPrivate(t *testing.T) {
	dir := setDir(t)
	cfg := &Config{MachineID: "m-1", Token: "ab_pat_x"}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("config perms = %v, want 0600", fi.Mode().Perm())
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed Config
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("saved config is not valid JSON: %v", err)
	}
	if parsed.Token != "ab_pat_x" {
		t.Errorf("token = %q", parsed.Token)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "config.json" {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestUpdatePreservesMachineID(t *testing.T) {
	setDir(t)
	if _, err := Update(func(c *Config) error { c.MachineID = "m-stable"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(func(c *Config) error { c.Token = "ab_pat_y"; return nil }); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MachineID != "m-stable" || cfg.Token != "ab_pat_y" {
		t.Errorf("config = %+v", cfg)
	}
}

func TestConcurrentUpdatesBothApply(t *testing.T) {
	setDir(t)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		key := []string{"remote:a/one", "remote:a/two"}[i]
		go func() {
			defer wg.Done()
			_, err := Update(func(c *Config) error {
				if c.Links == nil {
					c.Links = map[string]Link{}
				}
				c.Links[key] = Link{ProjectKey: "ab_proj_" + key, LinkedAt: "2026-07-02T00:00:00.000Z"}
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Links) != 2 {
		t.Errorf("links = %d, want 2 (lost update)", len(cfg.Links))
	}
}

func TestMemoryFieldsRoundTrip(t *testing.T) {
	setDir(t)
	if _, err := Update(func(c *Config) error {
		c.Consent.Memory = &Consent{GrantedAt: "2026-07-02T00:00:00.000Z"}
		c.Entitlement = &Entitlement{Tier: "pro", VerifiedAt: "2026-07-02T00:00:00.000Z"}
		c.MemoryPackBudgetTokens = 3000
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MemoryConsented() {
		t.Error("memory consent lost in round-trip")
	}
	if cfg.Entitlement == nil || cfg.Entitlement.Tier != "pro" {
		t.Errorf("entitlement = %+v", cfg.Entitlement)
	}
	if cfg.MemoryPackBudgetTokens != 3000 {
		t.Errorf("budget = %d", cfg.MemoryPackBudgetTokens)
	}
	if cfg.TelemetryConsented() {
		t.Error("memory consent leaked into telemetry consent")
	}
}

func TestMemoryCaptureRoundTripAndDefaults(t *testing.T) {
	setDir(t)
	// Absent block: both triggers use their defaults.
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.MemoryEvery(); got != DefaultMemoryEvery {
		t.Errorf("absent every = %d, want %d", got, DefaultMemoryEvery)
	}
	if got := cfg.MemoryCheckpoint(25); got != 25 {
		t.Errorf("absent checkpoint = %d, want 25", got)
	}

	// Explicit values, including 0 (off), survive a round trip and are not
	// confused with absent.
	zero, seven := 0, 7
	if _, err := Update(func(c *Config) error {
		c.MemoryCapture = &MemoryCaptureConfig{Every: &zero, Checkpoint: &seven}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.MemoryEvery(); got != 0 {
		t.Errorf("stored every 0 = %d, want 0 (off)", got)
	}
	if got := cfg.MemoryCheckpoint(25); got != 7 {
		t.Errorf("stored checkpoint = %d, want 7", got)
	}

	// A negative stored value reads as unset.
	neg := -3
	cfg.MemoryCapture.Every = &neg
	if got := cfg.MemoryEvery(); got != DefaultMemoryEvery {
		t.Errorf("negative every = %d, want default %d", got, DefaultMemoryEvery)
	}
}

func TestLinkMemoryEndpointRoundTrip(t *testing.T) {
	setDir(t)
	if _, err := Update(func(c *Config) error {
		c.Links = map[string]Link{
			"remote:gitlab.com/acme/api": {ProjectKey: "k", OrgProject: true, MemoryEndpoint: "https://brain.acme.internal"},
			"remote:gitlab.com/acme/web": {ProjectKey: "k2", OrgProject: true},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Links["remote:gitlab.com/acme/api"].MemoryEndpoint; got != "https://brain.acme.internal" {
		t.Errorf("endpoint = %q, want the self-hosted URL", got)
	}
	// An omitted endpoint stays empty (routes to the vendor cloud) and does not
	// serialize a key.
	if got := cfg.Links["remote:gitlab.com/acme/web"].MemoryEndpoint; got != "" {
		t.Errorf("absent endpoint = %q, want empty", got)
	}
}

func TestServerResolution(t *testing.T) {
	setDir(t)
	cfg := &Config{}
	if got := cfg.Server(); got != DefaultServerURL {
		t.Errorf("default server = %q", got)
	}
	cfg.ServerURL = "https://self-hosted.example"
	if got := cfg.Server(); got != "https://self-hosted.example" {
		t.Errorf("config server = %q", got)
	}
	t.Setenv("AGENT_BRAIN_SERVER_URL", "http://localhost:8080")
	if got := cfg.Server(); got != "http://localhost:8080" {
		t.Errorf("env server = %q", got)
	}
}

func TestUpdateModeDefault(t *testing.T) {
	var nilCfg *UpdateConfig
	if got := nilCfg.Mode(); got != UpdateAuto {
		t.Errorf("nil mode = %q, want %q", got, UpdateAuto)
	}
	if got := (&UpdateConfig{}).Mode(); got != UpdateAuto {
		t.Errorf("empty mode = %q, want %q", got, UpdateAuto)
	}
	if got := (&UpdateConfig{Auto: UpdateOff}).Mode(); got != UpdateOff {
		t.Errorf("off mode = %q, want %q", got, UpdateOff)
	}
}

func TestUpdateURL(t *testing.T) {
	saved := DefaultUpdateURL
	t.Cleanup(func() { DefaultUpdateURL = saved })
	c := &Config{}

	DefaultUpdateURL = ""
	t.Setenv("AGENT_BRAIN_UPDATE_URL", "")
	if got := c.UpdateURL(); got != "" {
		t.Fatalf("source build: UpdateURL = %q, want empty (cloud server)", got)
	}

	DefaultUpdateURL = "https://releases.example/"
	if got := c.UpdateURL(); got != "https://releases.example" {
		t.Fatalf("build default: UpdateURL = %q", got)
	}

	t.Setenv("AGENT_BRAIN_UPDATE_URL", "https://mirror.example")
	if got := c.UpdateURL(); got != "https://mirror.example" {
		t.Fatalf("env override: UpdateURL = %q", got)
	}

	// A plain-http override is ignored: updates replace the binary.
	t.Setenv("AGENT_BRAIN_UPDATE_URL", "http://mirror.example")
	if got := c.UpdateURL(); got != "https://releases.example" {
		t.Fatalf("http override: UpdateURL = %q, want the build default", got)
	}
}
