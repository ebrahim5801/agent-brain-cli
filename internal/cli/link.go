package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/wire"
)

func newLinkCmd() *cobra.Command {
	var force, auto, decline bool
	cmd := &cobra.Command{
		Use:     "link <project-key>",
		Short:   "Connect this project directory to a cloud project (one time per project)",
		Args:    cobra.MaximumNArgs(1),
		PostRun: updatePostRun,
		RunE: func(cmd *cobra.Command, args []string) error {
			if decline {
				return runLinkDecline()
			}
			if auto {
				return runLinkAuto(force)
			}
			if len(args) != 1 {
				return errors.New("provide a project key from the platform, or use --auto to create one")
			}
			projectKey := args[0]
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if !cfg.SignedIn() {
				return errors.New("run `agent-brain login` first")
			}

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			project := attribution.Resolve(cwd)
			linkKey := config.LinkKey(project.Kind, project.Identity)

			if existing, ok := cfg.Links[linkKey]; ok && !force {
				if existing.ProjectKey == projectKey {
					fmt.Printf("%s is already linked to this project.\n", project.DisplayName)
					return nil
				}
				return fmt.Errorf("%s is already linked to another project (since %s); use --force to rebind it",
					project.DisplayName, existing.LinkedAt)
			}

			var resp wire.LinkValidateResponse
			status, errCode, err := callPlatform(cfg.Server(), "/v1/links/validate", cfg.Token, wire.LinkValidateRequest{ProjectKey: projectKey}, &resp)
			if err != nil {
				return fmt.Errorf("validate project key: %w", err)
			}
			switch errCode {
			case "":
			case "not_authorized":
				return errors.New("your account cannot access that project; nothing was linked")
			case "unknown_key":
				return errors.New("no project has that key; check it on the platform")
			case "key_rotated":
				return errors.New("that key was rotated; get the current key from the platform and re-run link")
			case "reauth_required":
				// The server just rejected the stored token, so it is dead
				// (revoked, unknown, or expired from disuse).
				if cerr := clearStaleSignIn(); cerr != nil {
					return cerr
				}
				return errors.New("re-authentication required — run `agent-brain login`, then link again")
			default:
				return fmt.Errorf("platform refused the link (%d %s)", status, errCode)
			}

			if prev, ok := cfg.Links[linkKey]; ok && prev.ShareGranted() {
				// Rebinding replaces the link, and sharing consent is per
				// project — the old grant cannot be assumed to apply here.
				fmt.Println("Note: this directory's previous memory-sharing consent applied to the old link and is reset;")
				fmt.Println("re-grant with `agent-brain memory share` (or revoke the old project's grant from its dashboard).")
			}
			if _, err := config.Update(func(c *config.Config) error {
				if c.Links == nil {
					c.Links = map[string]config.Link{}
				}
				// Cache the team-memory capability as a gating hint; the server
				// re-authorizes every memory call and clears it on a pull 403 (R5).
				c.Links[linkKey] = config.Link{
					ProjectKey: projectKey, LinkedAt: store.Now(),
					TeamMemory: resp.MemorySharing, OrgProject: resp.OrgName != "",
					// Route memory to the org's self-hosted endpoint when the server
					// delivers one (enterprise + activated); empty keeps memory on the
					// vendor cloud (016).
					MemoryEndpoint: resp.MemoryEndpoint,
				}
				delete(c.LinkDeclines, linkKey)
				return nil
			}); err != nil {
				return fmt.Errorf("save link: %w", err)
			}
			// Make the promise below true even when the sessions were synced
			// before (rebind, or a platform that lost its data): queue them all
			// for re-send. The server upserts by sync_uid, so this never
			// duplicates anything it already has.
			if st, serr := store.Open(); serr == nil {
				if _, derr := st.MarkProjectSessionsDirty(project.Kind, project.Identity); derr != nil {
					fmt.Printf("Warning: could not queue existing sessions for re-sync: %v\n", derr)
				}
				_ = st.Close()
			}
			fmt.Printf("Linked %s to %s. Previously collected sessions for this project will sync too.\n",
				project.DisplayName, resp.ProjectName)
			if resp.OrgName != "" {
				fmt.Printf("This project belongs to the organization %q. Telemetry you sync for it will be visible to that organization%s.\n",
					resp.OrgName, visibilityClause(resp.OrgVisibility))
			}
			if err := afterLink(resp.OrgName, resp.OrgVisibility); err != nil {
				return err
			}
			// Chain the memory-sharing gate as a SEPARATE second prompt for org
			// projects; declining leaves telemetry linking fully functional
			// (Constitution II: never bundled, never implied).
			if resp.MemorySharing {
				return offerMemoryShareOnLink(linkKey, projectKey, resp.OrgName, resp.MemoryRetentionDays)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "rebind a directory that is already linked to a different project")
	cmd.Flags().BoolVar(&auto, "auto", false, "link without a key: find-or-create a personal cloud project named after this directory")
	cmd.Flags().BoolVar(&decline, "decline", false, "record that this directory should stay unlinked and never be offered again")
	return cmd
}

// runLinkAuto links the current directory to a personal cloud project the
// platform finds-or-creates by name, so no key needs to be pasted from the
// dashboard. It also clears any recorded decline: running it is the user
// changing their mind.
func runLinkAuto(force bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.SignedIn() {
		return errors.New("run `agent-brain login` first")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	project := attribution.Resolve(cwd)
	linkKey := config.LinkKey(project.Kind, project.Identity)
	if _, ok := cfg.Links[linkKey]; ok && !force {
		fmt.Printf("%s is already linked to a project.\n", project.DisplayName)
		return nil
	}

	var resp wire.LinkAutoCreateResponse
	status, errCode, err := callPlatform(cfg.Server(), "/v1/links/autocreate", cfg.Token,
		wire.LinkAutoCreateRequest{ProjectName: project.DisplayName}, &resp)
	if err != nil {
		return fmt.Errorf("create project: %w", err)
	}
	switch errCode {
	case "":
	case "reauth_required":
		if cerr := clearStaleSignIn(); cerr != nil {
			return cerr
		}
		return errors.New("re-authentication required — run `agent-brain login`, then link again")
	case "invalid_name":
		return fmt.Errorf("the platform rejected the project name %q; link manually with a key", project.DisplayName)
	default:
		return fmt.Errorf("platform refused the link (%d %s)", status, errCode)
	}

	if _, err := config.Update(func(c *config.Config) error {
		if c.Links == nil {
			c.Links = map[string]config.Link{}
		}
		c.Links[linkKey] = config.Link{ProjectKey: resp.ProjectKey, LinkedAt: store.Now()}
		delete(c.LinkDeclines, linkKey)
		return nil
	}); err != nil {
		return fmt.Errorf("save link: %w", err)
	}
	if st, serr := store.Open(); serr == nil {
		if _, derr := st.MarkProjectSessionsDirty(project.Kind, project.Identity); derr != nil {
			fmt.Printf("Warning: could not queue existing sessions for re-sync: %v\n", derr)
		}
		_ = st.Close()
	}
	verb := "Created cloud project"
	if !resp.Created {
		verb = "Reusing your existing cloud project"
	}
	fmt.Printf("%s %q and linked %s to it. Previously collected sessions for this project will sync too.\n",
		verb, resp.ProjectName, project.DisplayName)
	return afterLink("", "")
}

// runLinkDecline records that this directory should stay unlinked so the
// session-start offer never re-asks. Local collection continues unchanged.
func runLinkDecline() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	project := attribution.Resolve(cwd)
	linkKey := config.LinkKey(project.Kind, project.Identity)
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, ok := cfg.Links[linkKey]; ok {
		fmt.Printf("%s is already linked; nothing to decline. Use `agent-brain unlink` to stop syncing.\n", project.DisplayName)
		return nil
	}
	if _, err := config.Update(func(c *config.Config) error {
		if c.LinkDeclines == nil {
			c.LinkDeclines = map[string]string{}
		}
		c.LinkDeclines[linkKey] = store.Now()
		return nil
	}); err != nil {
		return fmt.Errorf("record decline: %w", err)
	}
	fmt.Printf("Noted: %s stays unlinked and won't be offered again. Local collection continues; link any time with `agent-brain link --auto`.\n", project.DisplayName)
	return nil
}

// clearStaleSignIn drops a token the server just rejected, so `login` doesn't
// short-circuit on SignedIn() and deadlock with link.
func clearStaleSignIn() error {
	if _, err := config.Update(func(c *config.Config) error {
		c.Token = ""
		c.MemoryToken = ""
		c.AccountEmail = ""
		return nil
	}); err != nil {
		return fmt.Errorf("re-authentication required, but clearing the stale sign-in failed: %w", err)
	}
	return nil
}

// visibilityClause renders the org's data-visibility policy for the consent
// notice (FR-013, R12).
func visibilityClause(policy string) string {
	switch policy {
	case "aggregate_only":
		return " as part of aggregate totals (no per-member breakdown)"
	case "per_member":
		return ", including your individual contribution"
	}
	return ""
}

// afterLink is extended by the consent feature to prompt on first link; it
// receives the owning organization's name and visibility policy (empty for
// personal projects) so the consent gate can narrow its wording.
var afterLink = func(orgName, orgVisibility string) error { return nil }

func newUnlinkCmd() *cobra.Command {
	var identity string
	cmd := &cobra.Command{
		Use:   "unlink",
		Short: "Stop syncing this project directory (local collection continues)",
		RunE: func(cmd *cobra.Command, args []string) error {
			linkKey := identity
			display := identity
			if linkKey == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				project := attribution.Resolve(cwd)
				linkKey = config.LinkKey(project.Kind, project.Identity)
				display = project.DisplayName
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if _, ok := cfg.Links[linkKey]; !ok {
				return fmt.Errorf("%s is not linked", display)
			}
			if _, err := config.Update(func(c *config.Config) error {
				delete(c.Links, linkKey)
				return nil
			}); err != nil {
				return fmt.Errorf("remove link: %w", err)
			}
			fmt.Printf("Unlinked %s. Future sessions stay local; already-synced data is unaffected.\n", display)
			return nil
		},
	}
	cmd.Flags().StringVar(&identity, "identity", "", "unlink by identity key (e.g. remote:github.com/acme/api) instead of the current directory")
	return cmd
}
