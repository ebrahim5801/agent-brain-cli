package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
	"github.com/ebrahim5801/agent-brain-cli/internal/diag"
	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

func newMemoryImportCmd() *cobra.Command {
	var dryRun, yes, intoCurrent bool
	cmd := &cobra.Command{
		Use:   "import FILE",
		Short: "Add memories from a `memory export --format json` file to this project",
		Long: `Adds the entries of an export file, made on another machine or by a colleague,
to the project in the current folder. It shows a summary first and writes
nothing until you confirm; --dry-run shows only the summary, and --yes imports
without asking (needed when no terminal can ask, for example when an assistant
runs the command). Entries already here are skipped, so importing a file twice
adds nothing. Imported entries start unverified, and an import never retires
an entry that was already on this machine.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fh, err := os.Open(args[0])
			if err != nil {
				return err
			}
			f, err := memory.ReadExport(fh)
			fh.Close()
			if err != nil {
				return err
			}
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			proj := attribution.Resolve(dir)
			if !f.ProjectMatches(proj) && !intoCurrent {
				from := f.Project.Name
				if f.Project.Remote != "" {
					from = f.Project.Remote
				}
				return fmt.Errorf("this file is from project %s, not %s. Run the import inside that project's folder, or add --into-current-project to import it here anyway (needed for a project without a git remote, since its folder differs between machines)", from, proj.DisplayName)
			}
			st, err := store.Open()
			if err != nil {
				return err
			}
			defer st.Close()
			identity := store.ProjectIdentity{Kind: proj.Kind, Identity: proj.Identity, DisplayName: proj.DisplayName}
			entries, relations := f.ImportRecords(dir)
			now := store.Now()
			completeness := "imported " + now[:10] + "; verification reset"
			preview, err := st.ImportMemories(identity, entries, relations, completeness, now, true)
			if err != nil {
				return err
			}
			printImportSummary(f, preview)
			if preview.Imported == 0 && preview.Relations == 0 {
				fmt.Println("Nothing to import.")
				return nil
			}
			switch {
			case dryRun:
				fmt.Println("Nothing written (--dry-run).")
				return nil
			case !yes && !stdinIsTTY():
				return errors.New("nothing imported: add --yes to import without a prompt")
			case !yes:
				fmt.Print("Import these entries? [y/N] ")
				// /dev/null passes the terminal check but gives no answer.
				answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
				if err != nil && answer == "" {
					fmt.Println()
					return errors.New("nothing imported: no answer could be read; add --yes to import without a prompt")
				}
				if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
					fmt.Println("Nothing imported.")
					return nil
				}
			}
			result, err := st.ImportMemories(identity, entries, relations, completeness, store.Now(), false)
			if err != nil {
				return err
			}
			fmt.Printf("Imported %d entries and %d links into %s.\n", result.Imported, result.Relations, proj.DisplayName)
			if !memoryHookActive(st, &diag.Logger{}, result.ProjectID) {
				fmt.Println("Memory is off for this project on this machine. Turn it on with `agent-brain memory enable` to use these entries.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be imported and write nothing")
	cmd.Flags().BoolVar(&yes, "yes", false, "import without asking")
	cmd.Flags().BoolVar(&intoCurrent, "into-current-project", false, "import into this folder's project even if the file came from another")
	return cmd
}

func printImportSummary(f memory.ExportFile, r store.ImportResult) {
	made := f.ExportedAt
	if len(made) >= 10 {
		made = made[:10]
	}
	if f.Generator != "" {
		made += " with " + f.Generator
	}
	fmt.Printf("Memory file from %s, exported %s: %d entries, %d links.\n", f.Project.Name, made, len(f.Entries), len(f.Relations))
	line := fmt.Sprintf("  New: %d", r.Imported)
	if r.Imported > 0 {
		line += fmt.Sprintf(" (%d stated by a person, %d critical)", r.Explicit, r.Critical)
	}
	fmt.Println(line)
	fmt.Printf("  Already here: %d\n", r.Duplicates)
	if r.Relations > 0 || r.Pending > 0 {
		line = fmt.Sprintf("  Links between entries: %d", r.Relations)
		if r.Pending > 0 {
			line += fmt.Sprintf(" (%d replacements of entries already here wait for review)", r.Pending)
		}
		fmt.Println(line)
	}
	var lost []string
	if r.ScopedSkipped > 0 {
		lost = append(lost, fmt.Sprintf("%d branch or checkout entries", r.ScopedSkipped))
	}
	if r.CompletedSkipped > 0 {
		lost = append(lost, fmt.Sprintf("%d completed tasks", r.CompletedSkipped))
	}
	if r.RelationsSkipped > 0 {
		lost = append(lost, fmt.Sprintf("%d links", r.RelationsSkipped))
	}
	if r.RationalesLost > 0 {
		lost = append(lost, fmt.Sprintf("the rationale of %d entries", r.RationalesLost))
	}
	if len(lost) > 0 {
		fmt.Printf("  Not kept, because memory V2 is off here: %s. Turn it on (see `agent-brain memory upgrade-v2`) before importing to keep them.\n", strings.Join(lost, ", "))
	}
}
