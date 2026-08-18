package attribution

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalizeRemote(t *testing.T) {
	cases := map[string]string{
		"git@github.com:ebrahim5801/agent-brain-cli.git":     "github.com/ebrahim5801/agent-brain-cli",
		"https://github.com/ebrahim5801/agent-brain-cli.git": "github.com/ebrahim5801/agent-brain-cli",
		"https://github.com/ebrahim5801/agent-brain-cli":     "github.com/ebrahim5801/agent-brain-cli",
		"https://user:token@github.com/Org/Repo.git":         "github.com/Org/Repo",
		"ssh://git@github.com/org/repo.git":                  "github.com/org/repo",
		"ssh://git@gitlab.com:2222/org/repo.git":             "gitlab.com:2222/org/repo",
		"git@GitHub.com:org/repo.git":                        "github.com/org/repo",
		"https://gitlab.com/group/sub/repo.git":              "gitlab.com/group/sub/repo",
		"https://gitlab.com/org/repo/":                       "gitlab.com/org/repo",
	}
	for in, want := range cases {
		if got := CanonicalizeRemote(in); got != want {
			t.Errorf("CanonicalizeRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

func writeGitRepo(t *testing.T, dir, remoteURL string) {
	t.Helper()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := "[core]\n\trepositoryformatversion = 0\n"
	if remoteURL != "" {
		config += "[remote \"origin\"]\n\turl = " + remoteURL + "\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveRemote(t *testing.T) {
	dir := t.TempDir()
	writeGitRepo(t, dir, "git@gitlab.com:acme/widgets.git")
	sub := filepath.Join(dir, "internal", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, from := range []string{dir, sub} {
		p := Resolve(from)
		if p.Kind != KindRemote {
			t.Fatalf("Resolve(%q).Kind = %q, want remote", from, p.Kind)
		}
		if p.Identity != "gitlab.com/acme/widgets" {
			t.Errorf("identity = %q", p.Identity)
		}
		if p.DisplayName != "widgets" {
			t.Errorf("display name = %q", p.DisplayName)
		}
	}
}

func TestResolveRepoRootWithoutRemote(t *testing.T) {
	dir := t.TempDir()
	writeGitRepo(t, dir, "")
	p := Resolve(dir)
	if p.Kind != KindRepoRoot {
		t.Fatalf("kind = %q, want repo_root", p.Kind)
	}
	if p.Identity != dir {
		t.Errorf("identity = %q, want %q", p.Identity, dir)
	}
}

func TestResolveDirectoryFallback(t *testing.T) {
	dir := t.TempDir()
	p := Resolve(dir)
	if p.Kind != KindDirectory {
		t.Fatalf("kind = %q, want directory", p.Kind)
	}
	if p.Identity != dir {
		t.Errorf("identity = %q, want %q", p.Identity, dir)
	}
	if p.DisplayName != filepath.Base(dir) {
		t.Errorf("display name = %q", p.DisplayName)
	}
}

func TestResolveWorktreeGitFile(t *testing.T) {
	main := t.TempDir()
	writeGitRepo(t, main, "git@gitlab.com:acme/widgets.git")
	wtGitDir := filepath.Join(main, ".git", "worktrees", "wt1")
	if err := os.MkdirAll(wtGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtGitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+wtGitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := Resolve(wt)
	if p.Kind != KindRemote {
		t.Fatalf("kind = %q, want remote (worktree should reach main config)", p.Kind)
	}
	if p.Identity != "gitlab.com/acme/widgets" {
		t.Errorf("identity = %q", p.Identity)
	}
}
