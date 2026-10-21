package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ebrahim5801/agent-brain-cli/internal/fsutil"
	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
	"github.com/ebrahim5801/agent-brain-cli/internal/redact"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

var exportHeadings = map[string]string{
	"decision":   "Decisions",
	"convention": "Conventions",
	"task_state": "Task notes",
	"fact":       "Facts",
}

func newMemoryExportCmd() *cobra.Command {
	var out string
	var all bool
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Write this project's memories as Markdown (stdout, or --out FILE)",
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
			var entries []memory.Ranked
			if found {
				dir, _ := os.Getwd()
				if entries, err = memory.List(st, projectID, dir, all, cliGitBudget); err != nil {
					return err
				}
			}
			text := renderMemoryExport(identity, entries, store.Now())
			if out == "" {
				fmt.Print(text)
				return nil
			}
			if err := writePrivateFile(out, []byte(text)); err != nil {
				return err
			}
			fmt.Printf("Wrote %d entries to %s\n", len(entries), out)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "write to this file instead of stdout")
	cmd.Flags().BoolVar(&all, "all", false, "include superseded and deleted entries")
	return cmd
}

// writePrivateFile replaces path with a file only its owner can read, since
// memories can hold private project details. It writes a new temp file (created
// 0600) and renames it over path, so an existing wider file or a symlink at
// path is replaced rather than written through.
func writePrivateFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".agent-brain-export-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = fsutil.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// renderMemoryExport is a readable snapshot, one section per kind, retired
// entries last. It is not read back: edits go through `memory edit`.
func renderMemoryExport(identity string, entries []memory.Ranked, at string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Project memory: %s\n\n", identity)
	fmt.Fprintf(&b, "Exported %s. Change an entry with `agent-brain memory edit <id>`; edits to this file are not read back.\n", at)
	byKind := map[string][]memory.Ranked{}
	var retired []memory.Ranked
	for _, e := range entries {
		if e.Entry.Status != "" && e.Entry.Status != store.MemoryActive {
			retired = append(retired, e)
			continue
		}
		byKind[e.Entry.Kind] = append(byKind[e.Entry.Kind], e)
	}
	if len(entries) == 0 {
		b.WriteString("\nNo memories yet.\n")
	}
	for _, kind := range memory.Kinds {
		writeExportSection(&b, exportHeadings[kind], byKind[kind])
	}
	writeExportSection(&b, "Retired", retired)
	return b.String()
}

func writeExportSection(b *strings.Builder, heading string, entries []memory.Ranked) {
	if len(entries) == 0 {
		return
	}
	fmt.Fprintf(b, "\n## %s\n", heading)
	for _, r := range entries {
		m := r.Entry
		meta := []string{fmt.Sprintf("#%d", m.ID)}
		if heading == "Retired" {
			meta = append(meta, m.Kind)
			status := m.Status
			if m.SupersededBy.Valid {
				status += fmt.Sprintf(" by #%d", m.SupersededBy.Int64)
			}
			meta = append(meta, status)
		}
		if m.Priority != "" && m.Priority != store.MemoryPriorityNormal {
			meta = append(meta, m.Priority)
		}
		meta = append(meta, m.Origin, exportDate(m))
		fmt.Fprintf(b, "\n### %s\n\n%s\n", strings.Join(meta, " · "), redact.Apply(m.Content))
		if m.Details.Rationale != "" {
			fmt.Fprintf(b, "\nWhy: %s\n", redact.Apply(m.Details.Rationale))
		}
		if m.Details.Scope != "" {
			fmt.Fprintf(b, "\nScope: %s\n", redact.Apply(m.Details.Scope))
		}
	}
}

// exportDate is the day an entry was last written.
func exportDate(m store.Memory) string {
	at := m.CapturedAt
	if m.UpdatedAt > at {
		at = m.UpdatedAt
	}
	if len(at) >= 10 {
		return at[:10]
	}
	return at
}
