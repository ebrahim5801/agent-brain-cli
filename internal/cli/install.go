package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func validAssistantNames() string {
	var names []string
	for _, a := range assistant.Registry() {
		names = append(names, a.Name())
	}
	return strings.Join(names, ", ")
}

func newInstallCmd() *cobra.Command {
	var only []string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Set up local collection and integrate your coding assistants",
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store.Open()
			if err != nil {
				return fmt.Errorf("initialize local store: %w", err)
			}
			st.Close()
			fmt.Printf("Local store ready at %s\n", st.Path)

			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("resolve own binary path: %w", err)
			}

			// Unknown --assistant names are a hard error before anything is
			// written, so the user can correct the typo.
			for _, name := range only {
				if _, ok := assistant.ByName(name); !ok {
					return fmt.Errorf("unknown assistant %q; valid: %s", name, validAssistantNames())
				}
			}

			targets, detectedAny, anyFailed := installTargets(only)
			if len(only) == 0 && len(targets) == 0 {
				if detectedAny {
					fmt.Println("Nothing to do — every detected assistant was skipped.")
				} else {
					fmt.Println("No supported assistants were detected on this machine.")
					fmt.Println("Collection will activate once one is installed: re-run `agent-brain install` then.")
				}
				return nil
			}

			for _, a := range targets {
				backups, err := a.Install(exe)
				if err != nil {
					fmt.Printf("%s: integration failed: %v\n", a.DisplayName(), err)
					anyFailed = true
					continue
				}
				fmt.Printf("%s: integrated\n", a.DisplayName())
				for _, b := range backups {
					fmt.Printf("  previous settings backed up to %s\n", b)
				}
				// Some hosts need a manual step before the hooks we just wrote
				// actually run; those adapters say so here (assistant.PostInstallNotice).
				if n, ok := a.(assistant.PostInstallNotice); ok {
					fmt.Println(n.PostInstallNotice())
				}
			}

			if anyFailed {
				return errors.New("one or more assistant integrations failed")
			}
			if len(only) == 0 {
				fmt.Println("Done. Run `agent-brain status` to verify.")
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&only, "assistant", nil, "integrate only the named assistant(s) (repeatable); default integrates every detected assistant")
	return cmd
}

// installTargets resolves which adapters to integrate, whether anything was
// detected at all, and whether a named-but-undetected target already failed.
// With explicit names it targets exactly those (a named-but-undetected one is
// a per-assistant failure, not a prompt). Otherwise it walks the registry:
// interactively it offers each detected assistant; non-interactively it
// integrates all detected without prompting (preserving scripted behavior).
func installTargets(only []string) (targets []assistant.Adapter, detectedAny, anyFailed bool) {
	if len(only) > 0 {
		for _, name := range only {
			a, _ := assistant.ByName(name)
			if !a.Detected() {
				fmt.Printf("%s was not detected on this machine\n", a.DisplayName())
				anyFailed = true
				continue
			}
			targets = append(targets, a)
		}
		return targets, len(targets) > 0, anyFailed
	}

	interactive := stdinIsTTY()
	reader := bufio.NewReader(os.Stdin)
	for _, a := range assistant.Registry() {
		if !a.Detected() {
			continue
		}
		detectedAny = true
		if interactive && !confirm(reader, fmt.Sprintf("Integrate %s? [Y/n] ", a.DisplayName())) {
			fmt.Printf("%s: skipped\n", a.DisplayName())
			continue
		}
		targets = append(targets, a)
	}
	return targets, detectedAny, false
}

// confirm reads a yes/no answer defaulting to yes (empty line = yes).
func confirm(reader *bufio.Reader, prompt string) bool {
	fmt.Print(prompt)
	answer, _ := reader.ReadString('\n')
	a := strings.ToLower(strings.TrimSpace(answer))
	return a == "" || a == "y" || a == "yes"
}

func newUninstallCmd() *cobra.Command {
	var purgeData, yes bool
	var only []string
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove agent-brain integration from your assistants (local data is kept unless --purge-data)",
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, name := range only {
				if _, ok := assistant.ByName(name); !ok {
					return fmt.Errorf("unknown assistant %q; valid: %s", name, validAssistantNames())
				}
			}

			// One assistant's failure (e.g. an unparseable settings file) must
			// not block removal from the others.
			removed, anyFailed := 0, false
			for _, a := range uninstallTargets(only) {
				state, err := a.State()
				integrated := err == nil && state.Integrated()
				if err := a.Uninstall(); err != nil {
					fmt.Printf("%s: removal failed: %v\n", a.DisplayName(), err)
					anyFailed = true
					continue
				}
				if integrated {
					fmt.Printf("%s: integration removed\n", a.DisplayName())
					removed++
				} else if len(only) > 0 {
					fmt.Printf("%s: was not integrated (nothing to remove)\n", a.DisplayName())
				}
			}
			if removed == 0 && !anyFailed && len(only) == 0 {
				fmt.Println("No assistant integrations were present.")
			}
			fmt.Println("Your other settings are untouched.")
			if anyFailed {
				return errors.New("one or more assistant removals failed")
			}

			if !purgeData {
				fmt.Println("Local data kept. Use --purge-data to delete it.")
				return nil
			}
			dataDir, err := store.DataDir()
			if err != nil {
				return err
			}
			if !yes {
				fmt.Printf("Delete all collected data in %s? [y/N] ", dataDir)
				reader := bufio.NewReader(os.Stdin)
				answer, _ := reader.ReadString('\n')
				if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
					fmt.Println("Kept local data.")
					return nil
				}
			}
			if err := os.RemoveAll(dataDir); err != nil {
				return fmt.Errorf("purge data: %w", err)
			}
			fmt.Printf("Deleted %s\n", dataDir)
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&only, "assistant", nil, "remove only the named assistant(s) (repeatable); default removes every integration")
	cmd.Flags().BoolVar(&purgeData, "purge-data", false, "also delete the local database and diagnostics")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

// uninstallTargets returns the named adapters, or every registered adapter when
// no names are given (removing from all integrations, generalizing the historic
// claude-only behavior).
func uninstallTargets(only []string) []assistant.Adapter {
	if len(only) == 0 {
		return assistant.Registry()
	}
	var targets []assistant.Adapter
	for _, name := range only {
		if a, ok := assistant.ByName(name); ok {
			targets = append(targets, a)
		}
	}
	return targets
}
