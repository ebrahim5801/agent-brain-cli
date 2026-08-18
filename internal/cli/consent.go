package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

const consentExplainer = `agent-brain will send ONLY these telemetry categories for LINKED projects:
  - session start/end times and durations
  - token counts and model names
  - activity counts (prompts, tool uses)
  - the opaque project key you linked
It will NEVER send: file paths, repository names or URLs, prompts,
code, file contents, or anything from unlinked projects.`

func newConsentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consent",
		Short: "Show or change what agent-brain may send to the cloud",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			fmt.Println(consentExplainer)
			fmt.Println()
			if cfg.TelemetryConsented() {
				fmt.Printf("Telemetry consent: granted %s (categories: %s)\n",
					cfg.Consent.Telemetry.GrantedAt, strings.Join(cfg.Consent.Telemetry.Categories, ", "))
				fmt.Println("Withdraw at any time with `agent-brain consent withdraw`.")
			} else {
				fmt.Println("Telemetry consent: not granted — nothing leaves this machine.")
				fmt.Println("Grant it with `agent-brain consent grant`.")
			}
			return nil
		},
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "grant",
			Short: "Allow sending the telemetry categories above",
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := grantConsent(); err != nil {
					return err
				}
				fmt.Println("Telemetry consent granted. Linked projects will sync.")
				return nil
			},
		},
		&cobra.Command{
			Use:   "withdraw",
			Short: "Stop all syncing and return to local-only (local data is kept)",
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg, err := config.Load()
				if err != nil {
					return err
				}
				if !cfg.TelemetryConsented() {
					fmt.Println("Telemetry consent was not granted; nothing to withdraw.")
					return nil
				}
				if _, err := config.Update(func(c *config.Config) error {
					c.Consent.Telemetry = nil
					return nil
				}); err != nil {
					return err
				}
				fmt.Println("Consent withdrawn. Syncing stopped; all data stays on this machine.")
				return nil
			},
		},
	)
	return cmd
}

func grantConsent() error {
	_, err := config.Update(func(c *config.Config) error {
		c.Consent.Telemetry = &config.Consent{
			GrantedAt:  store.Now(),
			Categories: config.TelemetryCategories,
		}
		return nil
	})
	return err
}

// promptConsentIfNeeded runs after the first successful link: interactive
// terminals get the prompt; non-interactive runs are told how to grant.
// Nothing transmits either way until consent exists (FR-011). When the linked
// project is organization-owned, the notice names the org before asking (FR-013).
func promptConsentIfNeeded(orgName, orgVisibility string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.TelemetryConsented() {
		return nil
	}
	fmt.Println()
	fmt.Println(consentExplainer)
	if orgName != "" {
		fmt.Printf("\nThis project belongs to %q; its telemetry will be visible to that organization%s.\n",
			orgName, visibilityClause(orgVisibility))
	}
	if fi, err := os.Stdin.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		fmt.Println("Nothing will be sent until you run `agent-brain consent grant`.")
		return nil
	}
	fmt.Print("Allow sending telemetry? [y/N] ")
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(answer)); a == "y" || a == "yes" {
		if err := grantConsent(); err != nil {
			return err
		}
		fmt.Println("Telemetry consent granted.")
	} else {
		fmt.Println("Not granted. Nothing will be sent until you run `agent-brain consent grant`.")
	}
	return nil
}

func init() {
	afterLink = promptConsentIfNeeded
}
