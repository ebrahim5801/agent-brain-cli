package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant/claude"
	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/entitlement"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// enableMemory is the full activation flow (contracts/cli-memory.md):
// sign-in check, synchronous entitlement verification, one-time consent,
// registration check, active-state summary. yes skips the interactive
// consent confirmation (the explainer is still printed).
func enableMemory(yes bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.SignedIn() {
		return errors.New("memory needs a signed-in account: run `agent-brain login` first")
	}

	cfg, err = entitlement.Refresh(nil, 15*time.Second)
	if err != nil {
		if entitlement.Entitled(cfg, time.Now()) {
			fmt.Printf("Could not reach the platform (%v); using the entitlement verified %s.\n",
				err, cfg.Entitlement.VerifiedAt)
		} else {
			return fmt.Errorf("could not verify your subscription: %w", err)
		}
	}
	if cfg.Entitlement == nil || cfg.Entitlement.Tier != entitlement.TierPro {
		fmt.Println("Personal memory is a Pro feature.")
		fmt.Printf("Upgrade at %s/billing and re-run `agent-brain memory enable`.\n", cfg.Server())
		return errors.New("current tier: free")
	}

	if !cfg.MemoryConsented() {
		fmt.Println(memoryConsentExplainer)
		fmt.Println()
		if !yes {
			if fi, err := os.Stdin.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
				return errors.New("memory needs your explicit consent: re-run in a terminal or pass --yes")
			}
			fmt.Print("Enable personal memory on this machine? [y/N] ")
			if !confirmYes() {
				fmt.Println("Not enabled. Nothing will be captured.")
				return nil
			}
		}
		if _, err := config.Update(func(c *config.Config) error {
			c.Consent.Memory = &config.Consent{GrantedAt: store.Now(), Categories: []string{"memory"}}
			return nil
		}); err != nil {
			return err
		}
	}

	if registered, cmd, _ := claude.MCPRegistered(); !registered || cmd == "" {
		exe, err := os.Executable()
		if err == nil {
			if err := claude.RegisterMCP(exe); err != nil {
				fmt.Printf("Warning: could not register the memory MCP server: %v\n", err)
				fmt.Println("Run `agent-brain install` to repair the integration.")
			}
		}
	}

	fmt.Println("Personal memory is active on this machine.")
	fmt.Printf("  tier:      pro (verified %s)\n", cfg.Entitlement.VerifiedAt)
	fmt.Println("  capture:   assistant distills context at session end + explicit saves")
	fmt.Println("  serving:   injected at session start; agent-brain memory list to inspect")
	fmt.Println("  privacy:   memory never leaves this machine; disable per project with `agent-brain memory disable`")
	return nil
}
