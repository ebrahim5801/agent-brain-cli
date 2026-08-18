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
