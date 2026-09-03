package core

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"
)

func TestBatchWriterReservesWriterBeforeReadingSnapshot(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "中文 空格", "index.sqlite")
	database, err := OpenIndexDatabase(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.ConfigureIndexing(ctx, true); err != nil {
		t.Fatal(err)
	}

	writer, err := database.BeginBatch(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			writer.Rollback()
		}
	}()

	competing, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer competing.Close()
	if _, err := competing.ExecContext(ctx, "PRAGMA busy_timeout=1"); err != nil {
		t.Fatal(err)
	}
	_, competingErr := competing.ExecContext(ctx, "INSERT INTO index_meta(key, value) VALUES ('heartbeat-probe', '1')")
	if competingErr == nil {
		t.Fatal("competing heartbeat unexpectedly wrote after batch snapshot; BEGIN IMMEDIATE is not active")
	}
	if !strings.Contains(strings.ToLower(competingErr.Error()), "locked") && !strings.Contains(strings.ToLower(competingErr.Error()), "busy") {
		t.Fatalf("competing write failed for an unexpected reason: %v", competingErr)
	}

	draft := syntheticDraft(99, 2)
	if err := writer.Write(ctx, "run", "generation", TaskResult{Candidate: draft.Candidate, Draft: &draft}); err != nil {
		t.Fatalf("batch write failed after competing heartbeat: %v", err)
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatalf("batch commit failed after competing heartbeat: %v", err)
	}
	committed = true
	if _, err := os.Stat(databasePath); err != nil {
		t.Fatal(err)
	}
}

func TestConfigureIndexingKeepsDurableWALSettings(t *testing.T) {
	database, err := OpenIndexDatabase(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	if err := database.ConfigureIndexing(ctx, true); err != nil {
		t.Fatal(err)
	}
	var synchronous, autoCheckpoint int
	if err := database.db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, "PRAGMA wal_autocheckpoint").Scan(&autoCheckpoint); err != nil {
		t.Fatal(err)
	}
	if synchronous != 1 {
		t.Fatalf("full indexing must keep synchronous=NORMAL, got %d", synchronous)
	}
	if autoCheckpoint != 16384 {
		t.Fatalf("unexpected wal_autocheckpoint=%d", autoCheckpoint)
	}
	if _, err := database.db.ExecContext(ctx, "INSERT OR REPLACE INTO index_meta(key, value) VALUES ('checkpoint-test', '1')"); err != nil {
		t.Fatal(err)
	}
	walPath := database.path + "-wal"
	walBefore, err := os.Stat(walPath)
	if err != nil {
		t.Fatal(err)
	}
	if walBefore.Size() == 0 {
		t.Fatal("expected WAL frames before explicit checkpoint")
	}
	if err := database.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	walAfter, err := os.Stat(walPath)
	if err != nil {
		t.Fatal(err)
	}
	if walAfter.Size() != 0 {
		t.Fatalf("explicit checkpoint did not truncate WAL: %d bytes", walAfter.Size())
	}
}

func syntheticEvidenceText(document, chunk int) string {
	var output strings.Builder
	output.WriteString("字段 | A=ID | B=类型 | C=奖励ID | D=缓存值 | E=公式 | F=策划描述 | G=Config-Table.xlsx | H=path/reward:v1\n")
	for row := 1; row <= 48; row++ {
		fmt.Fprintf(&output,
			"行 %d | A=POOL_%03d_%03d_%03d | B=奖励配置 | C=ITEM_%05d | D=%d | E=%d [公式: SUM(D%d:D%d)] | F=",
			row, document, chunk, row, document*1000+row, row*10, row*10, max(1, row-1), row,
		)
		for offset := 0; offset < 64; offset++ {
			value := (document*7919 + chunk*3571 + row*509 + offset*131) % 20_000
			output.WriteRune(rune(0x4E00 + value))
		}
		output.WriteByte('\n')
	}
	return output.String()
}

func syntheticDraft(document, chunks int) Draft {
	return syntheticDraftWithSearchTerms(document, chunks, BuildBodySearchTerms)
}

func syntheticDraftWithSearchTerms(document, chunks int, searchTerms func(string) string) Draft {
	candidate := Candidate{
		SourceID:          "plans",
		SourceLabel:       "策划案",
		SourceKind:        "design",
		SourceIdentity:    strings.Repeat("a", 64),
		AbsolutePath:      filepath.Join("root", fmt.Sprintf("性能奖池配置_%03d.xlsx", document)),
		RelativePath:      fmt.Sprintf("性能奖池配置_%03d.xlsx", document),
		Extension:         ".xlsx",
		SizeBytes:         4096,
		FilesystemMtimeMS: time.Date(2026, time.August, 31, 0, 0, 0, 0, time.UTC).UnixMilli(),
	}
	draft := Draft{
		ID:               fmt.Sprintf("doc_%024x", document+1),
		Candidate:        candidate,
		Title:            fmt.Sprintf("性能奖池配置_%03d", document),
		FamilyKey:        "性能奖池配置",
		FamilyConfidence: 0.9,
		ContentHash:      fmt.Sprintf("%064x", document+1),
		Date: DateResolution{
			EffectiveUpdatedAtMS: candidate.FilesystemMtimeMS,
			DateSource:           "filename",
		},
	}
	for chunk := 0; chunk < chunks; chunk++ {
		text := syntheticEvidenceText(document, chunk)
		draft.Chunks = append(draft.Chunks, Chunk{
			Ordinal:     chunk,
			Text:        text,
			HeadingPath: []string{"奖池配置", fmt.Sprintf("分组 %03d", chunk)},
			SectionType: "config",
			Locator:     fmt.Sprintf("奖池配置!A%d:E%d", chunk*48+1, chunk*48+48),
			ContentHash: fmt.Sprintf("%064x", (document+1)*100000+chunk),
			SearchTerms: searchTerms(text),
		})
	}
	return draft
}

func legacyBodySearchTerms(value string) string {
	pairs := make(map[uint64]struct{}, 256)
	asciiTokens := make(map[string]struct{}, 32)
	var ascii []rune
	flushASCII := func() {
		if len(ascii) > 1 {
			asciiTokens[string(ascii)] = struct{}{}
		}
		ascii = ascii[:0]
	}
	var previous rune
	inCJKRun := false
	for _, r := range value {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			flushASCII()
			if inCJKRun {
				pairs[uint64(uint32(previous))<<32|uint64(uint32(r))] = struct{}{}
			}
			previous = r
			inCJKRun = true
			continue
		}
		inCJKRun = false
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_./:-", r) {
			ascii = append(ascii, unicode.ToLower(r))
		} else {
			flushASCII()
		}
	}
	flushASCII()
	var output strings.Builder
	for pair := range pairs {
		if output.Len() > 0 {
			output.WriteByte(' ')
		}
		output.WriteRune(rune(uint32(pair >> 32)))
		output.WriteRune(rune(uint32(pair)))
	}
	for token := range asciiTokens {
		if output.Len() > 0 {
			output.WriteByte(' ')
		}
		output.WriteString(token)
	}
	return output.String()
}

func writeSyntheticDrafts(ctx context.Context, database *IndexDatabase, drafts []Draft) (int64, error) {
	return writeSyntheticDraftBatches(ctx, database, drafts, len(drafts))
}

func writeSyntheticDraftBatches(ctx context.Context, database *IndexDatabase, drafts []Draft, batchDocuments int) (int64, error) {
	return writeSyntheticDraftBatchesWithMode(ctx, database, drafts, batchDocuments, true)
}

func writeSyntheticDraftBatchesWithMode(ctx context.Context, database *IndexDatabase, drafts []Draft, batchDocuments int, bulkMode bool) (int64, error) {
	if bulkMode {
		if err := database.PrepareCleanRebuild(ctx); err != nil {
			return 0, err
		}
	}
	var chunksWritten int64
	for start := 0; start < len(drafts); start += batchDocuments {
		end := min(len(drafts), start+batchDocuments)
		writer, err := database.BeginBatch(ctx, true)
		if err != nil {
			return 0, err
		}
		for index := start; index < end; index++ {
			if err := writer.Write(ctx, "run", "generation", TaskResult{Candidate: drafts[index].Candidate, Draft: &drafts[index]}); err != nil {
				writer.Rollback()
				return 0, err
			}
		}
		if err := writer.Commit(ctx); err != nil {
			return 0, err
		}
		chunksWritten += writer.chunksWritten
	}
	return chunksWritten, nil
}

func recreateSyntheticTermsIndex(ctx context.Context, database *IndexDatabase, compact bool) error {
	detail := ""
	if compact {
		detail = ", detail=column"
	}
	_, err := database.db.ExecContext(ctx, fmt.Sprintf(`
DROP TABLE IF EXISTS chunks_terms;
CREATE VIRTUAL TABLE chunks_terms USING fts5(
  title_terms,
  heading_terms,
  path_terms,
  body_terms,
  tokenize='unicode61 remove_diacritics 2 tokenchars ''_./:-''',
  content='' %s,
  contentless_delete=1
);`, detail))
	return err
}

func recreateSyntheticLegacyTermsIndex(ctx context.Context, database *IndexDatabase) error {
	_, err := database.db.ExecContext(ctx, `
DROP TABLE IF EXISTS chunks_terms;
CREATE VIRTUAL TABLE chunks_terms USING fts5(
  title_terms,
  heading_terms,
  path_terms,
  body_terms,
  tokenize='unicode61 remove_diacritics 2',
  content='',
  contentless_delete=1
);`)
	return err
}

func TestBatchWriterPreservesRichEvidenceAndSearchIndexes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := OpenIndexDatabase(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.ConfigureIndexing(ctx, true); err != nil {
		t.Fatal(err)
	}
	// Simulate a database initialized by the TypeScript schema. A clean Go
	// rebuild must replace its full-detail terms index with the compact schema.
	if err := recreateSyntheticTermsIndex(ctx, database, false); err != nil {
		t.Fatal(err)
	}
	draft := syntheticDraft(0, 2)
	if _, err := writeSyntheticDrafts(ctx, database, []Draft{draft}); err != nil {
		t.Fatal(err)
	}

	var storedText, storedLocator, storedChunkHash, absolutePath, sourceIdentity, dateSource string
	if err := database.db.QueryRowContext(ctx, `
SELECT c.text, c.locator, c.content_hash, d.absolute_path, d.source_identity, d.date_source
FROM chunks c JOIN documents d ON d.id=c.document_id
WHERE c.document_id=? AND c.ordinal=0`, draft.ID).Scan(&storedText, &storedLocator, &storedChunkHash, &absolutePath, &sourceIdentity, &dateSource); err != nil {
		t.Fatal(err)
	}
	if storedText != draft.Chunks[0].Text {
		t.Fatal("rich chunk text changed during SQLite/FTS write")
	}
	for _, required := range []string{"A=POOL_", "D=10", "[公式: SUM(D1:D1)]"} {
		if !strings.Contains(storedText, required) {
			t.Fatalf("stored evidence lost %q", required)
		}
	}
	if storedLocator != draft.Chunks[0].Locator || storedChunkHash != draft.Chunks[0].ContentHash || absolutePath != draft.Candidate.AbsolutePath || sourceIdentity != draft.Candidate.SourceIdentity || dateSource != draft.Date.DateSource {
		t.Fatalf("metadata mismatch locator=%q hash=%q path=%q identity=%q dateSource=%q", storedLocator, storedChunkHash, absolutePath, sourceIdentity, dateSource)
	}

	var lexicalMatches, strongIDMatches, componentMatches, formulaMatches, punctuationMatches, trigramMatches int
	var rank float64
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks_terms WHERE chunks_terms MATCH '奖励'`).Scan(&lexicalMatches); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks_terms WHERE chunks_terms MATCH '"pool_000_000_001"'`).Scan(&strongIDMatches); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks_terms WHERE chunks_terms MATCH 'pool'`).Scan(&componentMatches); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks_terms WHERE chunks_terms MATCH '"d1:d1"'`).Scan(&formulaMatches); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks_terms WHERE chunks_terms MATCH '"path/reward:v1"'`).Scan(&punctuationMatches); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, `SELECT bm25(chunks_terms, 8.0, 6.0, 5.0, 1.0) FROM chunks_terms WHERE chunks_terms MATCH '奖励' LIMIT 1`).Scan(&rank); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks_trigram WHERE chunks_trigram MATCH '性能奖'`).Scan(&trigramMatches); err != nil {
		t.Fatal(err)
	}
	if lexicalMatches != 2 || strongIDMatches != 1 || componentMatches != 2 || formulaMatches != 2 || punctuationMatches != 2 || trigramMatches == 0 {
		t.Fatalf("search index mismatch lexical=%d strongID=%d component=%d formula=%d punctuation=%d trigram=%d rank=%f", lexicalMatches, strongIDMatches, componentMatches, formulaMatches, punctuationMatches, trigramMatches, rank)
	}
	var termsSchema string
	if err := database.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name='chunks_terms'`).Scan(&termsSchema); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(termsSchema), "detail=column") {
		t.Fatalf("chunks_terms schema is not compact column-detail FTS: %s", termsSchema)
	}
	var termSegments int
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT segid) FROM chunks_terms_idx`).Scan(&termSegments); err != nil {
		t.Fatal(err)
	}
	if termSegments > 16 {
		t.Fatalf("bulk FTS left too many term segments: %d", termSegments)
	}
	var quickCheck string
	if err := database.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&quickCheck); err != nil || quickCheck != "ok" {
		t.Fatalf("quick_check=%q err=%v", quickCheck, err)
	}
}

func TestPrepareCleanRebuildKeepsRemainingSourceSearchRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := OpenIndexDatabase(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.ConfigureIndexing(ctx, true); err != nil {
		t.Fatal(err)
	}
	draft := syntheticDraft(0, 2)
	if _, err := writeSyntheticDrafts(ctx, database, []Draft{draft}); err != nil {
		t.Fatal(err)
	}
	if err := database.PrepareCleanRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	var chunks, lexicalMatches int
	if err := database.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM chunks").Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks_terms WHERE chunks_terms MATCH '奖励'`).Scan(&lexicalMatches); err != nil {
		t.Fatal(err)
	}
	if chunks != 2 || lexicalMatches != 2 {
		t.Fatalf("remaining source rows changed: chunks=%d lexical=%d", chunks, lexicalMatches)
	}
}

func TestBatchWriterBatchesDocumentsBeforeChunks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := OpenIndexDatabase(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.ConfigureIndexing(ctx, true); err != nil {
		t.Fatal(err)
	}
	drafts := make([]Draft, 64)
	for index := range drafts {
		chunks := 0
		if index == 0 {
			chunks = 2
		}
		drafts[index] = syntheticDraft(index, chunks)
	}
	if _, err := writeSyntheticDraftBatches(ctx, database, drafts, len(drafts)); err != nil {
		t.Fatal(err)
	}
	var documents, chunks, orphans int
	if err := database.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM documents").Scan(&documents); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM chunks").Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks c LEFT JOIN documents d ON d.id=c.document_id WHERE d.id IS NULL`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if documents != len(drafts) || chunks != 2 || orphans != 0 {
		t.Fatalf("batched rows mismatch documents=%d chunks=%d orphans=%d", documents, chunks, orphans)
	}
	var sourceIdentity, dateSource string
	if err := database.db.QueryRowContext(ctx, "SELECT source_identity, date_source FROM documents WHERE id=?", drafts[len(drafts)-1].ID).Scan(&sourceIdentity, &dateSource); err != nil {
		t.Fatal(err)
	}
	if sourceIdentity != drafts[len(drafts)-1].Candidate.SourceIdentity || dateSource != drafts[len(drafts)-1].Date.DateSource {
		t.Fatalf("last batched metadata mismatch identity=%q date=%q", sourceIdentity, dateSource)
	}
}

func BenchmarkBatchWriterRichFTS(b *testing.B) {
	ctx := context.Background()
	b.StopTimer()
	drafts := make([]Draft, 24)
	for index := range drafts {
		drafts[index] = syntheticDraft(index, 32)
	}
	b.ReportMetric(float64(len(drafts)*32), "chunks/op")
	b.ReportAllocs()
	for iteration := 0; iteration < b.N; iteration++ {
		database, err := OpenIndexDatabase(filepath.Join(b.TempDir(), "index.sqlite"))
		if err != nil {
			b.Fatal(err)
		}
		if err := database.ConfigureIndexing(ctx, true); err != nil {
			database.Close()
			b.Fatal(err)
		}
		b.StartTimer()
		chunks, err := writeSyntheticDrafts(ctx, database, drafts)
		b.StopTimer()
		if err != nil {
			database.Close()
			b.Fatal(err)
		}
		if chunks != int64(len(drafts)*32) {
			database.Close()
			b.Fatalf("chunks=%d", chunks)
		}
		if err := database.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBatchWriterFTSDetail(b *testing.B) {
	ctx := context.Background()
	b.StopTimer()
	compactDrafts := make([]Draft, 24)
	legacyDrafts := make([]Draft, len(compactDrafts))
	for index := range compactDrafts {
		compactDrafts[index] = syntheticDraft(index, 32)
		legacyDrafts[index] = syntheticDraftWithSearchTerms(index, 32, legacyBodySearchTerms)
	}
	for _, mode := range []struct {
		name    string
		drafts  []Draft
		legacy  bool
		compact bool
	}{
		{name: "legacy-detail-full", drafts: legacyDrafts, legacy: true},
		{name: "identifier-detail-full", drafts: compactDrafts},
		{name: "identifier-detail-column", drafts: compactDrafts, compact: true},
	} {
		b.Run(mode.name, func(b *testing.B) {
			b.ReportMetric(float64(len(mode.drafts)*32), "chunks/op")
			b.ReportAllocs()
			var totalTermBytes int64
			for iteration := 0; iteration < b.N; iteration++ {
				database, err := OpenIndexDatabase(filepath.Join(b.TempDir(), "index.sqlite"))
				if err != nil {
					b.Fatal(err)
				}
				if err := database.ConfigureIndexing(ctx, true); err != nil {
					database.Close()
					b.Fatal(err)
				}
				if mode.legacy {
					err = recreateSyntheticLegacyTermsIndex(ctx, database)
				} else {
					err = recreateSyntheticTermsIndex(ctx, database, mode.compact)
				}
				if err != nil {
					database.Close()
					b.Fatal(err)
				}
				b.StartTimer()
				chunks, err := writeSyntheticDraftBatchesWithMode(ctx, database, mode.drafts, len(mode.drafts), false)
				b.StopTimer()
				if err != nil {
					database.Close()
					b.Fatal(err)
				}
				if chunks != int64(len(mode.drafts)*32) {
					database.Close()
					b.Fatalf("chunks=%d", chunks)
				}
				var termBytes int64
				if err := database.db.QueryRowContext(ctx, "SELECT COALESCE(SUM(pgsize), 0) FROM dbstat WHERE name LIKE 'chunks_terms_%'").Scan(&termBytes); err != nil {
					database.Close()
					b.Fatal(err)
				}
				totalTermBytes += termBytes
				if err := database.Close(); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(totalTermBytes)/float64(max(1, b.N)), "term-bytes/op")
		})
	}
}

func BenchmarkBatchWriterTransactionSize(b *testing.B) {
	ctx := context.Background()
	b.StopTimer()
	drafts := make([]Draft, 96)
	for index := range drafts {
		drafts[index] = syntheticDraft(index, 8)
	}
	for _, batchDocuments := range []int{8, 64, 256} {
		b.Run(fmt.Sprintf("documents-%d", batchDocuments), func(b *testing.B) {
			b.StopTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				database, err := OpenIndexDatabase(filepath.Join(b.TempDir(), "index.sqlite"))
				if err != nil {
					b.Fatal(err)
				}
				if err := database.ConfigureIndexing(ctx, true); err != nil {
					database.Close()
					b.Fatal(err)
				}
				b.StartTimer()
				chunks, err := writeSyntheticDraftBatchesWithMode(ctx, database, drafts, batchDocuments, false)
				b.StopTimer()
				if err != nil {
					database.Close()
					b.Fatal(err)
				}
				if chunks != int64(len(drafts)*8) {
					database.Close()
					b.Fatalf("chunks=%d", chunks)
				}
				var segments int
				if err := database.db.QueryRowContext(ctx, "SELECT COUNT(DISTINCT segid) FROM chunks_terms_idx").Scan(&segments); err != nil {
					database.Close()
					b.Fatal(err)
				}
				b.ReportMetric(float64(segments), "term-segments")
				var termBytes int64
				if err := database.db.QueryRowContext(ctx, "SELECT COALESCE(SUM(pgsize), 0) FROM dbstat WHERE name LIKE 'chunks_terms_%'").Scan(&termBytes); err != nil {
					database.Close()
					b.Fatal(err)
				}
				b.ReportMetric(float64(termBytes), "term-bytes")
				if err := database.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
