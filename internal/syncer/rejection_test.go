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

func TestRejectionErrorDistinguishesUnknownKeyFromNotAuthorized(t *testing.T) {
	unknown := rejectionError(4, map[string]int{"unknown_key": 4}, map[string]int{"dir:/x": 4}).Error()
	if !strings.Contains(unknown, "deleted there") {
		t.Fatalf("unknown_key should advise the project is gone, got %q", unknown)
	}
	if strings.Contains(unknown, "not authorized") {
		t.Fatalf("unknown_key must not be reported as an authorization failure, got %q", unknown)
	}

	rotated := rejectionError(4, map[string]int{"key_rotated": 4}, map[string]int{"dir:/x": 4}).Error()
	if !strings.Contains(rotated, "rotated") {
		t.Fatalf("key_rotated should advise re-linking, got %q", rotated)
	}
	if strings.Contains(rotated, "not authorized") {
		t.Fatalf("key_rotated must not be reported as an authorization failure, got %q", rotated)
	}
}

func TestRejectionErrorReportsSeveralCodesTogether(t *testing.T) {
	msg := rejectionError(6, map[string]int{"unknown_key": 2, "not_authorized": 4}, map[string]int{
		"dir:/a": 2,
		"dir:/b": 4,
	}).Error()
	if !strings.Contains(msg, "deleted there") || !strings.Contains(msg, "not authorized") {
		t.Fatalf("want guidance for both codes, got %q", msg)
	}
	if !strings.Contains(msg, "also,") {
		t.Fatalf("want the two reasons joined, got %q", msg)
	}
}

func TestRejectionErrorSurfacesUnknownCodeVerbatim(t *testing.T) {
	msg := rejectionError(1, map[string]int{"teapot_shortage": 1}, map[string]int{"dir:/x": 1}).Error()
	if !strings.Contains(msg, "teapot_shortage") {
		t.Fatalf("an unrecognized code must be reported verbatim, got %q", msg)
	}
}

func TestRejectionErrorMemoryRefCodeDoesNotAdviseRelinking(t *testing.T) {
	msg := rejectionError(2, map[string]int{"memory_ref_not_authorized": 2}, map[string]int{"dir:/x": 2}).Error()
	if !strings.Contains(msg, "team memory entries") {
		t.Fatalf("want team-memory wording, got %q", msg)
	}
	if strings.Contains(msg, "agent-brain link") {
		t.Fatalf("re-linking does not clear a team-ref rejection, got %q", msg)
	}
}
