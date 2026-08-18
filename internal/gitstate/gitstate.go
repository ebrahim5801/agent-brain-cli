// Package gitstate records a project's version-control state at memory
// capture time and computes freshness signals at serve time by comparing a
// capture-time state to the session's current state (spec FR-004, FR-013,
// FR-014). Capture is pure file reads (hook-path cheap, like attribution);
// comparison may shell out to the system git binary under a strict time
// budget and degrades conservatively when git is absent, slow, or the
// repository is in a non-standard state — it never errors.
package gitstate

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type State struct {
	HasRepo bool
	Branch  string // "" when detached or unresolvable
	Commit  string // "" when unresolvable
}

type Signal string

const (
	Current         Signal = "current"
	MovedOn         Signal = "moved-on"
	DifferentBranch Signal = "different-branch"
	Unverifiable    Signal = "unverifiable"
	Unknown         Signal = "unknown"
)

type Freshness struct {
	Signal Signal
	// Branch is the capture-time branch, set for different-branch.
	Branch string
	// CommitsSince is how far HEAD moved past the capture commit; -1 when
	// the distance could not be determined.
	CommitsSince int
}

// Render is the canonical serving-format token (contracts/mcp-memory.md).
func (f Freshness) Render() string {
	switch f.Signal {
	case Current:
		return "current"
	case MovedOn:
		if f.CommitsSince >= 0 {
			return "moved-on: " + strconv.Itoa(f.CommitsSince) + " commits since"
		}
		return "moved-on: distance unknown"
	case DifferentBranch:
		return "different-branch: " + f.Branch
	case Unverifiable:
		return "unverifiable: capture point no longer exists"
	default:
		return "unknown: no git state"
	}
}

// Capture reads the repository state for dir with pure file reads: HEAD for
// the branch (or detached commit), then the loose ref or packed-refs for the
// commit. Any unreadable layer degrades to the zero value for that field.
func Capture(dir string) State {
	gitDir := findGitDir(dir)
	if gitDir == "" {
		return State{}
	}
	st := State{HasRepo: true}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return st
	}
	line := strings.TrimSpace(string(head))
	if ref, ok := strings.CutPrefix(line, "ref: "); ok {
		ref = strings.TrimSpace(ref)
		st.Branch = strings.TrimPrefix(ref, "refs/heads/")
		st.Commit = resolveRef(gitDir, ref)
		return st
	}
	st.Commit = line // detached HEAD
	return st
}

// Comparer computes freshness for many entries against one current state,
// sharing a single git-exec time budget across all comparisons.
type Comparer struct {
	dir      string
	current  State
	deadline time.Time
	gitPath  string
}

// NewComparer captures the current state of dir once. budget bounds the total
// wall-clock spent in git subprocesses across every Compare call.
func NewComparer(dir string, budget time.Duration) *Comparer {
	c := &Comparer{dir: dir, current: Capture(dir), deadline: time.Now().Add(budget)}
	c.gitPath, _ = exec.LookPath("git")
	return c
}

func (c *Comparer) CurrentState() State { return c.current }

// Compare maps a capture-time (branch, commit) to a freshness signal per the
// R4 decision table.
func (c *Comparer) Compare(branch, commit string) Freshness {
	if !c.current.HasRepo || commit == "" || c.current.Commit == "" {
		return Freshness{Signal: Unknown, CommitsSince: -1}
	}
	if commit == c.current.Commit {
		return Freshness{Signal: Current}
	}
	if branch != "" && c.current.Branch != "" && branch != c.current.Branch {
		return Freshness{Signal: DifferentBranch, Branch: branch, CommitsSince: -1}
	}
	// Same branch (or detached somewhere): HEAD moved. Ancestry decides
	// moved-on vs unverifiable; without usable git, degrade to moved-on
	// with unknown distance.
	switch c.isAncestor(commit) {
	case ancestorYes:
		return Freshness{Signal: MovedOn, CommitsSince: c.countSince(commit)}
	case ancestorNo:
		return Freshness{Signal: Unverifiable, CommitsSince: -1}
	default:
		return Freshness{Signal: MovedOn, CommitsSince: -1}
	}
}

type ancestry int

const (
	ancestorUnknown ancestry = iota
	ancestorYes
	ancestorNo
)

func (c *Comparer) isAncestor(commit string) ancestry {
	out, code, ok := c.git("merge-base", "--is-ancestor", commit, "HEAD")
	_ = out
	if !ok {
		return ancestorUnknown
	}
	switch code {
	case 0:
		return ancestorYes
	case 1:
		return ancestorNo
	default:
		// Unknown revision (rewritten history, shallow clone) exits > 1.
		return ancestorNo
	}
}

func (c *Comparer) countSince(commit string) int {
	out, code, ok := c.git("rev-list", "--count", commit+"..HEAD")
	if !ok || code != 0 {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return -1
	}
	return n
}

// git runs a git plumbing command inside the remaining budget. ok is false
// when git is unavailable, the budget is spent, or the command timed out.
func (c *Comparer) git(args ...string) (stdout string, exitCode int, ok bool) {
	if c.gitPath == "" {
		return "", 0, false
	}
	remaining := time.Until(c.deadline)
	if remaining <= 0 {
		return "", 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), remaining)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.gitPath, args...)
	cmd.Dir = c.dir
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", 0, false
	}
	if err != nil {
		if ee, isExit := err.(*exec.ExitError); isExit {
			return string(out), ee.ExitCode(), true
		}
		return "", 0, false
	}
	return string(out), 0, true
}

// findGitDir walks up from dir to the enclosing repository's git directory,
// resolving worktree/submodule .git files (same approach as attribution).
func findGitDir(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	for d := dir; ; {
		g := filepath.Join(d, ".git")
		if fi, err := os.Stat(g); err == nil {
			if fi.IsDir() {
				return g
			}
			if resolved := resolveGitFile(g, d); resolved != "" {
				return resolved
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

func resolveGitFile(gitFile, repoDir string) string {
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	g, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return ""
	}
	g = strings.TrimSpace(g)
	if !filepath.IsAbs(g) {
		g = filepath.Join(repoDir, g)
	}
	return g
}

// resolveRef reads a ref's commit from the loose ref file, falling back to
// packed-refs; worktree git dirs keep shared refs in commondir.
func resolveRef(gitDir, ref string) string {
	dirs := []string{gitDir}
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		common := strings.TrimSpace(string(data))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
		dirs = append(dirs, common)
	}
	for _, d := range dirs {
		if data, err := os.ReadFile(filepath.Join(d, ref)); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	for _, d := range dirs {
		if commit := packedRef(filepath.Join(d, "packed-refs"), ref); commit != "" {
			return commit
		}
	}
	return ""
}

func packedRef(path, ref string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' || line[0] == '^' {
			continue
		}
		commit, name, ok := strings.Cut(line, " ")
		if ok && name == ref {
			return commit
		}
	}
	return ""
}
