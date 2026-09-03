package core

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testSource(id, root string, enabled bool) Source {
	return Source{ID: id, Label: id, Kind: "design", IndexIdentity: "test:" + CanonicalPathKey(root) + ":design", RootPath: root, Enabled: enabled, IncludeExtensions: []string{".md"}, MaxFileBytes: 1_000_000}
}

func TestRunIndexUsesGoSQLiteFTSAndIncrementalLifecycle(t *testing.T) {
	root := t.TempDir()
	sourceA := filepath.Join(root, "a")
	sourceB := filepath.Join(root, "b")
	if err := os.MkdirAll(sourceA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sourceB, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceA, "alpha_20260901.md"), []byte("# 玩法\n\n阿尔法抽奖奖励流程。"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceB, "beta_20260902.md"), []byte("# 配置\n\n贝塔奖池配置字段。"), 0o644); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(root, "data", "index.sqlite")
	config := AppConfig{Sources: []Source{testSource("a", sourceA, true)}, Indexing: IndexingConfig{Concurrency: 2}}
	request := IndexRequest{DatabasePath: databasePath, Config: config, Options: IndexOptions{Full: true}}
	summary, metrics, err := RunIndex(context.Background(), request, NewController(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Phase != "complete" || summary.Indexed != 1 || metrics.Backend != "go" || metrics.ChunksWritten == 0 {
		t.Fatalf("unexpected first run: %#v %#v", summary, metrics)
	}

	second, _, err := RunIndex(context.Background(), IndexRequest{DatabasePath: databasePath, Config: config}, NewController(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Indexed != 0 || second.Unchanged != 1 {
		t.Fatalf("unexpected incremental run: %#v", second)
	}

	config.Sources[0].Enabled = false
	config.Sources = append(config.Sources, testSource("b", sourceB, true))
	third, _, err := RunIndex(context.Background(), IndexRequest{DatabasePath: databasePath, Config: config, Options: IndexOptions{Full: true}}, NewController(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if third.Indexed != 1 {
		t.Fatalf("unexpected scoped full run: %#v", third)
	}

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var documents, matches int
	if err := db.QueryRow("SELECT COUNT(*) FROM documents WHERE deleted=0").Scan(&documents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM chunks_terms WHERE chunks_terms MATCH '奖池'").Scan(&matches); err != nil {
		t.Fatal(err)
	}
	if documents != 2 || matches < 1 {
		t.Fatalf("documents=%d matches=%d", documents, matches)
	}
}

func TestRunIndexPureGoXlsxCompatibilityFailureKeepsLastGoodAndCountsAttempt(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(sourceRoot, "fallback-last-good_20260901.xlsx")
	writeArchive(t, filePath, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="r"><sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst><si><t>字段</t></si><si><t>值</t></si><si><t>奖池</t></si><si><t>1001</t></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>
<row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2" t="s"><v>3</v></c></row>
</sheetData></worksheet>`,
	})
	databasePath := filepath.Join(root, "data", "index.sqlite")
	source := Source{
		ID: "xlsx", Label: "xlsx", Kind: "design", IndexIdentity: "test:" + CanonicalPathKey(sourceRoot) + ":design",
		RootPath: sourceRoot, Enabled: true, IncludeExtensions: []string{".xlsx"}, MaxFileBytes: 10_000_000,
	}
	request := IndexRequest{
		DatabasePath: databasePath,
		Config:       AppConfig{Sources: []Source{source}, Indexing: IndexingConfig{Concurrency: 1}},
		Options:      IndexOptions{Full: true},
	}
	baseline, _, err := RunIndex(context.Background(), request, NewController(), nil, nil)
	if err != nil || baseline.Indexed != 1 || baseline.Failed != 0 {
		t.Fatalf("unexpected baseline: summary=%#v err=%v", baseline, err)
	}

	if err := os.WriteFile(filePath, []byte("corrupted native OOXML and invalid for SheetJS"), 0o644); err != nil {
		t.Fatal(err)
	}
	fallback := &recordingFallback{}
	request.Options.Full = false
	failed, metrics, err := RunIndex(context.Background(), request, NewController(), fallback, nil)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Failed != 1 || failed.Indexed != 0 || metrics.FallbackDocuments != 1 || fallback.called {
		t.Fatalf("unexpected fallback failure: summary=%#v metrics=%#v fallback=%#v", failed, metrics, fallback)
	}

	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var stale, chunks int
	var extractionError string
	if err := database.QueryRow(
		"SELECT stale, chunk_count, extraction_error FROM documents WHERE absolute_path=? AND deleted=0",
		filePath,
	).Scan(&stale, &chunks, &extractionError); err != nil {
		t.Fatal(err)
	}
	var chunkRows, issueRows int
	if err := database.QueryRow("SELECT COUNT(*) FROM chunks WHERE document_id=(SELECT id FROM documents WHERE absolute_path=?)", filePath).Scan(&chunkRows); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM index_issues WHERE path=? AND message LIKE '%纯 Go compatibility fallback%'", filePath).Scan(&issueRows); err != nil {
		t.Fatal(err)
	}
	if stale != 1 || chunks == 0 || chunkRows != chunks || issueRows != 1 || !strings.Contains(extractionError, "Go 原生 OOXML") || !strings.Contains(extractionError, "纯 Go compatibility fallback") {
		t.Fatalf("last-good not preserved: stale=%d chunks=%d chunkRows=%d issues=%d error=%q", stale, chunks, chunkRows, issueRows, extractionError)
	}
}

func TestIncrementalDeleteAndUnchangedReappearanceAdvanceRevision(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sourceRoot, "reappear_20260902.md")
	content := []byte("# 回归\n\n删除后以相同内容重新出现，必须恢复检索。")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	originalInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(root, "data", "index.sqlite")
	request := IndexRequest{
		DatabasePath: databasePath,
		Config:       AppConfig{Sources: []Source{testSource("source", sourceRoot, true)}, Indexing: IndexingConfig{Concurrency: 1}},
		Options:      IndexOptions{Full: true},
	}
	if summary, _, err := RunIndex(context.Background(), request, NewController(), nil, nil); err != nil || summary.Indexed != 1 {
		t.Fatalf("baseline summary=%#v err=%v", summary, err)
	}
	database, err := OpenIndexDatabase(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	revision1, err := database.Revision()
	database.Close()
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	request.Options.Full = false
	deleted, _, err := RunIndex(context.Background(), request, NewController(), nil, nil)
	if err != nil || deleted.Deleted != 1 {
		t.Fatalf("delete summary=%#v err=%v", deleted, err)
	}
	database, err = OpenIndexDatabase(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	revision2, err := database.Revision()
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	status, err := database.RuntimeStatus("")
	database.Close()
	if err != nil || status.DocumentCount != 0 || revision2 <= revision1 {
		t.Fatalf("deleted status=%#v revisions=%d,%d err=%v", status, revision1, revision2, err)
	}

	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, originalInfo.ModTime(), originalInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	reappeared, _, err := RunIndex(context.Background(), request, NewController(), nil, nil)
	if err != nil || reappeared.Unchanged != 1 || reappeared.Indexed != 0 {
		t.Fatalf("reappearance summary=%#v err=%v", reappeared, err)
	}
	database, err = OpenIndexDatabase(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	revision3, err := database.Revision()
	if err != nil {
		t.Fatal(err)
	}
	status, err = database.RuntimeStatus("")
	if err != nil || status.DocumentCount != 1 || revision3 <= revision2 {
		t.Fatalf("reappeared status=%#v revisions=%d,%d err=%v", status, revision2, revision3, err)
	}
}

func TestFullRebuildPurgesRemovedDocumentsAndAdvancesRevision(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(sourceRoot, "first.md")
	second := filepath.Join(sourceRoot, "second.md")
	for path, content := range map[string]string{first: "# First\n\nalpha", second: "# Second\n\nbeta"} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	databasePath := filepath.Join(root, "data", "index.sqlite")
	request := IndexRequest{
		DatabasePath: databasePath,
		Config:       AppConfig{Sources: []Source{testSource("source", sourceRoot, true)}, Indexing: IndexingConfig{Concurrency: 1}},
		Options:      IndexOptions{Full: true},
	}
	if summary, _, err := RunIndex(context.Background(), request, NewController(), nil, nil); err != nil || summary.Indexed != 2 {
		t.Fatalf("baseline summary=%#v err=%v", summary, err)
	}
	database, err := OpenIndexDatabase(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	revision1, err := database.Revision()
	database.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(second); err != nil {
		t.Fatal(err)
	}
	rebuilt, _, err := RunIndex(context.Background(), request, NewController(), nil, nil)
	if err != nil || rebuilt.Indexed != 1 || rebuilt.Deleted != 2 {
		t.Fatalf("rebuild summary=%#v err=%v", rebuilt, err)
	}
	database, err = OpenIndexDatabase(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	revision2, err := database.Revision()
	if err != nil {
		t.Fatal(err)
	}
	status, err := database.RuntimeStatus("")
	if err != nil || status.DocumentCount != 1 || revision2 <= revision1 {
		t.Fatalf("rebuilt status=%#v revisions=%d,%d err=%v", status, revision1, revision2, err)
	}
}
