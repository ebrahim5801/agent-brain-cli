package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
)

// gitCheckout makes a directory that looks like a git checkout (a .git
// directory at its root) with the given subfolders.
func gitCheckout(t *testing.T, subdirs ...string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range subdirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// Checkout-scoped memories and artifact evidence are keyed by the exact
// folder, so CheckoutIdentity must not widen a subfolder to its repository.
// Only session bindings are matched by repository root.
func TestCheckoutIdentityKeepsTheFolder(t *testing.T) {
	root := gitCheckout(t, filepath.Join("apps", "api"))
	sub := filepath.Join(root, "apps", "api")
	if store.CheckoutIdentity(sub) == store.CheckoutIdentity(root) {
		t.Fatal("CheckoutIdentity widened a subfolder to the repository root")
	}
}

// One Claude Code session keeps one handle while its shell moves between
// folders of the same checkout, and the MCP server (started at the root)
// accepts that handle.
func TestShellDirectoryChangesKeepOneBinding(t *testing.T) {
	for _, b := range storetest.Both(t) {
		t.Run(b.Name, func(t *testing.T) {
			st := b.Store
			if err := st.UpgradeMemoryV2(); err != nil {
				t.Fatal(err)
			}
			root := gitCheckout(t, filepath.Join("apps", "web"), filepath.Join("apps", "api"))
			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "repo_root", Identity: root}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			first, err := st.EnsureBoundSession(pid, "claude-code", "native-1", root, "", "")
			if err != nil {
				t.Fatal(err)
			}
			for _, dir := range []string{filepath.Join(root, "apps", "web"), filepath.Join(root, "apps", "api")} {
				again, err := st.EnsureBoundSession(pid, "claude-code", "native-1", dir, "", "")
				if err != nil {
					t.Fatal(err)
				}
				if again.Handle != first.Handle || again.SessionID != first.SessionID {
					t.Fatalf("cd into %s created handle %s, want %s", dir, again.Handle, first.Handle)
				}
			}
			var bindings int
			if err := st.QueryRow(`SELECT COUNT(*) FROM memory_session_bindings WHERE native_id='native-1'`).Scan(&bindings); err != nil {
				t.Fatal(err)
			}
			if bindings != 1 {
				t.Fatalf("%d bindings for one session, want 1", bindings)
			}
			var stored string
			if err := st.QueryRow(`SELECT checkout FROM memory_session_bindings WHERE handle=?`, first.Handle).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if stored != store.CheckoutIdentity(root) {
				t.Fatalf("binding stored %q, want the folder the session started in", stored)
			}
			if _, err := st.ResolveBinding(first.Handle, pid, root); err != nil {
				t.Fatalf("MCP server at the root rejected the handle: %v", err)
			}
			if _, err := st.ResolveBinding(first.Handle, pid, filepath.Join(root, "apps", "api")); err != nil {
				t.Fatalf("MCP server in a subfolder rejected the handle: %v", err)
			}
			other := gitCheckout(t)
			if _, err := st.ResolveBinding(first.Handle, pid, other); err == nil {
				t.Fatal("a handle from another checkout was accepted")
			}
		})
	}
}

// A session that starts in a subfolder stores that folder: batch saves
// resolve artifact evidence against it, as the MCP server started there does.
func TestSubfolderSessionStoresItsStartFolder(t *testing.T) {
	for _, b := range storetest.Both(t) {
		t.Run(b.Name, func(t *testing.T) {
			st := b.Store
			if err := st.UpgradeMemoryV2(); err != nil {
				t.Fatal(err)
			}
			root := gitCheckout(t, filepath.Join("apps", "web"))
			web := filepath.Join(root, "apps", "web")
			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "repo_root", Identity: root}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			bound, err := st.EnsureBoundSession(pid, "claude-code", "native-web", web, "", "")
			if err != nil {
				t.Fatal(err)
			}
			var stored string
			if err := st.QueryRow(`SELECT checkout FROM memory_session_bindings WHERE handle=?`, bound.Handle).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if stored != store.CheckoutIdentity(web) {
				t.Fatalf("binding stored %q, want %q", stored, store.CheckoutIdentity(web))
			}
			if _, err := st.ResolveBinding(bound.Handle, pid, web); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Bindings written by 0.5.5 and earlier recorded the shell's folder. Their
// handles stay valid at the repository root, and a session that only has such
// a row keeps using it instead of gaining another one.
func TestLegacySubfolderBindingsStillResolve(t *testing.T) {
	for _, b := range storetest.Both(t) {
		t.Run(b.Name, func(t *testing.T) {
			st := b.Store
			if err := st.UpgradeMemoryV2(); err != nil {
				t.Fatal(err)
			}
			root := gitCheckout(t, filepath.Join("services", "worker"))
			sub := store.CheckoutIdentity(filepath.Join(root, "services", "worker"))
			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "repo_root", Identity: root}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			var sessionID int64
			if err := st.QueryRow(`INSERT INTO sessions(external_id,project_id,assistant,started_at,sync_uid) VALUES('v2:legacy-handle',?,'claude-code',?,'legacy-sync') RETURNING id`, pid, store.Now()).Scan(&sessionID); err != nil {
				t.Fatal(err)
			}
			if _, err := st.Exec(`INSERT INTO memory_session_bindings(handle,project_id,session_id,assistant,native_id,checkout,provenance,created_at) VALUES('legacy-handle',?,?,'claude-code','native-legacy',?,'native',?)`, pid, sessionID, sub, store.Now()); err != nil {
				t.Fatal(err)
			}
			if _, err := st.ResolveBinding("legacy-handle", pid, root); err != nil {
				t.Fatalf("legacy subfolder handle rejected at the root: %v", err)
			}
			again, err := st.EnsureBoundSession(pid, "claude-code", "native-legacy", root, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if again.Handle != "legacy-handle" {
				t.Fatalf("legacy session got a new handle %s", again.Handle)
			}
		})
	}
}
