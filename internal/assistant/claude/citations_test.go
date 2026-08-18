package claude

import (
	"path/filepath"
	"testing"
)

func TestScanMemoryCitations(t *testing.T) {
	personalIDs, teamHandles, err := ScanMemoryCitations(filepath.Join("testdata", "citations.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	wantPersonal := map[int64]bool{282: true, 17: true}
	if len(personalIDs) != len(wantPersonal) {
		t.Errorf("personalIDs = %v, want %d entries (282 from assistant text, 17 from bare PR reference)", personalIDs, len(wantPersonal))
	}
	for _, id := range personalIDs {
		if !wantPersonal[id] {
			t.Errorf("unexpected personal id %d extracted; user message #282/team#abc12345 and summary #999 must be ignored", id)
		}
	}
	if !contains(personalIDs, 282) {
		t.Errorf("personalIDs = %v, want 282 extracted from assistant text", personalIDs)
	}
	for _, id := range personalIDs {
		if id == 999 {
			t.Error("summary line #999 must be ignored (only type==\"assistant\" lines are scanned)")
		}
	}

	if len(teamHandles) != 1 || teamHandles[0] != "abc12345" {
		t.Errorf("teamHandles = %v, want [abc12345]", teamHandles)
	}
}

func TestScanMemoryCitationsMissingFile(t *testing.T) {
	if _, _, err := ScanMemoryCitations(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Fatal("want error for missing transcript")
	}
}

func contains(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
