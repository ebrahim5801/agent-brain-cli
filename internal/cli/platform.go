package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/wire"
)

var platformHTTP = &http.Client{Timeout: 30 * time.Second}

// callPlatform posts JSON to the platform and decodes the response. A non-2xx
// status returns the contract's machine-readable error code alongside it.
func callPlatform(serverURL, path, token string, body, out any) (status int, errCode string, err error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, "", err
	}
	req, err := http.NewRequest(http.MethodPost, serverURL+path, bytes.NewReader(payload))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := platformHTTP.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out != nil && resp.StatusCode != http.StatusNoContent {
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				return resp.StatusCode, "", fmt.Errorf("decode platform response: %w", err)
			}
		}
		return resp.StatusCode, "", nil
	}
	var apiErr wire.ErrorResponse
	if decodeErr := json.NewDecoder(resp.Body).Decode(&apiErr); decodeErr != nil {
		return resp.StatusCode, "", fmt.Errorf("platform returned %d", resp.StatusCode)
	}
	return resp.StatusCode, apiErr.Error, nil
}
