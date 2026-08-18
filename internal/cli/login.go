package cli

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/wire"
)

func newLoginCmd() *cobra.Command {
	var noBrowser bool
	cmd := &cobra.Command{
		Use:     "login",
		Short:   "Sign in to the agent-brain platform (approve once in your browser)",
		PostRun: updatePostRun,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.SignedIn() {
				// The local token can outlive its server-side record (revoked
				// elsewhere, expired from disuse, or the platform's data moved).
				// Trusting it blind would deadlock with every command that says
				// "run `agent-brain login`" — so ask the platform first.
				if tokenRejected(cfg.Server(), cfg.Token) {
					fmt.Println("Your sign-in is no longer valid on the platform; signing in again.")
					if _, err := config.Update(func(c *config.Config) error {
						c.Token = ""
						c.MemoryToken = ""
						c.AccountEmail = ""
						return nil
					}); err != nil {
						return fmt.Errorf("clear stale sign-in: %w", err)
					}
				} else {
					fmt.Printf("Already signed in as %s\n", cfg.AccountEmail)
					return nil
				}
			}
			server := cfg.Server()

			machineID := cfg.MachineID
			if machineID == "" {
				if updated, err := config.Update(func(c *config.Config) error {
					if c.MachineID == "" {
						c.MachineID = uuid.NewString()
					}
					return nil
				}); err == nil {
					machineID = updated.MachineID
				}
			}
			hint := runtime.GOOS
			if host, err := os.Hostname(); err == nil && host != "" {
				hint = runtime.GOOS + "/" + host
			}
			var start wire.DeviceStartResponse
			if _, errCode, err := callPlatform(server, "/v1/auth/device", "", wire.DeviceStartRequest{MachineHint: hint, MachineID: machineID}, &start); err != nil || errCode != "" {
				if err == nil {
					err = fmt.Errorf("platform refused sign-in: %s", errCode)
				}
				return fmt.Errorf("start sign-in: %w", err)
			}

			fmt.Printf("Confirm this code in your browser: %s\n", start.UserCode)
			if noBrowser {
				fmt.Printf("Visit %s on any device.\n", start.VerificationURI)
			} else {
				fmt.Printf("Opening %s ... (or visit it on any device)\n", start.VerificationURI)
				openBrowser(start.VerificationURI)
			}
			fmt.Println("Waiting for approval...")

			token, memoryToken, email, err := pollDeviceToken(server, start)
			if err != nil {
				return err
			}

			serverOverride := os.Getenv("AGENT_BRAIN_SERVER_URL")
			if _, err := config.Update(func(c *config.Config) error {
				if c.MachineID == "" {
					c.MachineID = uuid.NewString()
				}
				if serverOverride != "" {
					c.ServerURL = serverOverride
				}
				c.Token = token
				c.MemoryToken = memoryToken
				c.AccountEmail = email
				return nil
			}); err != nil {
				return fmt.Errorf("save sign-in: %w", err)
			}
			fmt.Printf("Signed in as %s\n", email)
			return finishLogin()
		},
	}
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "print the approval URL instead of opening a browser")
	return cmd
}

// finishLogin is extended by the sync feature to install the background
// service once an account exists on the machine.
var finishLogin = func() error { return nil }

// tokenRejected reports whether the platform definitively refused the stored
// token (401). Network trouble or server errors return false: they prove
// nothing about the token, and a fresh device flow would fail the same way.
func tokenRejected(server, token string) bool {
	req, err := http.NewRequest(http.MethodGet, server+"/v1/entitlement", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := *platformHTTP
	client.Timeout = 5 * time.Second
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusUnauthorized
}

func pollDeviceToken(server string, start wire.DeviceStartResponse) (token, memoryToken, email string, err error) {
	interval := time.Duration(start.Interval) * time.Second
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	deadline := time.Now().Add(time.Duration(start.ExpiresIn) * time.Second)
	for {
		if time.Now().After(deadline) {
			return "", "", "", errors.New("sign-in did not complete in time; nothing was changed — run `agent-brain login` to try again")
		}
		time.Sleep(interval)

		var resp wire.DeviceTokenResponse
		_, errCode, err := callPlatform(server, "/v1/auth/device/token", "", wire.DeviceTokenRequest{DeviceCode: start.DeviceCode}, &resp)
		if err != nil {
			return "", "", "", fmt.Errorf("poll sign-in: %w", err)
		}
		switch errCode {
		case "":
			return resp.Token, resp.MemoryToken, resp.AccountEmail, nil
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			return "", "", "", errors.New("sign-in was denied in the browser; nothing was changed")
		case "expired_token":
			return "", "", "", errors.New("the sign-in request expired; nothing was changed — run `agent-brain login` to try again")
		default:
			return "", "", "", fmt.Errorf("sign-in failed: %s", errCode)
		}
	}
}

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Sign out: revoke this machine's token and stop syncing (local data is kept)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if !cfg.SignedIn() {
				fmt.Println("Not signed in.")
				return nil
			}
			if status, errCode, err := callPlatform(cfg.Server(), "/v1/auth/logout", cfg.Token, struct{}{}, nil); err != nil || (status != 204 && errCode != "reauth_required") {
				fmt.Println("Could not reach the platform to revoke the token; it was removed from this machine anyway.")
			}
			if _, err := config.Update(func(c *config.Config) error {
				c.Token = ""
				c.MemoryToken = ""
				c.AccountEmail = ""
				return nil
			}); err != nil {
				return fmt.Errorf("remove sign-in: %w", err)
			}
			fmt.Println("Signed out. Local collection continues; project links and consent are kept for the next sign-in.")
			return finishLogout()
		},
	}
}

// finishLogout is extended by the sync feature to remove the background service.
var finishLogout = func() error { return nil }

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
