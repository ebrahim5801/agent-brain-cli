// Package attribution resolves which project a session belongs to, using a
// layered identity: git remote URL when available, else repository root path,
// else the working directory path (FR-007). Git metadata is read directly
// from disk — no git binary required, no subprocess latency on the hook path.
package attribution

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	KindRemote    = "remote"
	KindRepoRoot  = "repo_root"
	KindDirectory = "directory"
)

type Project struct {
	Kind        string
	Identity    string
	DisplayName string
}

func Resolve(dir string) Project {
	if dir == "" {
		dir, _ = os.Getwd()
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}

	root, gitDir := findRepo(dir)
	if root == "" {
		return Project{Kind: KindDirectory, Identity: dir, DisplayName: filepath.Base(dir)}
	}
	if remote := originURL(gitDir); remote != "" {
		canon := CanonicalizeRemote(remote)
		return Project{Kind: KindRemote, Identity: canon, DisplayName: repoName(canon)}
	}
	return Project{Kind: KindRepoRoot, Identity: root, DisplayName: filepath.Base(root)}
}

// findRepo walks up from dir looking for a .git entry. Returns the repo root
// and the resolved git directory (handling .git files used by worktrees and
// submodules, including their commondir indirection).
func findRepo(dir string) (root, gitDir string) {
	for d := dir; ; {
		g := filepath.Join(d, ".git")
		if fi, err := os.Stat(g); err == nil {
			if fi.IsDir() {
				return d, g
			}
			if resolved := resolveGitFile(g, d); resolved != "" {
				return d, resolved
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", ""
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
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	g := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if !filepath.IsAbs(g) {
		g = filepath.Join(repoDir, g)
	}
	// Worktrees keep config in the common dir.
	if data, err := os.ReadFile(filepath.Join(g, "commondir")); err == nil {
		common := strings.TrimSpace(string(data))
		if !filepath.IsAbs(common) {
			common = filepath.Join(g, common)
		}
		return filepath.Clean(common)
	}
	return g
}

// originURL extracts the "origin" remote URL from the git config, falling
// back to the first remote when origin is absent.
func originURL(gitDir string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "config"))
	if err != nil {
		return ""
	}
	var inOrigin, inRemote bool
	var firstRemoteURL string
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") {
			lower := strings.ToLower(line)
			inRemote = strings.HasPrefix(lower, `[remote "`)
			inOrigin = lower == `[remote "origin"]`
			continue
		}
		if !inRemote {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found || strings.TrimSpace(key) != "url" {
			continue
		}
		url := strings.TrimSpace(value)
		if inOrigin {
			return url
		}
		if firstRemoteURL == "" {
			firstRemoteURL = url
		}
	}
	return firstRemoteURL
}

// CanonicalizeRemote normalizes a git remote URL so the same repository yields
// one identity from any clone: credentials and scheme stripped, scp-like ssh
// form unified with URL forms, ".git" suffix dropped, host lowercased.
func CanonicalizeRemote(url string) string {
	s := strings.TrimSpace(url)

	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	} else if at := strings.Index(s, "@"); at >= 0 && strings.Index(s, ":") > at {
		// scp-like: user@host:path
		rest := s[at+1:]
		host, path, _ := strings.Cut(rest, ":")
		s = host + "/" + path
	}

	// Strip userinfo remaining in URL forms (user:pass@host/...).
	if at := strings.LastIndex(strings.SplitN(s, "/", 2)[0], "@"); at >= 0 {
		hostPart, rest, _ := strings.Cut(s, "/")
		hostPart = hostPart[strings.LastIndex(hostPart, "@")+1:]
		if rest != "" {
			s = hostPart + "/" + rest
		} else {
			s = hostPart
		}
		_ = at
	}

	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")

	host, path, found := strings.Cut(s, "/")
	if !found {
		return strings.ToLower(host)
	}
	return strings.ToLower(host) + "/" + strings.Trim(path, "/")
}

func repoName(canonical string) string {
	if i := strings.LastIndex(canonical, "/"); i >= 0 {
		return canonical[i+1:]
	}
	return canonical
}
