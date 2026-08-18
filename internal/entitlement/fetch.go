package entitlement

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/wire"
)

// Refresh asks the server for the account's tier and updates the cached
// entitlement in global config. Only a 200 moves VerifiedAt; a 401 clears
// the cache (token revoked); any other failure leaves the cache untouched so
// the grace period keeps working offline (contracts/entitlement-api.md).
func Refresh(client *http.Client, timeout time.Duration) (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if !cfg.SignedIn() {
		return cfg, fmt.Errorf("not signed in")
	}
	if client == nil {
		client = &http.Client{}
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	req, err := http.NewRequest(http.MethodGet, cfg.Server()+"/v1/entitlement", nil)
	if err != nil {
		return cfg, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	c := *client
	c.Timeout = timeout
	resp, err := c.Do(req)
	if err != nil {
		return cfg, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		var body wire.EntitlementResponse
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return cfg, fmt.Errorf("decode entitlement: %w", err)
		}
		if body.Tier != "free" && body.Tier != TierPro {
			return cfg, fmt.Errorf("unknown tier %q", body.Tier)
		}
		return config.Update(func(c *config.Config) error {
			c.Entitlement = &config.Entitlement{Tier: body.Tier, VerifiedAt: body.CheckedAt}
			return nil
		})
	case http.StatusUnauthorized:
		updated, uerr := config.Update(func(c *config.Config) error {
			c.Entitlement = nil
			return nil
		})
		if uerr != nil {
			return cfg, uerr
		}
		return updated, fmt.Errorf("token rejected: sign in again with `agent-brain login`")
	default:
		return cfg, fmt.Errorf("entitlement check failed (%d)", resp.StatusCode)
	}
}
