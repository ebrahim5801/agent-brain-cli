package syncer

import (
	"strings"
	"testing"
)

func TestRejectionErrorNamesSingleProject(t *testing.T) {
	err := rejectionError(8, map[string]int{"not_authorized": 8}, map[string]int{"dir:/var/www/html/Sync/nexustate": 8})
	msg := err.Error()
	if !strings.Contains(msg, "not authorized") {
		t.Fatalf("want not-authorized wording, got %q", msg)
	}
	if !strings.Contains(msg, "dir:/var/www/html/Sync/nexustate (8 record(s))") {
		t.Fatalf("want the offending project named, got %q", msg)
	}
}

func TestRejectionErrorListsMultipleProjectsSorted(t *testing.T) {
	err := rejectionError(5, map[string]int{"not_authorized": 5}, map[string]int{
		"dir:/b": 2,
		"dir:/a": 3,
	})
	msg := err.Error()
	if !strings.Contains(msg, "these projects: dir:/a (3), dir:/b (2)") {
		t.Fatalf("want both projects listed in sorted order, got %q", msg)
	}
}

func TestRejectionErrorFallsBackWithoutBreakdown(t *testing.T) {
	err := rejectionError(1, map[string]int{"not_authorized": 1}, nil)
	if !strings.Contains(err.Error(), "the linked project") {
		t.Fatalf("want generic fallback when no per-project data, got %q", err.Error())
	}
}

func TestRejectionErrorNonAuthStillNamesProject(t *testing.T) {
	err := rejectionError(3, map[string]int{"invalid_record": 3}, map[string]int{"dir:/x": 3})
	msg := err.Error()
	if strings.Contains(msg, "not authorized") {
		t.Fatalf("non-auth rejection should not claim authorization failure, got %q", msg)
	}
	if !strings.Contains(msg, "dir:/x (3 record(s))") {
		t.Fatalf("want project named for generic rejection too, got %q", msg)
	}
}
