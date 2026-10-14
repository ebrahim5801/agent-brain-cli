package memory

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// ReviewSources checks only supplied memory IDs in their original local source.
// Successful historical verification is not a claim that the claim remains true
// forever. A changed/missing source appends failure evidence, without undoing
// historical decisions or rewriting their rows. Unsupported sources abstain.
func ReviewSources(st *store.Store, projectID int64, dir string, ids []int64) (map[int64]string, error) {
	if !st.MemoryV2 {
		return nil, store.ErrV2Required
	}
	if len(ids) > 16 {
		return nil, ErrContentTooLong
	}
	out := map[int64]string{}
	for _, id := range ids {
		m, err := st.GetMemory(id)
		if err != nil {
			return nil, err
		}
		if m.ProjectID != projectID {
			return nil, store.ErrMemoryNotFound
		}
		rows, err := st.Query(`SELECT source_type,locator,digest,status FROM memory_evidence WHERE project_id=? AND memory_id=?`, projectID, id)
		if err != nil {
			return nil, err
		}
		type ref struct{ kind, locator, digest, status string }
		var refs []ref
		for rows.Next() {
			var r ref
			if err := rows.Scan(&r.kind, &r.locator, &r.digest, &r.status); err != nil {
				rows.Close()
				return nil, err
			}
			refs = append(refs, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		status := "unverified"
		checked := false
		for _, r := range refs {
			if r.status == "unverified" {
				continue
			}
			checked = true
			ok := false
			switch r.kind {
			case "artifact":
				ok = artifactMatches(dir, r.locator, r.digest)
			case "user_message":
				var host, path string
				if err := st.QueryRow(`SELECT b.assistant,COALESCE(s.transcript_path,'') FROM memory_details d JOIN memory_session_bindings b ON b.handle=d.source_handle JOIN sessions s ON s.id=b.session_id WHERE d.memory_id=? AND d.project_id=?`, id, projectID).Scan(&host, &path); err == nil {
					_, ok = assistant.ReadUserReference(host, path, r.locator, r.digest)
				}
			}
			if !ok {
				status = "verification_failed"
				break
			}
			status = "source_checked"
		}
		if checked && status == "verification_failed" {
			if err := st.RecordVerificationReview(projectID, id, false); err != nil {
				return nil, err
			}
		}
		out[id] = status
	}
	return out, nil
}

func artifactMatches(dir, locator, digest string) bool {
	if filepath.IsAbs(locator) || locator == "" {
		return false
	}
	root := store.CheckoutIdentity(dir)
	path, err := filepath.EvalSymlinks(filepath.Join(root, locator))
	if err != nil || !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return false
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 32768 {
		return false
	}
	raw, err := os.ReadFile(path)
	return err == nil && digestText(string(raw)) == digest
}
