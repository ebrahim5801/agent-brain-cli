package assistant

import "testing"

func scan(text string) ([]int64, []string) {
	var c CitationSet
	c.Scan(text)
	return c.Result()
}

func TestScanRecognizesEveryCitationForm(t *testing.T) {
	ids, handles := scan("Per memory #12 (staging DB), memory#34, [#56] and plain #78, plus team#abc12345.")
	want := []int64{12, 34, 56, 78}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
	if len(handles) != 1 || handles[0] != "abc12345" {
		t.Errorf("handles = %v, want [abc12345]", handles)
	}
}

// An all-digit team handle also matches the bare "#" form of a personal id, so
// team#12345678 used to be reported as personal entry 12345678 as well. Only
// the caller's intersection against actual retrievals kept that from surfacing.
func TestScanDoesNotReadATeamHandleAsAPersonalID(t *testing.T) {
	ids, handles := scan("As team#12345678 says, and team#00000042 agrees.")
	if len(ids) != 0 {
		t.Errorf("personal ids = %v, want none: those digits are team handles", ids)
	}
	if len(handles) != 2 {
		t.Errorf("handles = %v, want both team handles", handles)
	}
}

func TestScanDedupesInFirstSeenOrder(t *testing.T) {
	ids, handles := scan("#9 then #3 then #9 again; team#deadbeef twice: team#deadbeef.")
	if len(ids) != 2 || ids[0] != 9 || ids[1] != 3 {
		t.Errorf("ids = %v, want [9 3]", ids)
	}
	if len(handles) != 1 {
		t.Errorf("handles = %v, want one", handles)
	}
}

func TestScanIgnoresTextWithoutCitations(t *testing.T) {
	ids, handles := scan("## Heading\n#!/bin/sh\n# a comment\nissue # 42 with a space")
	if len(ids) != 0 || len(handles) != 0 {
		t.Errorf("ids = %v handles = %v, want none", ids, handles)
	}
}
