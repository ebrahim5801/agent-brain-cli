package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/memsync"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/wire"
)

// memoryShareDisclosure versions the gate text recorded server-side (SC-003).
const memoryShareDisclosure = "team-memory-v1"

func memoryShareExplainer(orgName, retention string) string {
	org := "your organization"
	if orgName != "" {
		org = fmt.Sprintf("the %q organization", orgName)
	}
	return fmt.Sprintf(`Team memory sharing contributes this project's assistant-distilled memory
(decisions, conventions, task state, key facts) to a shared pool for %s.

  - What is shared: memory text captured for THIS project only, redacted for
    secrets before it ever leaves this machine. Personal memory on other
    projects is never affected.
  - With whom: current members of this organization.
  - Retention: %s.
  - Control: mark an entry personal_only to keep it local; 'agent-brain memory
    pause' stops new sharing; 'agent-brain memory share --revoke' stops future
    sharing entirely. Already-shared entries persist and are removed only by
    deleting them in the dashboard.
  - This is SEPARATE from telemetry consent and from personal memory consent.`, org, retention)
}

func newMemoryShareCmd() *cobra.Command {
	var yes, revoke bool
	cmd := &cobra.Command{
		Use:   "share",
		Short: "Share this org project's memory with the team (separate consent)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if revoke {
				return revokeMemoryShare()
			}
			return grantMemoryShare(yes)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "grant sharing without the interactive prompt")
	cmd.Flags().BoolVar(&revoke, "revoke", false, "stop sharing this project (already-shared entries persist)")
	return cmd
}

// currentLink resolves the working directory to its config link, requiring the
// project to be linked.
func currentLink() (cfg *config.Config, linkKey string, link config.Link, err error) {
	cfg, err = config.Load()
	if err != nil {
		return nil, "", config.Link{}, err
	}
	if !cfg.SignedIn() {
		return nil, "", config.Link{}, errors.New("run `agent-brain login` first")
	}
	dir, err := os.Getwd()
	if err != nil {
		return nil, "", config.Link{}, err
	}
	proj := attribution.Resolve(dir)
	linkKey = config.LinkKey(proj.Kind, proj.Identity)
	link, ok := cfg.Links[linkKey]
	if !ok {
		return nil, "", config.Link{}, fmt.Errorf("this project is not linked; run `agent-brain link <project-key>` first")
	}
	return cfg, linkKey, link, nil
}

func grantMemoryShare(yes bool) error {
	cfg, linkKey, link, err := currentLink()
	if err != nil {
		return err
	}

	// Re-validate fresh so the retention wording is truthful (FR-003).
	var resp wire.LinkValidateResponse
	_, errCode, err := callPlatform(cfg.Server(), "/v1/links/validate", cfg.Token, wire.LinkValidateRequest{ProjectKey: link.ProjectKey}, &resp)
	if err != nil {
		return fmt.Errorf("validate project: %w", err)
	}
	if errCode != "" {
		return fmt.Errorf("cannot share: platform refused (%s)", errCode)
	}
	if resp.OrgName == "" || !resp.MemorySharing {
		return errors.New("this project is not an organization project with team memory enabled")
	}

	fmt.Println(memoryShareExplainer(resp.OrgName, retentionWording(resp.MemoryRetentionDays)))
	if !yes {
		if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			fmt.Print("\nShare this project's memory with the team? [y/N] ")
			answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
				fmt.Println("Not shared. Nothing is contributed until you run `agent-brain memory share`.")
				return nil
			}
		} else {
			fmt.Println("\nRe-run with --yes to grant sharing non-interactively.")
			return nil
		}
	}

	if err := applyShareGrant(cfg, linkKey, link.ProjectKey); err != nil {
		return err
	}
	fmt.Printf("Sharing memory for %s. New distilled entries will contribute on the next sync.\n", resp.ProjectName)
	return seedExistingMemories(linkKey, link.ProjectKey, yes)
}

// applyShareGrant records consent server-side then locally, rolling back the
// server grant if the local write fails — both-or-neither, the SC-003 audit
// invariant (contracts/memory-serving.md). Every grant path must go through
// here so the protocol cannot diverge between entry points.
func applyShareGrant(cfg *config.Config, linkKey, projectKey string) error {
	// Consent is a memory operation: it must reach the org's memory endpoint
	// (the self-hosted instance for enterprise orgs), never the vendor cloud (016).
	base, err := memsync.MemoryBaseURL(cfg, cfg.Links[linkKey])
	if err != nil {
		return fmt.Errorf("cannot record consent: %w", err)
	}
	_, errCode, err := callPlatform(base, "/v1/memory/consent", cfg.MemoryAuthToken(),
		wire.MemoryConsentRequest{ProjectKey: projectKey, Action: "grant", Disclosure: memoryShareDisclosure}, nil)
	if err != nil {
		return fmt.Errorf("record consent: %w", err)
	}
	if errCode != "" {
		return fmt.Errorf("platform refused consent (%s)", errCode)
	}
	if _, err := config.Update(func(c *config.Config) error {
		l := c.Links[linkKey]
		l.MemoryShare = &config.MemoryShare{GrantedAt: store.Now()}
		l.TeamMemory = true
		c.Links[linkKey] = l
		return nil
	}); err != nil {
		_, _, _ = callPlatform(base, "/v1/memory/consent", cfg.MemoryAuthToken(),
			wire.MemoryConsentRequest{ProjectKey: projectKey, Action: "revoke"}, nil)
		return fmt.Errorf("save consent locally: %w", err)
	}
	return nil
}

func revokeMemoryShare() error {
	cfg, linkKey, link, err := currentLink()
	if err != nil {
		return err
	}
	if !link.ShareGranted() {
		fmt.Println("Sharing was not granted for this project; nothing to revoke.")
		return nil
	}
	base, err := memsync.MemoryBaseURL(cfg, link)
	if err != nil {
		return fmt.Errorf("cannot revoke consent: %w", err)
	}
	_, errCode, err := callPlatform(base, "/v1/memory/consent", cfg.MemoryAuthToken(),
		wire.MemoryConsentRequest{ProjectKey: link.ProjectKey, Action: "revoke"}, nil)
	if err != nil {
		return fmt.Errorf("revoke consent: %w", err)
	}
	if errCode != "" {
		return fmt.Errorf("platform refused revoke (%s)", errCode)
	}
	if _, err := config.Update(func(c *config.Config) error {
		l := c.Links[linkKey]
		l.MemoryShare = nil
		c.Links[linkKey] = l
		return nil
	}); err != nil {
		return fmt.Errorf("clear local consent: %w", err)
	}
	fmt.Println("Sharing revoked. Future entries stay local; already-shared entries persist until deleted in the dashboard.")
	fmt.Println("You still receive the team's memory (a membership benefit).")
	return nil
}

func newMemoryPauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pause",
		Short: "Pause contributing this project's memory (serving continues)",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, linkKey, link, err := currentLink()
			if err != nil {
				return err
			}
			if !link.ShareGranted() {
				return errors.New("sharing is not enabled for this project; nothing to pause")
			}
			if _, err := config.Update(func(c *config.Config) error {
				l := c.Links[linkKey]
				if l.MemoryShare == nil {
					l.MemoryShare = &config.MemoryShare{}
				}
				l.MemoryShare.PausedAt = store.Now()
				c.Links[linkKey] = l
				return nil
			}); err != nil {
				return err
			}
			fmt.Println("Contribution paused. New entries stay local; resume with `agent-brain memory resume`.")
			return nil
		},
	}
}

func newMemoryResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume",
		Short: "Resume contributing this project's memory",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, linkKey, link, err := currentLink()
			if err != nil {
				return err
			}
			if !link.SharePaused() {
				fmt.Println("Sharing is not paused.")
				return nil
			}
			if _, err := config.Update(func(c *config.Config) error {
				l := c.Links[linkKey]
				if l.MemoryShare != nil {
					l.MemoryShare.PausedAt = ""
				}
				c.Links[linkKey] = l
				return nil
			}); err != nil {
				return err
			}
			fmt.Println("Contribution resumed. Entries captured while paused stay local (resume is not retroactive).")
			return nil
		},
	}
}

// offerMemoryShareOnLink presents the memory gate immediately after linking an
// org project, as a separate prompt. It never blocks the link: declining or a
// non-interactive shell just leaves sharing off.
func offerMemoryShareOnLink(linkKey, projectKey, orgName string, retentionDays int) error {
	fmt.Println()
	fmt.Println(memoryShareExplainer(orgName, retentionWording(retentionDays)))
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		fmt.Println("\nTeam memory sharing is off. Enable it any time with `agent-brain memory share`.")
		return nil
	}
	fmt.Print("\nShare this project's memory with the team? [y/N] ")
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
		fmt.Println("Team memory sharing is off. You will still receive the team's memory; enable sharing later with `agent-brain memory share`.")
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := applyShareGrant(cfg, linkKey, projectKey); err != nil {
		return err
	}
	fmt.Println("Sharing enabled.")
	return seedExistingMemories(linkKey, projectKey, false)
}

func retentionWording(days int) string {
	if days <= 0 {
		return "kept until deleted"
	}
	return fmt.Sprintf("kept %d days, then automatically removed", days)
}
