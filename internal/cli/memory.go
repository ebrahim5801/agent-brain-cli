package cli

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
	"github.com/ebrahim5801/agent-brain-cli/internal/gitstate"
	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// cliGitBudget bounds git subprocess time when the CLI renders freshness;
// interactive use tolerates more than the hook path.
const cliGitBudget = 2 * time.Second

func newMemoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Inspect and control per-project personal memory (Pro)",
	}
	cmd.AddCommand(
		newMemoryEnableCmd(),
		newMemoryDisableCmd(),
		newMemoryListCmd(),
		newMemoryShowCmd(),
		newMemoryEditCmd(),
		newMemoryDeleteCmd(),
		newMemoryWipeCmd(),
		newMemoryRestoreCmd(),
		newMemoryShareCmd(),
		newMemoryPauseCmd(),
		newMemoryResumeCmd(),
	)
	return cmd
}

// currentProject resolves the working directory's project and its store row.
// found is false when the project has never been seen (no row, no memories).
func currentProject(st *store.Store) (projectID int64, identity string, found bool, err error) {
	dir, err := os.Getwd()
	if err != nil {
		return 0, "", false, err
	}
	proj := attribution.Resolve(dir)
	err = st.QueryRow(`SELECT id FROM projects WHERE identity = ?`, proj.Identity).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, proj.Identity, false, nil
	}
	if err != nil {
		return 0, proj.Identity, false, err
	}
	return projectID, proj.Identity, true, nil
}

func newMemoryListCmd() *cobra.Command {
	var all bool
	var kind string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List this project's memory entries",
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store.Open()
			if err != nil {
				return err
			}
			defer st.Close()
			projectID, identity, found, err := currentProject(st)
			if err != nil {
				return err
			}
			if !found {
				fmt.Printf("No memory for %s yet.\n", identity)
				return nil
			}
			dir, _ := os.Getwd()
			entries, err := memory.List(st, projectID, dir, all, cliGitBudget)
			if err != nil {
				return err
			}
			if kind != "" {
				var filtered []memory.Ranked
				for _, r := range entries {
					if r.Entry.Kind == kind {
						filtered = append(filtered, r)
					}
				}
				entries = filtered
			}
			if asJSON {
				return printMemoriesJSON(entries)
			}
			if len(entries) == 0 {
				fmt.Printf("No memory for %s yet.\n", identity)
				return nil
			}
			for _, r := range entries {
				line := memory.RenderEntry(r)
				if r.Entry.Status == store.MemorySuperseded {
					line += fmt.Sprintf(" [superseded by %d]", r.Entry.SupersededBy.Int64)
				}
				fmt.Println(line)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include superseded and deleted entries")
	cmd.Flags().StringVar(&kind, "kind", "", "filter by kind (decision, convention, task_state, fact)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

type memoryJSON struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"`
	Origin     string `json:"origin"`
	Status     string `json:"status"`
	Content    string `json:"content"`
	Branch     string `json:"branch,omitempty"`
	Commit     string `json:"commit,omitempty"`
	CapturedAt string `json:"captured_at"`
	Freshness  string `json:"freshness"`
	Edited     bool   `json:"edited,omitempty"`
	Superseded int64  `json:"superseded_by,omitempty"`
}

func printMemoriesJSON(entries []memory.Ranked) error {
	out := make([]memoryJSON, 0, len(entries))
	for _, r := range entries {
		out = append(out, memoryJSON{
			ID: r.Entry.ID, Kind: r.Entry.Kind, Origin: r.Entry.Origin, Status: r.Entry.Status,
			Content: r.Entry.Content, Branch: r.Entry.Branch.String, Commit: r.Entry.CommitHash.String,
			CapturedAt: r.Entry.CapturedAt, Freshness: r.Freshness.Render(),
			Edited: r.Entry.Edited, Superseded: r.Entry.SupersededBy.Int64,
		})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func parseMemoryID(arg string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimPrefix(arg, "#"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid memory id %q", arg)
	}
	return id, nil
}

func newMemoryShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show one memory entry in full",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseMemoryID(args[0])
			if err != nil {
				return err
			}
			st, err := store.Open()
			if err != nil {
				return err
			}
			defer st.Close()
			m, err := st.GetMemory(id)
			if err != nil {
				return err
			}
			dir, _ := os.Getwd()
			fresh := gitstate.NewComparer(dir, cliGitBudget).Compare(m.Branch.String, m.CommitHash.String)

			fmt.Printf("id:        %d\n", m.ID)
			fmt.Printf("kind:      %s\n", m.Kind)
			fmt.Printf("origin:    %s\n", m.Origin)
			status := m.Status
			if m.SupersededBy.Valid {
				status += fmt.Sprintf(" (by %d)", m.SupersededBy.Int64)
			}
			if m.Edited {
				status += ", edited"
			}
			fmt.Printf("status:    %s\n", status)
			fmt.Printf("captured:  %s\n", m.CapturedAt)
			branch, commit := m.Branch.String, m.CommitHash.String
			if !m.CommitHash.Valid {
				branch, commit = "-", "no repository"
			} else if branch == "" {
				branch = "(detached)"
			}
			if len(commit) > 12 && commit != "no repository" {
				commit = commit[:12]
			}
			fmt.Printf("git state: %s @ %s\n", branch, commit)
			fmt.Printf("freshness: %s\n", fresh.Render())
			fmt.Printf("content:   %s\n", m.Content)
			return nil
		},
	}
}

func newMemoryEditCmd() *cobra.Command {
	var content string
	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Edit a memory entry's content ($EDITOR or --content)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseMemoryID(args[0])
			if err != nil {
				return err
			}
			st, err := store.Open()
			if err != nil {
				return err
			}
			defer st.Close()
			m, err := st.GetMemory(id)
			if err != nil {
				return err
			}
			text := content
			if text == "" {
				text, err = editInEditor(m.Content)
				if err != nil {
					return err
				}
			}
			if strings.TrimSpace(text) == strings.TrimSpace(m.Content) {
				fmt.Println("Unchanged.")
				return nil
			}
			if err := memory.Edit(st, id, text); err != nil {
				return err
			}
			fmt.Printf("Updated memory %d.\n", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&content, "content", "", "new content (skips the editor)")
	return cmd
}

func editInEditor(initial string) (string, error) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		return "", errors.New("$EDITOR is not set; use --content")
	}
	f, err := os.CreateTemp("", "agent-brain-memory-*.md")
	if err != nil {
		return "", err
	}
	path := f.Name()
	defer func() { _ = os.Remove(path) }()
	if _, err := f.WriteString(initial); err != nil {
		f.Close()
		return "", err
	}
	f.Close()
	c := exec.Command(editor, path)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("editor: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func newMemoryDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Permanently delete a memory entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseMemoryID(args[0])
			if err != nil {
				return err
			}
			st, err := store.Open()
			if err != nil {
				return err
			}
			defer st.Close()
			m, err := st.GetMemory(id)
			if err != nil {
				return err
			}
			if !yes {
				fmt.Printf("Delete memory %d (%q)? It stops being served now; restore it with `agent-brain memory restore %d`. [y/N] ", id, firstLine(m.Content), id)
				if !confirmYes() {
					fmt.Println("Kept.")
					return nil
				}
			}
			if err := st.DeleteMemory(id, store.Now()); err != nil {
				return err
			}
			fmt.Printf("Deleted memory %d (restore with `agent-brain memory restore %d`).\n", id, id)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func newMemoryWipeCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "wipe",
		Short: "Delete ALL memory for this project (restorable per entry)",
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store.Open()
			if err != nil {
				return err
			}
			defer st.Close()
			projectID, identity, found, err := currentProject(st)
			if err != nil {
				return err
			}
			if !found {
				fmt.Printf("No memory for %s.\n", identity)
				return nil
			}
			if !yes {
				name := filepath.Base(identity)
				fmt.Printf("Type the project name (%s) to wipe ALL its memory: ", name)
				answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if strings.TrimSpace(answer) != name {
					fmt.Println("Name did not match; nothing wiped.")
					return nil
				}
			}
			n, err := st.WipeProjectMemories(projectID, store.Now())
			if err != nil {
				return err
			}
			fmt.Printf("Wiped %d memory entries for %s (each restorable with `agent-brain memory restore <id>`).\n", n, identity)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func newMemoryRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <id>",
		Short: "Re-activate a superseded or deleted memory entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseMemoryID(args[0])
			if err != nil {
				return err
			}
			st, err := store.Open()
			if err != nil {
				return err
			}
			defer st.Close()
			if err := st.RestoreMemory(id, store.Now()); err != nil {
				if errors.Is(err, store.ErrMemoryNotFound) {
					return fmt.Errorf("memory %d is not a superseded or deleted entry", id)
				}
				return err
			}
			fmt.Printf("Restored memory %d to active.\n", id)
			return nil
		},
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}

func confirmYes() bool {
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	a := strings.ToLower(strings.TrimSpace(answer))
	return a == "y" || a == "yes"
}
