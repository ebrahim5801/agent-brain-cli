package entitlement

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
)

func seedConfig(t *testing.T, serverURL string, ent *config.Entitlement) {
	t.Helper()
	t.Setenv("AGENT_BRAIN_CONFIG_DIR", t.TempDir())
	t.Setenv("AGENT_BRAIN_SERVER_URL", serverURL)
	if err := config.Save(&config.Config{Token: "ab_pat_x", Entitlement: ent}); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshUpdatesCacheOn200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/entitlement" || r.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer ab_pat_x" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tier":"pro","checked_at":"2026-07-02T12:00:00Z"}`))
	}))
	defer srv.Close()
	seedConfig(t, srv.URL, nil)

	cfg, err := Refresh(nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Entitlement == nil || cfg.Entitlement.Tier != "pro" || cfg.Entitlement.VerifiedAt != "2026-07-02T12:00:00Z" {
		t.Errorf("entitlement = %+v", cfg.Entitlement)
	}
}

func TestRefreshClearsCacheOn401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	seedConfig(t, srv.URL, &config.Entitlement{Tier: "pro", VerifiedAt: time.Now().Format(time.RFC3339)})

	_, err := Refresh(nil, time.Second)
	if err == nil || !strings.Contains(err.Error(), "token rejected") {
		t.Fatalf("err = %v", err)
	}
	cfg, _ := config.Load()
	if cfg.Entitlement != nil {
		t.Errorf("401 left cache in place: %+v", cfg.Entitlement)
	}
}

func TestRefreshPreservesCacheOnServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	cached := &config.Entitlement{Tier: "pro", VerifiedAt: "2026-07-01T00:00:00Z"}
	seedConfig(t, srv.URL, cached)

	if _, err := Refresh(nil, time.Second); err == nil {
		t.Fatal("want error on 500")
	}
	cfg, _ := config.Load()
	if cfg.Entitlement == nil || cfg.Entitlement.VerifiedAt != cached.VerifiedAt {
		t.Errorf("5xx mutated cache: %+v", cfg.Entitlement)
	}
}

func TestRefreshPreservesCacheOnNetworkFailure(t *testing.T) {
	seedConfig(t, "http://127.0.0.1:1", &config.Entitlement{Tier: "pro", VerifiedAt: "2026-07-01T00:00:00Z"})
	if _, err := Refresh(nil, 200*time.Millisecond); err == nil {
		t.Fatal("want error when unreachable")
	}
	cfg, _ := config.Load()
	if cfg.Entitlement == nil || cfg.Entitlement.Tier != "pro" {
		t.Errorf("network failure mutated cache: %+v", cfg.Entitlement)
	}
}

func TestRefreshRejectsUnknownTier(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tier":"platinum","checked_at":"2026-07-02T12:00:00Z"}`))
	}))
	defer srv.Close()
	seedConfig(t, srv.URL, nil)
	if _, err := Refresh(nil, time.Second); err == nil {
		t.Fatal("unknown tier accepted")
	}
	cfg, _ := config.Load()
	if cfg.Entitlement != nil {
		t.Error("unknown tier cached")
	}
}

func TestRefreshRequiresSignIn(t *testing.T) {
	t.Setenv("AGENT_BRAIN_CONFIG_DIR", t.TempDir())
	if err := config.Save(&config.Config{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Refresh(nil, time.Second); err == nil || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("err = %v", err)
	}
}
