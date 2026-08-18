// Package httpauth is the shared transport for the client's authorized JSON
// requests to the platform. The telemetry and team-memory pipelines never
// share a request, a wire type, or a consent gate (Constitution II) — they
// share only this retry discipline, so transport fixes (backoff, Retry-After
// handling, error classification) land on both at once.
package httpauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/wire"
)

// ErrReauth surfaces a 401: the engine pauses and keeps its queue untouched.
var ErrReauth = errors.New("re-authentication required — run `agent-brain login`")

// TransportError marks failures where the platform was never reached.
type TransportError struct{ Err error }

func (t *TransportError) Error() string { return "platform unreachable: " + t.Err.Error() }
func (t *TransportError) Unwrap() error { return t.Err }

const attempts = 3

// Do sends an authorized JSON request with the sync engines' shared retry
// discipline: 401 returns ErrReauth, 429 waits out Retry-After, other
// retryable statuses back off on a doubling 1-second delay for up to three
// attempts, and transport failures return a *TransportError immediately (the
// long between-run backoff belongs to the daemon loop). A 200 decodes into
// out; a 403/404 is terminal and yields the contract's machine-readable error
// code. status is the final response's code, 0 when no response arrived; a
// non-200 status with a nil error means the platform answered and the caller
// decides what the refusal means for its pipeline.
func Do(client *http.Client, token, method, url string, body, out any) (status int, errCode string, err error) {
	var payload []byte
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return 0, "", err
		}
	}
	backoff := time.Second
	for attempt := 0; ; attempt++ {
		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}
		req, err := http.NewRequest(method, url, reader)
		if err != nil {
			return 0, "", err
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			return 0, "", &TransportError{Err: err}
		}
		code, errCode, retryAfter, retry, err := handle(resp, out)
		if err != nil || !retry {
			return code, errCode, err
		}
		if attempt >= attempts-1 {
			return code, errCode, nil
		}
		wait := backoff
		if retryAfter > 0 {
			wait = retryAfter
		}
		time.Sleep(wait)
		backoff *= 2
	}
}

func handle(resp *http.Response, out any) (code int, errCode string, retryAfter time.Duration, retry bool, err error) {
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		if out != nil {
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				return resp.StatusCode, "", 0, false, err
			}
		}
		return resp.StatusCode, "", 0, false, nil
	case http.StatusUnauthorized:
		return resp.StatusCode, "", 0, false, ErrReauth
	case http.StatusForbidden, http.StatusNotFound:
		var refusal wire.ErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&refusal)
		return resp.StatusCode, refusal.Error, 0, false, nil
	case http.StatusTooManyRequests:
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
			retryAfter = time.Duration(secs) * time.Second
		}
		return resp.StatusCode, "", retryAfter, true, nil
	default:
		return resp.StatusCode, "", 0, true, nil
	}
}
