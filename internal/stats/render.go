package stats

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"unicode"
	"unicode/utf8"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

const summaryWrapWidth = 100

const emptyState = `No sessions recorded yet.

Data appears automatically after your first Claude Code session.
Run one, then check back — or run 'agent-brain status' to verify the
integration is healthy.`

type jsonReport struct {
	Since  string `json:"since,omitempty"`
	Rows   []Row  `json:"rows"`
	Totals Totals `json:"totals"`
}

func RenderJSON(w io.Writer, rows []Row, since string) error {
	if rows == nil {
		rows = []Row{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(jsonReport{Since: since, Rows: rows, Totals: Sum(rows)})
}

// RenderSessions lists individual sessions with their agent-written summaries.
// Each session's full summary is word-wrapped and printed on its own
// indented line(s) below that session's aligned table row.
func RenderSessions(w io.Writer, rows []SessionRow, since string) {
	if len(rows) == 0 {
		fmt.Fprintln(w, emptyState)
		return
	}
	if since != "" {
		fmt.Fprintf(w, "Since %s\n\n", since)
	}

	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tAGENT\tSTARTED\tPROJECT\tTIME\tMODEL\tTOKENS IN\tTOKENS OUT")
	for _, r := range rows {
		started := r.StartedAt
		if len(started) >= 16 {
			started = started[:10] + " " + started[11:16]
		}
		duration := formatDuration(r.DurationSeconds)
		if r.Open {
			duration = "open"
		}
		id := r.ID
		if id == "" {
			id = "-"
		}
		agent := r.AgentType
		if agent == "" {
			if r.ParentID != "" {
				agent = "subagent"
			} else {
				agent = "-"
			}
		}
		model := r.Model
		if n := len(r.Models); n > 1 {
			model = fmt.Sprintf("%s (+%d)", model, n-1)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\n",
			printable(id), printable(agent), started, printable(r.Project), duration,
			printable(model), r.InputTokens, r.OutputTokens)
	}
	tw.Flush()

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	fmt.Fprintln(w, lines[0])
	for i, r := range rows {
		fmt.Fprintln(w, lines[i+1])
		for _, line := range modelBreakdownLines(r.Models) {
			fmt.Fprintf(w, "  %s\n", line)
		}
		if r.Summary == "" {
			continue
		}
		for _, line := range wrapSummary(r.Summary, summaryWrapWidth) {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
}

// modelBreakdownLines renders a session's per-model token split as indented
// lines under its row, one model per line, heaviest first. Returns nothing for
// a single-model session (the MODEL column already names it).
func modelBreakdownLines(models []store.ModelUsage) []string {
	if len(models) < 2 {
		return nil
	}
	out := make([]string, 0, len(models)+1)
	out = append(out, "models used:")
	for _, m := range models {
		total := m.Usage.Input + m.Usage.Output + m.Usage.CacheRead + m.Usage.CacheWrite
		out = append(out, fmt.Sprintf("  %s  %s tok (%d in / %d out)",
			printable(m.Model), formatThousands(total), m.Usage.Input, m.Usage.Output))
	}
	return out
}

// formatThousands renders n with comma thousands separators.
func formatThousands(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// wrapSummary neutralizes control characters in s (keeping newlines) and
// greedily word-wraps the result to width runes per line.
func wrapSummary(s string, width int) []string {
	var out []string
	for _, block := range strings.Split(printableBlock(s), "\n") {
		fields := strings.Fields(block)
		if len(fields) == 0 {
			out = append(out, "")
			continue
		}
		line := fields[0]
		for _, f := range fields[1:] {
			if utf8.RuneCountInString(line)+1+utf8.RuneCountInString(f) > width {
				out = append(out, line)
				line = f
				continue
			}
			line += " " + f
		}
		out = append(out, line)
	}
	return out
}

// RenderSessionPrompts prints the full spawn prompt of each sub-session in
// rows, newest first, matching the order of the sessions table above it.
func RenderSessionPrompts(w io.Writer, rows []SessionRow) {
	printed := false
	for _, r := range rows {
		if r.Prompt == "" {
			continue
		}
		started := r.StartedAt
		if len(started) >= 16 {
			started = started[:10] + " " + started[11:16]
		}
		fmt.Fprintf(w, "\n--- %s  %s  %s ---\n%s\n",
			printable(r.ID), printable(r.AgentType), started, printableBlock(r.Prompt))
		printed = true
	}
	if !printed {
		fmt.Fprintln(w, "\nNo sub-session prompts recorded for this selection.")
	}
}

func RenderTable(w io.Writer, rows []Row, since string, grouped bool) {
	if len(rows) == 0 {
		fmt.Fprintln(w, emptyState)
		return
	}
	if since != "" {
		fmt.Fprintf(w, "Since %s\n\n", since)
	}

	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	if grouped {
		fmt.Fprintln(tw, "PERIOD\tPROJECT\tSESSIONS\tTIME\tTOKENS IN\tTOKENS OUT\tCACHE R\tCACHE W")
	} else {
		fmt.Fprintln(tw, "PROJECT\tSESSIONS\tTIME\tTOKENS IN\tTOKENS OUT\tCACHE R\tCACHE W")
	}
	for _, r := range rows {
		if grouped {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%d\t%d\t%d\t%d\n",
				r.Period, printable(r.Project), r.Sessions, formatDuration(r.DurationSeconds),
				r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheWriteTokens)
		} else {
			fmt.Fprintf(tw, "%s\t%d\t%s\t%d\t%d\t%d\t%d\n",
				printable(r.Project), r.Sessions, formatDuration(r.DurationSeconds),
				r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheWriteTokens)
		}
	}
	t := Sum(rows)
	if grouped {
		fmt.Fprintf(tw, "\tTOTAL\t%d\t%s\t%d\t%d\t%d\t%d\n",
			t.Sessions, formatDuration(t.DurationSeconds),
			t.InputTokens, t.OutputTokens, t.CacheReadTokens, t.CacheWriteTokens)
	} else {
		fmt.Fprintf(tw, "TOTAL\t%d\t%s\t%d\t%d\t%d\t%d\n",
			t.Sessions, formatDuration(t.DurationSeconds),
			t.InputTokens, t.OutputTokens, t.CacheReadTokens, t.CacheWriteTokens)
	}
	tw.Flush()
}

// printable neutralizes terminal control characters (ANSI/OSC escape openers,
// raw tabs, C1 controls): summaries are model-authored and agent types come
// from repo-controlled files, so neither may drive the reader's terminal.
func printable(s string) string {
	if !strings.ContainsFunc(s, unicode.IsControl) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// printableBlock is printable for multi-line text: newlines survive, every
// other control character is neutralized.
func printableBlock(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func formatDuration(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm %ds", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%dh %dm", seconds/3600, (seconds%3600)/60)
}
