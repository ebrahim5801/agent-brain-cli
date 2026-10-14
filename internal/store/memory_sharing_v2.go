package store

// Sharing retains the existing wire format. Only globally applicable effective
// heads may be contributed; checkout/branch evidence is local. Accepted global
// replacement edges retain the existing team UID retirement mechanism.
func (s *Store) filterContributions(projectID int64, pending []PendingMemory) ([]PendingMemory, error) {
	snap, err := s.MemorySnapshot(projectID, nil)
	if err != nil {
		return nil, err
	}
	rows, err := s.Query(`SELECT id,team_uid FROM memories WHERE project_id=? AND team_uid IS NOT NULL`, projectID)
	if err != nil {
		return nil, err
	}
	ids := map[string]int64{}
	uids := map[int64]string{}
	for rows.Next() {
		var id int64
		var uid string
		if err := rows.Scan(&id, &uid); err != nil {
			rows.Close()
			return nil, err
		}
		ids[uid] = id
		uids[id] = uid
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	active := map[int64]bool{}
	for _, m := range snap.Entries {
		active[m.ID] = m.Status == MemoryActive && m.Details.Scope == ""
	}
	var out []PendingMemory
	for _, p := range pending {
		if !active[ids[p.UID]] {
			continue
		}
		seen := map[string]bool{}
		for _, u := range p.Supersedes {
			seen[u] = true
		}
		for _, r := range snap.Relations {
			u := uids[r.TargetID]
			if r.State == "accepted" && r.Type == "supersedes" && r.Scope == "" && r.SourceID == ids[p.UID] && u != "" && !seen[u] {
				p.Supersedes = append(p.Supersedes, u)
				seen[u] = true
			}
		}
		out = append(out, p)
	}
	return out, nil
}
