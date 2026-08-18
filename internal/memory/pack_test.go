package memory

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBuildPackEmptyProject(t *testing.T) {
	st, projectID := openStore(t)
	pack, err := BuildPack(st, projectID, t.TempDir(), 0, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if pack.Text != "" {
		t.Errorf("empty project pack.Text = %q, want empty string", pack.Text)
	}
}

func TestBuildPackFormatAndHeader(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	res, err := Save(st, SaveInput{ProjectID: projectID, Dir: dir, Content: "use pgx directly", Kind: "decision", Origin: OriginExplicit})
	if err != nil {
		t.Fatal(err)
	}

	pack, err := BuildPack(st, projectID, dir, 0, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, must := range []string{
		"## Project memory (agent-brain)",
		"memory_search",
		"memory_save",
		"supersedes:[id]",
	} {
		if !strings.Contains(pack.Text, must) {
			t.Errorf("pack missing %q", must)
		}
	}
	wantLine := "[#" + strconv.FormatInt(res.ID, 10) + "] (decision, explicit, unknown: no git state) use pgx directly"
	if !strings.Contains(pack.Text, wantLine) {
		t.Errorf("pack missing entry line %q in:\n%s", wantLine, pack.Text)
	}
}

func TestBuildPackRespectsBudget(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	long := strings.Repeat("x", 400)
	for i := 0; i < 30; i++ {
		if _, err := Save(st, SaveInput{ProjectID: projectID, Dir: dir, Content: long, Kind: "fact", Origin: OriginAuto}); err != nil {
			t.Fatal(err)
		}
	}

	budgetTokens := 500 // 2000 chars
	pack, err := BuildPack(st, projectID, dir, budgetTokens, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Text) > budgetTokens*4 {
		t.Errorf("pack len %d exceeds budget %d chars", len(pack.Text), budgetTokens*4)
	}
	if entries := strings.Count(pack.Text, "[#"); entries == 0 || entries >= 30 {
		t.Errorf("budgeted pack served %d entries", entries)
	}
}

func TestBuildPackServesOneOversizedEntry(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	if _, err := Save(st, SaveInput{ProjectID: projectID, Dir: dir, Content: strings.Repeat("y", 3000), Kind: "fact", Origin: OriginAuto}); err != nil {
		t.Fatal(err)
	}
	// Budget smaller than header+entry: still serve the single entry.
	pack, err := BuildPack(st, projectID, dir, 100, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(pack.Text, "[#") != 1 {
		t.Errorf("oversized single entry not served:\n%s", pack.Text)
	}
}

func TestBuildPackCounts(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		if _, err := Save(st, SaveInput{ProjectID: projectID, Dir: dir, Content: "fact " + strconv.Itoa(i), Kind: "fact", Origin: OriginAuto}); err != nil {
			t.Fatal(err)
		}
	}
	pack, err := BuildPack(st, projectID, dir, 0, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if pack.Personal != 3 || pack.Team != 0 || pack.Active != 3 || pack.Served() != 3 {
		t.Errorf("counts = %+v, want 3 personal of 3 active", pack)
	}
}

func TestBuildPackCountsReflectBudgetCut(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	long := strings.Repeat("x", 400)
	for i := 0; i < 30; i++ {
		if _, err := Save(st, SaveInput{ProjectID: projectID, Dir: dir, Content: long, Kind: "fact", Origin: OriginAuto}); err != nil {
			t.Fatal(err)
		}
	}
	pack, err := BuildPack(st, projectID, dir, 500, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if pack.Active != 30 {
		t.Errorf("Active = %d, want 30", pack.Active)
	}
	if served := strings.Count(pack.Text, "[#"); served != pack.Served() {
		t.Errorf("Served() = %d but pack renders %d entries", pack.Served(), served)
	}
	if pack.Served() == 0 || pack.Served() >= 30 {
		t.Errorf("budgeted pack served %d entries", pack.Served())
	}
}

func TestBuildPackReportsServedIDsAndUIDs(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	res, err := Save(st, SaveInput{ProjectID: projectID, Dir: dir, Content: "my local decision", Kind: "decision", Origin: OriginExplicit})
	if err != nil {
		t.Fatal(err)
	}
	seedTeam(t, st, projectID, teamRow("team-uid-1", "bob@example.com", "team decision"))

	pack, err := BuildPack(st, projectID, dir, 0, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.PersonalIDs) != 1 || pack.PersonalIDs[0] != res.ID {
		t.Errorf("PersonalIDs = %v, want [%d]", pack.PersonalIDs, res.ID)
	}
	if len(pack.TeamUIDs) != 1 || pack.TeamUIDs[0] != "team-uid-1" {
		t.Errorf("TeamUIDs = %v, want [team-uid-1]", pack.TeamUIDs)
	}
	if !strings.Contains(pack.Text, "[#"+strconv.FormatInt(res.ID, 10)+"]") {
		t.Errorf("reported personal id %d not actually rendered in Text:\n%s", res.ID, pack.Text)
	}
	if !strings.Contains(pack.Text, TeamHandle("team-uid-1")) {
		t.Errorf("reported team uid not actually rendered in Text:\n%s", pack.Text)
	}
}

func TestBuildPackExcludingSkipsSeenEntries(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	var ids []int64
	for i := 0; i < 3; i++ {
		res, err := Save(st, SaveInput{ProjectID: projectID, Dir: dir, Content: "fact " + strconv.Itoa(i), Kind: "fact", Origin: OriginAuto})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, res.ID)
	}

	seen := map[int64]bool{ids[0]: true}
	pack, err := BuildPackExcluding(st, projectID, dir, 0, 0, time.Now(), seen, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pack.Text, "## Project memory (agent-brain) — more relevant entries") {
		t.Errorf("prompt pack missing its distinct header:\n%s", pack.Text)
	}
	if pack.Served() != 2 {
		t.Errorf("Served() = %d, want 2 (one entry excluded as already seen)", pack.Served())
	}
	if strings.Contains(pack.Text, "[#"+strconv.FormatInt(ids[0], 10)+"]") {
		t.Errorf("excluded entry #%d still rendered:\n%s", ids[0], pack.Text)
	}
}

func TestBuildPackExcludingAllSeenIsEmpty(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	res, err := Save(st, SaveInput{ProjectID: projectID, Dir: dir, Content: "only fact", Kind: "fact", Origin: OriginAuto})
	if err != nil {
		t.Fatal(err)
	}
	pack, err := BuildPackExcluding(st, projectID, dir, 0, 0, time.Now(), map[int64]bool{res.ID: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pack.Text != "" {
		t.Errorf("all-seen prompt pack should be empty, got:\n%s", pack.Text)
	}
}

func TestBuildPackExcludesSuperseded(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	first, err := Save(st, SaveInput{ProjectID: projectID, Dir: dir, Content: "old truth", Kind: "decision", Origin: OriginAuto})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Save(st, SaveInput{ProjectID: projectID, Dir: dir, Content: "new truth", Kind: "decision", Origin: OriginAuto, Supersedes: []int64{first.ID}}); err != nil {
		t.Fatal(err)
	}
	pack, err := BuildPack(st, projectID, dir, 0, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(pack.Text, "old truth") {
		t.Error("superseded entry served in pack")
	}
	if !strings.Contains(pack.Text, "new truth") {
		t.Error("active entry missing from pack")
	}
}
