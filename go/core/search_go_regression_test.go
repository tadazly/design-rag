package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type goSearchSource struct {
	id, kind string
	files    map[string]string
}

func newGoSearchService(t *testing.T, definitions []goSearchSource) *RuntimeService {
	t.Helper()
	root := t.TempDir()
	config := CreateDefaultConfig()
	config.Sources = nil
	config.Indexing.AutomaticScan = false
	config.Indexing.Concurrency = 2
	for _, definition := range definitions {
		sourceRoot := filepath.Join(root, definition.id)
		if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range definition.files {
			if err := os.WriteFile(filepath.Join(sourceRoot, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		source, err := CreateSourceConfig(definition.id, definition.id, definition.kind, sourceRoot, true)
		if err != nil {
			t.Fatal(err)
		}
		extensions := []string{}
		seenExtensions := map[string]bool{}
		for name := range definition.files {
			extension := strings.ToLower(filepath.Ext(name))
			if extension != "" && !seenExtensions[extension] {
				seenExtensions[extension] = true
				extensions = append(extensions, extension)
			}
		}
		source.IncludeExtensions = extensions
		config.Sources = append(config.Sources, source)
	}
	store := NewConfigStore(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if _, err := store.SaveSnapshot(config); err != nil {
		t.Fatal(err)
	}
	service, err := NewRuntimeService(context.Background(), ServiceOptions{ConfigDir: store.ConfigDir, DataDir: store.DataDir})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Index(context.Background(), IndexOptions{Full: true}, nil)
	if err != nil || result.Failed != 0 || result.Indexed != len(flattenTestFiles(definitions)) {
		service.Close()
		t.Fatalf("index failed: result=%#v err=%v", result, err)
	}
	t.Cleanup(func() { service.Close() })
	return service
}

func flattenTestFiles(definitions []goSearchSource) []string {
	result := []string{}
	for _, definition := range definitions {
		for name := range definition.files {
			result = append(result, name)
		}
	}
	return result
}

func TestGoSearchLatestActivityUsesTitleIdentityNotNewerBodyMatch(t *testing.T) {
	service := newGoSearchService(t, []goSearchSource{
		{"plans", "design", map[string]string{
			"环潮龙888活动_20260101.md": "# 玩法\n\n环潮龙888活动的玩法和产出逻辑。",
			"冰王888活动_20251201.md":  "# 玩法\n\n冰王888活动的历史方案。",
		}},
		{"tables", "table", map[string]string{
			"rule_20260831.md": "# 数据\n\n规则表正文包含 888，但它不是活动策划身份。",
		}},
	})
	for _, query := range []string{"找到最新的一个 888活动", "最近 888活动", "latest 888"} {
		result, err := service.Search.Search(context.Background(), SearchRequest{Query: query, Sort: "newest", Limit: 8})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Hits) == 0 || result.Hits[0].Title != "环潮龙888活动_20260101" {
			t.Fatalf("%s top=%v", query, result.Hits)
		}
		for _, hit := range result.Hits {
			if !strings.Contains(hit.Title+"\n"+hit.RelativePath, "888") || hit.Title == "rule_20260831" {
				t.Fatalf("latest identity leaked body-only hit: %#v", hit)
			}
		}
	}
	ordinary, err := service.Search.Retrieve(context.Background(), RetrievalRequest{SearchRequest: SearchRequest{Query: "888活动", Sort: "newest"}, MaxDocuments: 8})
	if err != nil {
		t.Fatal(err)
	}
	foundTable := false
	for _, hit := range ordinary.Search.Hits {
		foundTable = foundTable || hit.Title == "rule_20260831"
	}
	if !foundTable {
		t.Fatal("ordinary query must retain body match")
	}
}

func TestGoSearchKeepsEachExplicitStrongIDInEvidence(t *testing.T) {
	files := map[string]string{"betaStrong_20260101.md": "# 配置\n\nbetaStrong 独立配置。"}
	for index := 1; index <= 9; index++ {
		files["alphaStrong_2026080"+string(rune('0'+index))+".md"] = "# 配置\n\nalphaStrong 配置版本。"
	}
	service := newGoSearchService(t, []goSearchSource{{"tables", "table", files}})
	bundle, err := service.Search.Retrieve(context.Background(), RetrievalRequest{SearchRequest: SearchRequest{Query: "alphaStrong betaStrong 配置"}, MaxDocuments: 8, MaxChunksPerDocument: 1, MaxChars: 24000})
	if err != nil {
		t.Fatal(err)
	}
	foundHit, foundEvidence := false, false
	for _, hit := range bundle.Search.Hits {
		foundHit = foundHit || hit.Title == "betaStrong_20260101"
	}
	for _, evidence := range bundle.Evidence {
		foundEvidence = foundEvidence || evidence.Title == "betaStrong_20260101"
	}
	if !foundHit || !foundEvidence {
		t.Fatalf("strong ID missing: hit=%v evidence=%v", foundHit, foundEvidence)
	}
}

func TestGoRetrieveDocumentIDsSearchesInsideSelectedDocument(t *testing.T) {
	service := newGoSearchService(t, []goSearchSource{{"plans", "design", map[string]string{
		"目标活动_20260831.md":  "# 玩法产出\n\neventSummary 记录活动货币产出与兑换流程。",
		"全词干扰项_20260901.md": "# 配置\n\neventSummary dropUnit item statistic 玩法产出。",
	}}})
	selected, err := service.Search.Search(context.Background(), SearchRequest{Query: "目标活动 eventSummary", Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	documentID := ""
	for _, hit := range selected.Hits {
		if hit.Title == "目标活动_20260831" {
			documentID = hit.DocumentID
		}
	}
	if documentID == "" {
		t.Fatal("selected document missing")
	}
	bundle, err := service.Search.Retrieve(context.Background(), RetrievalRequest{SearchRequest: SearchRequest{Query: "eventSummary dropUnit item statistic 玩法产出"}, DocumentIDs: []string{documentID}, MaxDocuments: 1, MaxChunksPerDocument: 3, MaxChars: 8000})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Search.Hits) != 1 || bundle.Search.Hits[0].DocumentID != documentID || len(bundle.Evidence) == 0 {
		t.Fatalf("selected retrieval failed: %#v", bundle)
	}
}

func TestGoEntityIDTargetedProjectionKeepsExactRowAndIdentifierFields(t *testing.T) {
	text := strings.Join([]string{
		"字段 | A=版本 | B=类型 | C=名称 | D=出处 | E=形态 | F=属性 | G=petId | H=modelId | I=petClass | J=eggId | K=图纸Id | L=图纸碎片Id | M=缩略图",
		"行 1618 | A[版本]=20260923 | B=精灵 | C=史密 | D=888活动 | E=小形态 | F=龙 | G=11346 | H=3825 | I=13668 | J=1367 | K=71275 | L=81275",
		"行 1619 | A[版本]=20260923 | B=精灵 | C=龙王史密斯 | D=888活动 | E=大形态 | F=龙 | G=11347 | H=3826 | I=13668 | J=1367 | K=71275 | L=81275",
		"行 1620 | A[版本]=20260826 | B=精灵 | C=伊瓦 | D=充值盒子精灵 | E=小形态 | F=机械 战斗 | G=11348 | H=3827 | I=13669 | J=1368 | K=71276 | L=81276",
	}, "\n")
	projection, err := MakeExcerpt(text, "Sheet1!A1618:M1620", []string{
		"龙王史密斯", "petid", "modelid", "petclass", "eggid", "图纸id", "图纸碎片id",
	}, 520)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"C=龙王史密斯", "G=petId", "H=modelId", "G=11347", "H=3826", "A[版本]=20260923"} {
		if !strings.Contains(projection.Text, marker) {
			t.Fatalf("entity ID projection missing %s: %s", marker, projection.Text)
		}
	}
	if strings.Contains(projection.Text, "G=597") {
		t.Fatalf("entity ID projection fell back to legacy form: %s", projection.Text)
	}
	if !strings.HasPrefix(projection.Locator, "Sheet1!A") || !strings.Contains(projection.Locator, ":M") {
		t.Fatalf("entity ID projection did not preserve the A:M evidence boundary: %s", projection.Locator)
	}
}

func TestGoScopedCitationPreservesUTF16SlicesAndRejectsTampering(t *testing.T) {
	header := "id,名称,超长字段,说明\n"
	rows := strings.Builder{}
	for row := 1; row <= 190; row++ {
		value := "普通"
		if row == 150 {
			value = strings.Repeat("🙂", 180) + "unique_target" + strings.Repeat("龙", 220)
		}
		rows.WriteString("item")
		rows.WriteString(strings.Repeat("0", 4-len(string(rune('0'+row%10)))))
		rows.WriteString(",奖励,")
		rows.WriteString(value)
		rows.WriteString(",轮盘奖池配置\n")
	}
	service := newGoSearchService(t, []goSearchSource{{"tables", "table", map[string]string{"newPrizePool_20260902.csv": header + rows.String()}}})
	bundle, err := service.Search.Retrieve(context.Background(), RetrievalRequest{SearchRequest: SearchRequest{Query: "unique_target", SourceKinds: []string{"table"}}, MaxDocuments: 1, MaxChunksPerDocument: 1, MaxChars: 8000})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Evidence) != 1 || len(bundle.Search.Hits) != 1 || len(bundle.Search.Hits[0].Excerpts) != 1 {
		t.Fatalf("unexpected evidence: %#v", bundle)
	}
	evidence := bundle.Evidence[0]
	excerpt := bundle.Search.Hits[0].Excerpts[0]
	if !strings.HasPrefix(evidence.CitationID, "DRAG:2.") || utf16Length(evidence.Content) > 520 {
		t.Fatalf("citation=%s utf16Length=%d", evidence.CitationID, utf16Length(evidence.Content))
	}
	read, err := service.Search.ReadCitation(context.Background(), evidence.CitationID, &bundle.IndexRevision)
	if err != nil {
		t.Fatal(err)
	}
	if read.Content != evidence.Content || read.Citation.Locator != evidence.Locator || read.Citation.CitationID != evidence.CitationID {
		t.Fatalf("scoped round-trip differs: read=%#v evidence=%#v", read, evidence)
	}
	tampered := evidence.CitationID[:len(evidence.CitationID)-1] + "x"
	if _, err := service.Search.ReadCitation(context.Background(), tampered, nil); err == nil {
		t.Fatal("tampered citation must fail")
	}
	legacy, err := service.Search.ReadCitation(context.Background(), "DRAG:"+excerpt.ChunkID, nil)
	if err != nil || utf16Length(legacy.Content) <= utf16Length(evidence.Content) {
		t.Fatalf("legacy citation must return full chunk: len=%d err=%v", utf16Length(legacy.Content), err)
	}
}

func TestGoGenericExcerptUsesReplayableScopedCitation(t *testing.T) {
	content := "# Long evidence\n\n" + strings.Repeat("前置内容🙂", 240) + " generic-scope-target " + strings.Repeat("后置内容龙", 240)
	service := newGoSearchService(t, []goSearchSource{{"plans", "design", map[string]string{"long.md": content}}})
	bundle, err := service.Search.Retrieve(context.Background(), RetrievalRequest{SearchRequest: SearchRequest{Query: "generic-scope-target"}, MaxDocuments: 1, MaxChunksPerDocument: 1, MaxChars: 8_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Evidence) != 1 || !strings.HasPrefix(bundle.Evidence[0].CitationID, "DRAG:3.") || utf16Length(bundle.Evidence[0].Content) > 520 {
		t.Fatalf("generic scoped evidence=%#v", bundle.Evidence)
	}
	evidence := bundle.Evidence[0]
	read, err := service.Search.ReadCitation(context.Background(), evidence.CitationID, &bundle.IndexRevision)
	if err != nil || read.Content != evidence.Content || read.Citation.CitationID != evidence.CitationID {
		t.Fatalf("generic scoped round-trip read=%#v evidence=%#v err=%v", read, evidence, err)
	}
	tampered := evidence.CitationID[:len(evidence.CitationID)-1] + "x"
	if _, err := service.Search.ReadCitation(context.Background(), tampered, nil); err == nil {
		t.Fatal("tampered generic scoped citation must fail")
	}
}

func TestGoRetrieveHonorsTenChunksPerDocument(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	source, err := CreateSourceConfig("plans", "策划案", "design", sourceRoot, true)
	if err != nil {
		t.Fatal(err)
	}
	database, err := OpenIndexDatabase(filepath.Join(root, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	draft := Draft{
		ID:        "doc_000000000000000000000001",
		Candidate: Candidate{SourceID: source.ID, SourceLabel: source.Label, SourceKind: source.Kind, SourceIdentity: SourceIndexIdentity(source), AbsolutePath: filepath.Join(sourceRoot, "ten.md"), RelativePath: "ten.md", Extension: ".md", SizeBytes: 100, FilesystemMtimeMS: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC).UnixMilli()},
		Title:     "十段证据", FamilyKey: "十段证据", FamilyConfidence: 1, ContentHash: strings.Repeat("1", 64),
		Date: DateResolution{EffectiveUpdatedAtMS: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC).UnixMilli(), DateSource: "filename"},
	}
	for index := 0; index < 12; index++ {
		text := fmt.Sprintf("ten-chunk-marker 第 %02d 段独立证据", index)
		draft.Chunks = append(draft.Chunks, Chunk{Ordinal: index, Text: text, HeadingPath: []string{fmt.Sprintf("段 %02d", index)}, SectionType: "gameplay", Locator: fmt.Sprintf("line:%d", index+1), ContentHash: fmt.Sprintf("%064x", index+1), SearchTerms: BuildBodySearchTerms(text)})
	}
	if _, err := writeSyntheticDrafts(ctx, database, []Draft{draft}); err != nil {
		t.Fatal(err)
	}
	config := CreateDefaultConfig()
	config.Sources = []Source{source}
	engine := NewSearchEngine(database, func() AppConfig { return config })
	bundle, err := engine.Retrieve(ctx, RetrievalRequest{SearchRequest: SearchRequest{Query: "ten-chunk-marker", SourceIDs: []string{"plans"}, Limit: 1}, MaxDocuments: 1, MaxChunksPerDocument: 10, MaxChars: 60_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Search.Hits) != 1 || len(bundle.Search.Hits[0].Excerpts) != 10 || len(bundle.Evidence) != 10 {
		t.Fatalf("maxChunksPerDocument=10 was not honored: hits=%d excerpts=%d evidence=%d", len(bundle.Search.Hits), len(bundle.Search.Hits[0].Excerpts), len(bundle.Evidence))
	}
}

func TestCandidateCutoffDeterministicAcrossInsertionOrder(t *testing.T) {
	ctx := context.Background()
	const documentCount = 1225
	sourceIdentity := strings.Repeat("a", 64)
	drafts := make([]Draft, 0, documentCount)
	for index := 0; index < documentCount; index++ {
		id := fmt.Sprintf("doc_%024x", index+1)
		text := "deterministic-cutoff-token"
		drafts = append(drafts, Draft{
			ID:        id,
			Candidate: Candidate{SourceID: "plans", SourceLabel: "策划案", SourceKind: "design", SourceIdentity: sourceIdentity, AbsolutePath: filepath.Join("root", fmt.Sprintf("doc-%04d.md", index)), RelativePath: fmt.Sprintf("doc-%04d.md", index), Extension: ".md", SizeBytes: int64(len(text)), FilesystemMtimeMS: 1_800_000_000_000},
			Title:     fmt.Sprintf("doc-%04d", index), FamilyKey: id, FamilyConfidence: 1, ContentHash: fmt.Sprintf("%064x", index+1), Date: DateResolution{EffectiveUpdatedAtMS: 1_800_000_000_000, DateSource: "filename"},
			Chunks: []Chunk{{Ordinal: 0, Text: text, SectionType: "other", Locator: "line:1", ContentHash: fmt.Sprintf("%064x", index+10000), SearchTerms: BuildBodySearchTerms(text)}},
		})
	}
	load := func(root string, values []Draft) []LexicalCandidateRow {
		database, err := OpenIndexDatabase(filepath.Join(root, "index.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		if _, err := writeSyntheticDrafts(ctx, database, values); err != nil {
			t.Fatal(err)
		}
		rows, err := database.LexicalCandidates(ctx, EscapeFTSToken("deterministic-cutoff-token"), 1200, CandidateSourceFilter{SourceIDs: []string{"plans"}, SourceScopes: []SourceIdentityScope{{SourceID: "plans", SourceIdentity: sourceIdentity}}})
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	forward := load(filepath.Join(t.TempDir(), "forward"), drafts)
	reversed := append([]Draft(nil), drafts...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	backward := load(filepath.Join(t.TempDir(), "backward"), reversed)
	if len(forward) != 1200 || len(backward) != len(forward) {
		t.Fatalf("candidate lengths forward=%d backward=%d", len(forward), len(backward))
	}
	for index := range forward {
		if forward[index].ChunkID != backward[index].ChunkID {
			t.Fatalf("candidate cutoff differs at %d: %s != %s", index, forward[index].ChunkID, backward[index].ChunkID)
		}
	}
}

func TestGoSearchHonorsCancellationAndDoesNotSwallowFTSErrors(t *testing.T) {
	service := newGoSearchService(t, []goSearchSource{{"plans", "design", map[string]string{
		"cancel.md": "# Cancel\n\ncancel-search-marker",
	}}})
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Search.Search(canceled, SearchRequest{Query: "cancel-search-marker"}); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled search error=%v", err)
	}
	if _, err := service.Database.db.Exec(`DROP TABLE chunks_terms; CREATE TABLE chunks_terms(rowid INTEGER PRIMARY KEY,title_terms TEXT,heading_terms TEXT,path_terms TEXT,body_terms TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Search.Search(context.Background(), SearchRequest{Query: "cancel-search-marker"}); err == nil {
		t.Fatal("broken FTS table was silently treated as a successful fallback")
	}
}

func TestGoSearchRechecksConfigBeforeReturningEvidence(t *testing.T) {
	service := newGoSearchService(t, []goSearchSource{{"plans", "design", map[string]string{
		"disable.md": "# Disable\n\nin-flight-config-marker",
	}}})
	config := service.Config()
	refreshCalls := 0
	engine := NewSearchEngine(service.Database, func() AppConfig { return cloneConfig(config) })
	engine.refreshConfig = func() error {
		refreshCalls++
		if refreshCalls == 2 {
			config.Sources[0].Enabled = false
		}
		return nil
	}
	result, err := engine.Search(context.Background(), SearchRequest{Query: "in-flight-config-marker"})
	if err != nil {
		t.Fatal(err)
	}
	if refreshCalls < 3 || len(result.Hits) != 0 {
		t.Fatalf("stale evidence returned after config changed: calls=%d result=%#v", refreshCalls, result)
	}
}
