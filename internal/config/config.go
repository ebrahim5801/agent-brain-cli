// Package config owns the collector's global configuration: the personal
// token, machine identity, project links, and consent records. It lives
// outside any repository (constitution: nothing secret in a repo) and is
// written atomically under a lock so daemon and CLI never corrupt it.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const DefaultServerURL = "https://brain.ohnopixel.com"

// TelemetryCategories is the fixed list a user consents to; shown verbatim
// by `agent-brain consent` and recorded in the consent record.
var TelemetryCategories = []string{"sessions", "durations", "tokens", "models", "event_counts"}

type Consent struct {
	GrantedAt  string   `json:"granted_at"`
	Categories []string `json:"categories"`
}

type Link struct {
	ProjectKey string `json:"project_key"`
	LinkedAt   string `json:"linked_at"`
	// MemoryShare records the per-project memory-sharing consent, separate from
	// telemetry consent (Constitution II). Absent = never granted. Pausing sets
	// PausedAt without clearing GrantedAt.
	MemoryShare *MemoryShare `json:"memory_share,omitempty"`
	// TeamMemory caches the server's team-memory capability for this org
	// project (display/gating hint only). Set at link time and on every
	// successful pull/contribute; cleared on a pull 403.
	TeamMemory bool `json:"team_memory,omitempty"`
	// OrgProject records that the link points at an organization project. Unlike
	// TeamMemory it is never cleared by a 403: the pull loop keeps asking the
	// server for org projects so access resumes automatically after a lapse or
	// re-add (FR-018) — the server stays the authority on every call.
	OrgProject bool `json:"org_project,omitempty"`
	// MemoryEndpoint is the per-link memory base URL for a self-hosted enterprise
	// org (016). Empty ⇒ memory routes to cfg.Server() (vendor cloud), the
	// universal default and the personal-project behavior. Set at link time from
	// LinkValidateResponse and refreshed from every pull response; telemetry never
	// consults it (Constitution II — the pipelines can point at different servers).
	MemoryEndpoint string `json:"memory_endpoint,omitempty"`
}

type MemoryShare struct {
	GrantedAt string `json:"granted_at,omitempty"`
	PausedAt  string `json:"paused_at,omitempty"`
}

// ShareGranted reports live-or-paused consent (a grant exists at all).
func (l Link) ShareGranted() bool {
	return l.MemoryShare != nil && l.MemoryShare.GrantedAt != ""
}

// SharePaused reports whether contribution is paused for this link.
func (l Link) SharePaused() bool {
	return l.MemoryShare != nil && l.MemoryShare.PausedAt != ""
}

// ShareLive reports whether entries distilled now become contribution-eligible:
// consent granted and not paused.
func (l Link) ShareLive() bool {
	return l.ShareGranted() && !l.SharePaused()
}

// LinkKey is the canonical map key for a project link: the attribution kind
// prefixes the identity so a directory path can never collide with a remote.
func LinkKey(kind, identity string) string {
	return kind + ":" + identity
}

type Consents struct {
	Telemetry *Consent `json:"telemetry,omitempty"`
	Memory    *Consent `json:"memory,omitempty"`
}

// Storage selects the local store backend (feature 018). Absent ⇒ Basic mode
// (SQLite at the default path). Backend "postgres" runs the same local store on
// a user-supplied PostgreSQL database at DSN. The DSN carries credentials and
// lives only in this global config (0600), never in a repository.
type Storage struct {
	Backend string `json:"backend"`
	DSN     string `json:"dsn"`
}

type Daemon struct {
	Installed bool `json:"installed"`
}

// UpdateConfig holds the self-update preferences (cli-self-update). Auto is the
// mode: "on" (default when empty) silently self-updates in the background,
// "notify" only prints that a newer version exists, "off" disables even the
// check. The remaining fields are the throttle/announce bookkeeping.
type UpdateConfig struct {
	Auto          string `json:"auto,omitempty"`
	LastCheck     string `json:"last_check,omitempty"`      // RFC3339 of the last server check
	CachedLatest  string `json:"cached_latest,omitempty"`   // last version seen from the server
	LastAutoApply string `json:"last_auto_apply,omitempty"` // version an auto-update installed but has not yet announced
}

// Mode returns the effective auto-update mode, defaulting to "on".
func (u *UpdateConfig) Mode() string {
	if u == nil || u.Auto == "" {
		return UpdateAuto
	}
	return u.Auto
}

const (
	UpdateAuto   = "on"
	UpdateNotify = "notify"
	UpdateOff    = "off"
)

// Entitlement caches the server's tier decision; the client only honors it
// within the grace window and never extends VerifiedAt locally.
type Entitlement struct {
	Tier       string `json:"tier"`
	VerifiedAt string `json:"verified_at"`
}

type Config struct {
	MachineID string `json:"machine_id,omitempty"`
	ServerURL string `json:"server_url,omitempty"`
	Token     string `json:"token,omitempty"`
	// MemoryToken is a memory-scoped credential used only for the memory
	// pipeline, so the account-wide Token is never sent to a self-hosted memory
	// endpoint. Empty for sessions predating it; MemoryAuthToken falls back to
	// Token, which the memory endpoints still accept.
	MemoryToken  string          `json:"memory_token,omitempty"`
	AccountEmail string          `json:"account_email,omitempty"`
	Consent      Consents        `json:"consent"`
	Links        map[string]Link `json:"links,omitempty"`
	// LinkDeclines records directories whose session-start link offer the user
	// declined (link key → declined-at, RFC3339). A declined directory is never
	// offered again; local collection continues, and a later manual or --auto
	// link clears the record.
	LinkDeclines map[string]string `json:"link_declines,omitempty"`
	Daemon       Daemon          `json:"daemon"`
	Entitlement  *Entitlement    `json:"entitlement,omitempty"`
	Storage      *Storage        `json:"storage,omitempty"`
	Update       *UpdateConfig   `json:"update,omitempty"`

	// MemoryPackBudgetTokens bounds the session-start memory pack; 0 means
	// the 2000-token default.
	MemoryPackBudgetTokens int `json:"memory_pack_budget_tokens,omitempty"`
}

func (c *Config) SignedIn() bool {
	return c.Token != ""
}

// MemoryAuthToken returns the credential the memory pipeline should present.
// It prefers the memory-scoped token and falls back to the account token for
// sessions that signed in before memory tokens existed (the memory endpoints
// still accept a full token).
func (c *Config) MemoryAuthToken() string {
	if c.MemoryToken != "" {
		return c.MemoryToken
	}
	return c.Token
}

func (c *Config) TelemetryConsented() bool {
	return c.Consent.Telemetry != nil && c.Consent.Telemetry.GrantedAt != ""
}

func (c *Config) MemoryConsented() bool {
	return c.Consent.Memory != nil && c.Consent.Memory.GrantedAt != ""
}

// Server resolves the platform base URL: env override, then config, then default.
func (c *Config) Server() string {
	if v := os.Getenv("AGENT_BRAIN_SERVER_URL"); v != "" {
		return v
	}
	if c.ServerURL != "" {
		return c.ServerURL
	}
	return DefaultServerURL
}

// Dir resolves the OS-appropriate config directory.
// AGENT_BRAIN_CONFIG_DIR overrides it (used by tests and quickstart).
func Dir() (string, error) {
	if v := os.Getenv("AGENT_BRAIN_CONFIG_DIR"); v != "" {
		return v, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "agent-brain"), nil
}

func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the config, returning an empty Config when none exists yet.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

// Save writes the config atomically (temp file + rename) with 0600 perms.
func Save(cfg *Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Update performs a locked read-modify-write so concurrent daemon and CLI
// mutations never lose each other's changes.
func Update(mutate func(*Config) error) (*Config, error) {
	unlock, err := lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	cfg, err := Load()
	if err != nil {
		return nil, err
	}
	if err := mutate(cfg); err != nil {
		return nil, err
	}
	if err := Save(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

const (
	lockRetry = 50 * time.Millisecond
	lockWait  = 5 * time.Second
	lockStale = 30 * time.Second
)

// lock acquires the config-dir lock file, stealing locks older than
// lockStale (a crashed process must not wedge the collector forever).
func lock() (func(), error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "config.lock")
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if fi, statErr := os.Stat(path); statErr == nil && time.Since(fi.ModTime()) > lockStale {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("config locked by another agent-brain process (%s)", path)
		}
		time.Sleep(lockRetry)
	}
}
