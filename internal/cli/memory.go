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

	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
	"github.com/ebrahim5801/agent-brain-cli/internal/gitstate"
	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/spf13/cobra"
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
		newMemoryHistoryCmd(),
		newMemoryV2StatusCmd(),
		newMemoryReviewCmd(),
		newMemoryUpgradeV2Cmd(),
		newMemoryEnableCmd(),
		newMemoryDisableCmd(),
		newMemoryListCmd(),
		newMemoryShowCmd(),
		newMemoryExportCmd(),
		newMemoryImportCmd(),
		newMemoryEditCmd(),
		newMemoryPriorityCmd(),
		newMemoryDeleteCmd(),
		newMemoryWipeCmd(),
		newMemoryRestoreCmd(),
		newMemoryShareCmd(),
		newMemoryPauseCmd(),
		newMemoryResumeCmd(),
		newMemoryCaptureCmd(),
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
	var priority string
	var critical bool
	var asJSON bool
	var limit int
	var offset int
	var session string
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
			if critical {
				priority = memory.PriorityCritical
			}
			if priority != "" && !memory.ValidPriority(priority) {
				return fmt.Errorf("unknown priority %q; want one of: %s", priority, strings.Join(memory.Priorities, ", "))
			}
			if kind != "" && !memory.ValidKind(kind) {
				return fmt.Errorf("unknown kind %q; want one of: %s", kind, strings.Join(memory.Kinds, ", "))
			}
			if session != "" {
				sessions, err := st.MemorySessions(projectID)
				if err != nil {
					return err
				}
				match, err := memory.ResolveSession(sessions, session)
				if err != nil {
					return err
				}
				var mine []memory.Ranked
				for _, r := range entries {
					if r.Entry.SessionID.Valid && r.Entry.SessionID.Int64 == match.ID {
						mine = append(mine, r)
					}
				}
				entries = mine
			}
			if kind != "" || priority != "" {
				filter := memory.Filter{Kind: kind, Priority: priority}
				var filtered []memory.Ranked
				for _, r := range entries {
					if filter.Matches(r.Entry) {
						filtered = append(filtered, r)
					}
				}
				entries = filtered
			}
			page, err := memory.PageItems(entries, memory.Page{Limit: limit, Offset: offset})
			if err != nil {
				return err
			}
			entries = page.Items
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
			if footer := page.RangeFooter(); footer != "" {
				if page.HasMore {
					footer += fmt.Sprintf(" Next: agent-brain memory list --offset %d (or --limit 0 for all)", page.To)
				}
				fmt.Println(footer)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include superseded and deleted entries")
	cmd.Flags().StringVar(&kind, "kind", "", "filter by kind (decision, convention, task_state, fact)")
	cmd.Flags().StringVar(&priority, "priority", "", "filter by priority (critical, normal, background)")
	cmd.Flags().BoolVar(&critical, "critical", false, "only critical entries (shorthand for --priority critical)")
	cmd.MarkFlagsMutuallyExclusive("critical", "priority")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum entries to show (0 for all)")
	cmd.Flags().IntVar(&offset, "offset", 0, "number of matching entries to skip")
	cmd.Flags().StringVar(&session, "session", "", "only entries saved by this session (its ID or first 8+ characters, from `memory show`)")
	return cmd
}

type memoryJSON struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"`
	Origin     string `json:"origin"`
	Priority   string `json:"priority"`
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
			ID: r.Entry.ID, Kind: r.Entry.Kind, Origin: r.Entry.Origin, Priority: r.Entry.Priority, Status: r.Entry.Status,
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
			pid, _, found, err := currentProject(st)
			if err != nil {
				return err
			}
			if !found || pid != m.ProjectID {
				return store.ErrMemoryNotFound
			}
			dir, _ := os.Getwd()
			entries, err := memory.List(st, pid, dir, true, cliGitBudget)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if entry.Entry.ID == id {
					m = entry.Entry
					break
				}
			}
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
			fmt.Printf("verification: %s (last checked %s)\n", m.Verification, m.VerifiedAt)
			if m.Details.Rationale != "" {
				fmt.Printf("rationale: %s\n", m.Details.Rationale)
			}
			fmt.Printf("captured:  %s\n", m.CapturedAt)
			if m.SessionID.Valid {
				sessions, err := st.MemorySessions(pid)
				if err != nil {
					return err
				}
				for _, ms := range sessions {
					if ms.ID == m.SessionID.Int64 {
						fmt.Printf("session:   %s (started %s)\n", memory.SessionLabel(ms.ExternalID), ms.StartedAt)
					}
				}
			}
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

func newMemoryPriorityCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "priority <id> <critical|normal|background>",
		Short: "Reclassify how hard an entry competes for the session-start memory budget",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if handle := strings.TrimPrefix(args[0], "team#"); handle != args[0] {
				return fmt.Errorf("team#%s is a teammate's entry; its priority is set by its author. "+
					"To disagree with it, save your own entry with supersedes_team: [\"team#%s\"]", handle, handle)
			}
			id, err := parseMemoryID(args[0])
			if err != nil {
				return err
			}
			st, err := store.Open()
			if err != nil {
				return err
			}
			defer st.Close()
			shared, err := st.MemoryShared(id)
			if err != nil {
				return err
			}
			if err := memory.SetPriority(st, id, args[1]); err != nil {
				return err
			}
			fmt.Printf("Memory %d is now %s.\n", id, args[1])
			if shared {
				fmt.Println("Note: this entry is already shared with the team; the change is local. " +
					"Teammates keep the priority it was contributed with.")
			}
			return nil
		},
	}
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
	var session string
	cmd := &cobra.Command{
		Use:   "delete <id> | --session <id>",
		Short: "Soft-delete a memory entry, or everything one session saved (restorable)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (len(args) == 1) == (session != "") {
				return fmt.Errorf("give either a memory id or --session, not both")
			}
			if session != "" {
				return deleteSessionMemories(session, yes)
			}
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
	cmd.Flags().StringVar(&session, "session", "", "delete every active entry this session saved (its ID or first 8+ characters, from `memory show`) and bring back the entries they replaced")
	return cmd
}

// deleteSessionMemories undoes one session's memory: it soft-deletes the
// session's active entries and restores the entries from other sessions that
// they replaced, which would otherwise stay hidden.
func deleteSessionMemories(ref string, yes bool) error {
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
	sessions, err := st.MemorySessions(projectID)
	if err != nil {
		return err
	}
	match, err := memory.ResolveSession(sessions, ref)
	if err != nil {
		return err
	}
	dir, _ := os.Getwd()
	entries, err := memory.List(st, projectID, dir, true, cliGitBudget)
	if err != nil {
		return err
	}
	plan := memory.PlanSessionCleanup(entries, match.ID)
	if len(plan.Delete) == 0 && len(plan.Restore) == 0 {
		fmt.Printf("Session %s (started %s) has no active memories left.\n", memory.SessionLabel(match.ExternalID), match.StartedAt)
		return nil
	}
	fmt.Printf("Session %s (started %s) saved %d active memories:\n", memory.SessionLabel(match.ExternalID), match.StartedAt, len(plan.Delete))
	for _, m := range plan.Delete {
		fmt.Printf("  #%d %s: %s\n", m.ID, m.Kind, firstLine(m.Content))
	}
	if len(plan.Restore) > 0 {
		fmt.Printf("They replaced %d older memories, which come back:\n", len(plan.Restore))
		for _, m := range plan.Restore {
			fmt.Printf("  #%d %s: %s\n", m.ID, m.Kind, firstLine(m.Content))
		}
	}
	if !yes {
		fmt.Print("Delete them? Each one can be restored with `agent-brain memory restore <id>`. [y/N] ")
		if !confirmYes() {
			fmt.Println("Kept.")
			return nil
		}
	}
	at := store.Now()
	for i, m := range plan.Delete {
		if err := st.DeleteMemory(m.ID, at); err != nil {
			return fmt.Errorf("deleted %d of %d, then memory %d failed: %w", i, len(plan.Delete), m.ID, err)
		}
	}
	for i, m := range plan.Restore {
		if err := st.RestoreMemory(m.ID, at); err != nil {
			return fmt.Errorf("deleted %d; restored %d of %d, then memory %d failed: %w", len(plan.Delete), i, len(plan.Restore), m.ID, err)
		}
	}
	fmt.Printf("Deleted %d memories from session %s", len(plan.Delete), memory.SessionLabel(match.ExternalID))
	if len(plan.Restore) > 0 {
		fmt.Printf(" and restored %d they had replaced", len(plan.Restore))
	}
	fmt.Println(". Restore any of them with `agent-brain memory restore <id>`.")
	return nil
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
