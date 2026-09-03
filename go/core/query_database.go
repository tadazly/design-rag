package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type SourceIdentityScope struct {
	SourceID       string `json:"sourceId"`
	SourceIdentity string `json:"sourceIdentity"`
}

type CandidateSourceFilter struct {
	SourceIDs    []string
	SourceKinds  []string
	SourceScopes []SourceIdentityScope
}

type LexicalCandidateRow struct {
	ChunkID              string
	ChunkDocumentID      string
	Ordinal              int
	SectionType          string
	HeadingPathJSON      string
	Locator              string
	Text                 string
	ContentHash          string
	ID                   string
	CanonicalID          string
	SourceID             string
	SourceLabel          string
	SourceKind           string
	SourceIdentity       string
	AbsolutePath         string
	RelativePath         string
	Extension            string
	Title                string
	FamilyKey            string
	FamilyConfidence     float64
	FilesystemModifiedAt string
	EffectiveUpdatedAtMS int64
	EffectiveUpdatedAt   string
	DateSource           string
	DocumentContentHash  string
	Stale                bool
	LexicalRank          float64
	ExactAnchors         []string
}

type StoredDocument struct {
	ID                   string
	CanonicalID          string
	SourceID             string
	SourceLabel          string
	SourceKind           string
	SourceIdentity       string
	AbsolutePath         string
	RelativePath         string
	Extension            string
	Title                string
	FamilyKey            string
	FamilyConfidence     float64
	FilesystemModifiedAt string
	EffectiveUpdatedAtMS int64
	EffectiveUpdatedAt   string
	DateSource           string
	ContentHash          string
	Stale                bool
	WarningsJSON         string
	NeedsOCR             bool
	ChunkCount           int
}

type IndexIssueRecord struct {
	Path       string `json:"path"`
	SourceID   string `json:"sourceId"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	OccurredAt string `json:"occurredAt"`
}

type FormatIndexStat struct {
	Extension string `json:"extension"`
	Documents int64  `json:"documents"`
	Chunks    int64  `json:"chunks"`
	Stale     int64  `json:"stale"`
	NeedsOCR  int64  `json:"needsOcr"`
}

type RuntimeIndexStatus struct {
	DatabasePath     string             `json:"databasePath"`
	ConfigPath       string             `json:"configPath"`
	IndexRevision    int64              `json:"indexRevision"`
	FTS5Available    bool               `json:"fts5Available"`
	TrigramAvailable bool               `json:"trigramAvailable"`
	DocumentCount    int64              `json:"documentCount"`
	ChunkCount       int64              `json:"chunkCount"`
	StaleCount       int64              `json:"staleCount"`
	SourceCounts     map[string]int     `json:"sourceCounts"`
	ActiveRun        *RunSummary        `json:"activeRun"`
	LastRun          *RunSummary        `json:"lastRun"`
	RecentIssues     []IndexIssueRecord `json:"recentIssues"`
	IndexBackend     any                `json:"indexBackend,omitempty"`
}

type MutationLease struct {
	Name          string `json:"name"`
	OwnerID       string `json:"ownerId"`
	Operation     string `json:"operation"`
	AcquiredAtMS  int64  `json:"acquiredAtMs"`
	HeartbeatAtMS int64  `json:"heartbeatAtMs"`
	ExpiresAtMS   int64  `json:"expiresAtMs"`
}

type PurgeSourcesResult struct {
	SourceIDs     []string `json:"sourceIds"`
	Documents     int64    `json:"documents"`
	Chunks        int64    `json:"chunks"`
	Embeddings    int64    `json:"embeddings"`
	Issues        int64    `json:"issues"`
	IndexRevision int64    `json:"indexRevision"`
}

type SourceConfigurationReconciliationResult struct {
	Purged               PurgeSourcesResult `json:"purged"`
	InvalidatedSourceIDs []string           `json:"invalidatedSourceIds"`
	RecoverySourceIDs    []string           `json:"recoverySourceIds"`
}

type IndexConsistency struct {
	ActiveDocuments   int64 `json:"activeDocuments"`
	DeletedDocuments  int64 `json:"deletedDocuments"`
	DeclaredChunks    int64 `json:"declaredChunks"`
	ChunkRows         int64 `json:"chunkRows"`
	TermRows          int64 `json:"termRows"`
	TrigramRows       int64 `json:"trigramRows"`
	OrphanChunks      int64 `json:"orphanChunks"`
	OrphanTermRows    int64 `json:"orphanTermRows"`
	OrphanTrigramRows int64 `json:"orphanTrigramRows"`
}

type BIFFFormulaAudit struct {
	Documents                 int64 `json:"documents"`
	DocumentsWithQualityData  int64 `json:"documentsWithQualityData"`
	Total                     int64 `json:"total"`
	Cached                    int64 `json:"cached"`
	Uncached                  int64 `json:"uncached"`
	Decoded                   int64 `json:"decoded"`
	Degraded                  int64 `json:"degraded"`
	Empty                     int64 `json:"empty"`
	StringLiteral             int64 `json:"stringLiteral"`
	FormulaMarkers            int64 `json:"formulaMarkers"`
	XLookupOccurrences        int64 `json:"xlookupOccurrences"`
	TextJoinOccurrences       int64 `json:"textjoinOccurrences"`
	DegradedMarkerOccurrences int64 `json:"degradedMarkerOccurrences"`
}

var biffFormulaQualityPattern = regexp.MustCompile(`^BIFF 公式质量: total=(\d+) cached=(\d+) uncached=(\d+) decoded=(\d+) degraded=(\d+) empty=(\d+) stringLiteral=(\d+)$`)

func (database *IndexDatabase) Consistency(ctx context.Context) (IndexConsistency, error) {
	var result IndexConsistency
	checks := []struct {
		query string
		value *int64
	}{
		{"SELECT COUNT(*) FROM documents WHERE deleted=0", &result.ActiveDocuments},
		{"SELECT COUNT(*) FROM documents WHERE deleted<>0", &result.DeletedDocuments},
		{"SELECT COALESCE(SUM(chunk_count),0) FROM documents WHERE deleted=0", &result.DeclaredChunks},
		{"SELECT COUNT(*) FROM chunks c JOIN documents d ON d.id=c.document_id WHERE d.deleted=0", &result.ChunkRows},
		{"SELECT COUNT(*) FROM chunks_terms", &result.TermRows},
		{"SELECT COUNT(*) FROM chunks_trigram", &result.TrigramRows},
		{"SELECT COUNT(*) FROM chunks c LEFT JOIN documents d ON d.id=c.document_id WHERE d.id IS NULL", &result.OrphanChunks},
		{"SELECT COUNT(*) FROM chunks_terms t LEFT JOIN chunks c ON c.rowid=t.rowid WHERE c.rowid IS NULL", &result.OrphanTermRows},
		{"SELECT COUNT(*) FROM chunks_trigram t LEFT JOIN chunks c ON c.rowid=t.rowid WHERE c.rowid IS NULL", &result.OrphanTrigramRows},
	}
	for _, check := range checks {
		if err := database.db.QueryRowContext(ctx, check.query).Scan(check.value); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (database *IndexDatabase) BIFFFormulaQuality(ctx context.Context) (BIFFFormulaAudit, error) {
	var result BIFFFormulaAudit
	rows, err := database.db.QueryContext(ctx, "SELECT warnings_json FROM documents WHERE deleted=0 AND extension='.xls'")
	if err != nil {
		return result, err
	}
	for rows.Next() {
		result.Documents++
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return result, err
		}
		var warnings []string
		if err := json.Unmarshal([]byte(raw), &warnings); err != nil {
			rows.Close()
			return result, err
		}
		for _, warning := range warnings {
			matches := biffFormulaQualityPattern.FindStringSubmatch(warning)
			if len(matches) != 8 {
				continue
			}
			values := make([]int64, 7)
			for index := range values {
				values[index], _ = strconv.ParseInt(matches[index+1], 10, 64)
			}
			result.DocumentsWithQualityData++
			result.Total += values[0]
			result.Cached += values[1]
			result.Uncached += values[2]
			result.Decoded += values[3]
			result.Degraded += values[4]
			result.Empty += values[5]
			result.StringLiteral += values[6]
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	rows.Close()
	rows, err = database.db.QueryContext(ctx, "SELECT c.text FROM chunks c JOIN documents d ON d.id=c.document_id WHERE d.deleted=0 AND d.extension='.xls'")
	if err != nil {
		return result, err
	}
	degradedMarkers := []string{"BIFF_TOKEN_HEX", "_UNK", "_NAMEX_", "UNKNOWN"}
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			rows.Close()
			return result, err
		}
		upper := strings.ToUpper(text)
		result.FormulaMarkers += int64(strings.Count(text, "{formula="))
		result.XLookupOccurrences += int64(strings.Count(upper, "XLOOKUP"))
		result.TextJoinOccurrences += int64(strings.Count(upper, "TEXTJOIN"))
		for _, marker := range degradedMarkers {
			result.DegradedMarkerOccurrences += int64(strings.Count(upper, marker))
		}
	}
	return result, rows.Err()
}

func (database *IndexDatabase) IssuesForRun(ctx context.Context, runID string) ([]IndexIssueRecord, error) {
	rows, err := database.db.QueryContext(ctx, "SELECT path,source_id,code,message,occurred_at FROM index_issues WHERE run_id=? ORDER BY path,code,message", runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []IndexIssueRecord{}
	for rows.Next() {
		var issue IndexIssueRecord
		if err := rows.Scan(&issue.Path, &issue.SourceID, &issue.Code, &issue.Message, &issue.OccurredAt); err != nil {
			return nil, err
		}
		result = append(result, issue)
	}
	return result, rows.Err()
}

func (database *IndexDatabase) ProbeReadHealth(ctx context.Context, configPath string) error {
	for _, probe := range []struct{ name, query string }{
		{"documents", "SELECT content_hash FROM documents NOT INDEXED ORDER BY rowid LIMIT 1"},
		{"chunks", "SELECT text FROM chunks NOT INDEXED ORDER BY rowid LIMIT 1"},
	} {
		var value string
		err := database.db.QueryRowContext(ctx, probe.query).Scan(&value)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("SQLite database corruption: %s 有界读取检查失败: %w", probe.name, err)
		}
	}
	if _, err := database.RuntimeStatus(configPath); err != nil {
		return fmt.Errorf("SQLite 核心读取健康检查失败: %w", err)
	}
	for _, table := range []string{"chunks_terms", "chunks_trigram"} {
		rows, err := database.db.QueryContext(ctx, "SELECT rowid FROM "+table+" WHERE "+table+" MATCH ? LIMIT 1", "__drag_startup_health_probe__")
		if err != nil {
			return fmt.Errorf("SQLite %s 读取健康检查失败: %w", table, err)
		}
		for rows.Next() {
			var rowID int64
			if err := rows.Scan(&rowID); err != nil {
				rows.Close()
				return fmt.Errorf("SQLite %s 读取健康检查失败: %w", table, err)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return fmt.Errorf("SQLite %s 读取健康检查失败: %w", table, err)
		}
	}
	return nil
}

func (database *IndexDatabase) FormatStats() ([]FormatIndexStat, error) {
	rows, err := database.db.Query(`
		SELECT d.extension,
		       COUNT(*) AS documents,
		       COALESCE(SUM((SELECT COUNT(*) FROM chunks c WHERE c.document_id=d.id)), 0) AS chunks,
		       COALESCE(SUM(CASE WHEN d.stale=1 THEN 1 ELSE 0 END), 0) AS stale,
		       COALESCE(SUM(CASE WHEN d.needs_ocr=1 THEN 1 ELSE 0 END), 0) AS needs_ocr
		FROM documents d WHERE d.deleted=0
		GROUP BY d.extension ORDER BY d.extension`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []FormatIndexStat{}
	for rows.Next() {
		var item FormatIndexStat
		if err := rows.Scan(&item.Extension, &item.Documents, &item.Chunks, &item.Stale, &item.NeedsOCR); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func candidateSourceWhere(filter CandidateSourceFilter, alias string) (string, []any) {
	clauses := []string{}
	values := []any{}
	sourceIDs := uniqueStrings(filter.SourceIDs)
	sourceKinds := uniqueStrings(filter.SourceKinds)
	if len(sourceIDs) > 0 {
		clauses = append(clauses, fmt.Sprintf("%s.source_id IN (%s)", alias, placeholders(len(sourceIDs))))
		values = append(values, stringArgs(sourceIDs)...)
	}
	if len(sourceKinds) > 0 {
		clauses = append(clauses, fmt.Sprintf("%s.source_kind IN (%s)", alias, placeholders(len(sourceKinds))))
		values = append(values, stringArgs(sourceKinds)...)
	}
	if len(filter.SourceScopes) > 0 {
		parts := make([]string, 0, len(filter.SourceScopes))
		for _, scope := range filter.SourceScopes {
			parts = append(parts, fmt.Sprintf("(%s.source_id=? AND %s.source_identity=?)", alias, alias))
			values = append(values, scope.SourceID, scope.SourceIdentity)
		}
		clauses = append(clauses, "("+strings.Join(parts, " OR ")+")")
	}
	if len(clauses) == 0 {
		return "", values
	}
	return " AND " + strings.Join(clauses, " AND "), values
}

func candidateCanonicalWhere(filter CandidateSourceFilter) (string, []any) {
	hasFilter := len(filter.SourceIDs) > 0 || len(filter.SourceKinds) > 0 || len(filter.SourceScopes) > 0
	if !hasFilter {
		return " AND d.id=d.canonical_id", nil
	}
	preferredWhere, values := candidateSourceWhere(filter, "preferred")
	return ` AND d.id=(
SELECT preferred.id FROM documents preferred
WHERE preferred.content_hash=d.content_hash AND preferred.deleted=0` + preferredWhere + `
ORDER BY preferred.effective_updated_at_ms DESC, preferred.relative_path ASC, preferred.id ASC LIMIT 1)`, values
}

const candidateSelect = `
c.id, c.document_id, c.ordinal, c.section_type, c.heading_path_json, c.locator, c.text, c.content_hash,
d.id, d.canonical_id, d.source_id, d.source_label, d.source_kind, d.source_identity,
d.absolute_path, d.relative_path, d.extension, d.title, d.family_key, d.family_confidence,
d.filesystem_modified_at, d.effective_updated_at_ms, d.effective_updated_at, d.date_source,
d.content_hash, d.stale`

func scanCandidate(scanner interface{ Scan(...any) error }, rank float64) (LexicalCandidateRow, error) {
	var row LexicalCandidateRow
	var stale int
	err := scanner.Scan(
		&row.ChunkID, &row.ChunkDocumentID, &row.Ordinal, &row.SectionType, &row.HeadingPathJSON, &row.Locator, &row.Text, &row.ContentHash,
		&row.ID, &row.CanonicalID, &row.SourceID, &row.SourceLabel, &row.SourceKind, &row.SourceIdentity,
		&row.AbsolutePath, &row.RelativePath, &row.Extension, &row.Title, &row.FamilyKey, &row.FamilyConfidence,
		&row.FilesystemModifiedAt, &row.EffectiveUpdatedAtMS, &row.EffectiveUpdatedAt, &row.DateSource,
		&row.DocumentContentHash, &stale,
	)
	row.Stale = stale != 0
	row.LexicalRank = rank
	return row, err
}

func scanRankedCandidate(scanner interface{ Scan(...any) error }) (LexicalCandidateRow, error) {
	var row LexicalCandidateRow
	var stale int
	err := scanner.Scan(
		&row.ChunkID, &row.ChunkDocumentID, &row.Ordinal, &row.SectionType, &row.HeadingPathJSON, &row.Locator, &row.Text, &row.ContentHash,
		&row.ID, &row.CanonicalID, &row.SourceID, &row.SourceLabel, &row.SourceKind, &row.SourceIdentity,
		&row.AbsolutePath, &row.RelativePath, &row.Extension, &row.Title, &row.FamilyKey, &row.FamilyConfidence,
		&row.FilesystemModifiedAt, &row.EffectiveUpdatedAtMS, &row.EffectiveUpdatedAt, &row.DateSource,
		&row.DocumentContentHash, &stale, &row.LexicalRank,
	)
	row.Stale = stale != 0
	return row, err
}

func collectRankedCandidates(rows *sql.Rows) ([]LexicalCandidateRow, error) {
	defer rows.Close()
	result := []LexicalCandidateRow{}
	for rows.Next() {
		row, err := scanRankedCandidate(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (database *IndexDatabase) Revision() (int64, error) {
	var value string
	if err := database.db.QueryRow("SELECT value FROM index_meta WHERE key='index_revision'").Scan(&value); err != nil {
		return 0, err
	}
	revision, err := strconv.ParseInt(value, 10, 64)
	if err != nil || revision < 0 {
		return 0, fmt.Errorf("索引 index_revision 无效：%q", value)
	}
	return revision, nil
}

func (database *IndexDatabase) HasTable(name string) bool {
	var count int
	_ = database.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&count)
	return count > 0
}

func (database *IndexDatabase) LexicalCandidates(ctx context.Context, matchQuery string, limit int, filter CandidateSourceFilter) ([]LexicalCandidateRow, error) {
	if strings.TrimSpace(matchQuery) == "" || !database.HasTable("chunks_terms") {
		return nil, nil
	}
	sourceWhere, sourceValues := candidateSourceWhere(filter, "d")
	canonicalWhere, canonicalValues := candidateCanonicalWhere(filter)
	query := `SELECT ` + candidateSelect + `, bm25(chunks_terms,8.0,6.0,5.0,1.0)
FROM chunks_terms JOIN chunks c ON c.rowid=chunks_terms.rowid JOIN documents d ON d.id=c.document_id
WHERE chunks_terms MATCH ? AND d.deleted=0` + sourceWhere + canonicalWhere + `
ORDER BY bm25(chunks_terms,8.0,6.0,5.0,1.0) ASC,d.effective_updated_at_ms DESC,d.relative_path ASC,d.id ASC,c.ordinal ASC,c.id ASC LIMIT ?`
	args := []any{matchQuery}
	args = append(args, sourceValues...)
	args = append(args, canonicalValues...)
	args = append(args, limit)
	rows, err := database.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return collectRankedCandidates(rows)
}

func (database *IndexDatabase) TrigramCandidates(ctx context.Context, matchQuery string, limit int, filter CandidateSourceFilter) ([]LexicalCandidateRow, error) {
	if strings.TrimSpace(matchQuery) == "" || !database.HasTable("chunks_trigram") {
		return nil, nil
	}
	sourceWhere, sourceValues := candidateSourceWhere(filter, "d")
	canonicalWhere, canonicalValues := candidateCanonicalWhere(filter)
	query := `SELECT ` + candidateSelect + `, bm25(chunks_trigram,8.0,6.0,5.0)
FROM chunks_trigram JOIN chunks c ON c.rowid=chunks_trigram.rowid JOIN documents d ON d.id=c.document_id
WHERE chunks_trigram MATCH ? AND d.deleted=0` + sourceWhere + canonicalWhere + `
ORDER BY bm25(chunks_trigram,8.0,6.0,5.0) ASC,d.effective_updated_at_ms DESC,d.relative_path ASC,d.id ASC,c.ordinal ASC,c.id ASC LIMIT ?`
	args := []any{matchQuery}
	args = append(args, sourceValues...)
	args = append(args, canonicalValues...)
	args = append(args, limit)
	rows, err := database.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return collectRankedCandidates(rows)
}

func (database *IndexDatabase) LikeCandidates(ctx context.Context, terms []string, limit int, filter CandidateSourceFilter) ([]LexicalCandidateRow, error) {
	usable := []string{}
	for _, term := range terms {
		if len([]rune(term)) >= 2 && len(usable) < 12 {
			usable = append(usable, strings.ToLower(strings.TrimSpace(term)))
		}
	}
	if len(usable) == 0 {
		return nil, nil
	}
	sourceWhere, sourceValues := candidateSourceWhere(filter, "d")
	canonicalWhere, canonicalValues := candidateCanonicalWhere(filter)
	predicates := []string{}
	args := append([]any{}, sourceValues...)
	args = append(args, canonicalValues...)
	for _, term := range usable {
		predicates = append(predicates, "LOWER(c.text) LIKE ?", "LOWER(d.title) LIKE ?", "LOWER(d.relative_path) LIKE ?")
		value := "%" + term + "%"
		args = append(args, value, value, value)
	}
	args = append(args, limit)
	query := `SELECT ` + candidateSelect + `, 10.0
FROM chunks c JOIN documents d ON d.id=c.document_id
WHERE d.deleted=0` + sourceWhere + canonicalWhere + ` AND (` + strings.Join(predicates, " OR ") + `)
ORDER BY d.effective_updated_at_ms DESC,d.relative_path ASC,d.id ASC,c.ordinal ASC,c.id ASC LIMIT ?`
	rows, err := database.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return collectRankedCandidates(rows)
}

func (database *IndexDatabase) DocumentExactCandidates(ctx context.Context, terms []string, limit int, filter CandidateSourceFilter) ([]LexicalCandidateRow, error) {
	usable := uniqueStrings(terms)
	if len(usable) > 12 {
		usable = usable[:12]
	}
	if len(usable) == 0 {
		return nil, nil
	}
	sourceWhere, sourceValues := candidateSourceWhere(filter, "d")
	result := map[string]*LexicalCandidateRow{}
	order := []string{}
	perTerm := max(4, (limit+len(usable)-1)/len(usable))
	for _, term := range usable {
		query := `SELECT d.id,d.content_hash,d.canonical_id FROM documents d WHERE d.deleted=0` + sourceWhere + `
AND (instr(LOWER(d.title),?)>0 OR instr(LOWER(d.relative_path),?)>0 OR EXISTS(
SELECT 1 FROM chunks h WHERE h.document_id=d.id AND instr(LOWER(h.heading_path_json),?)>0))
ORDER BY CASE WHEN LOWER(d.title)=? THEN 0 WHEN instr(LOWER(d.title),?)>0 THEN 1 WHEN instr(LOWER(d.relative_path),?)>0 THEN 2 ELSE 3 END,
d.effective_updated_at_ms DESC,d.relative_path ASC,d.id ASC LIMIT ?`
		args := append([]any{}, sourceValues...)
		args = append(args, term, term, term, term, term, term, perTerm*4)
		documents, err := database.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		type matched struct{ id, hash, canonical string }
		matches := []matched{}
		seenContent := map[string]bool{}
		for documents.Next() {
			var item matched
			if err := documents.Scan(&item.id, &item.hash, &item.canonical); err != nil {
				documents.Close()
				return nil, err
			}
			key := item.hash
			if key == "" {
				key = item.id
			}
			if !seenContent[key] {
				seenContent[key] = true
				matches = append(matches, item)
				if len(matches) >= perTerm {
					break
				}
			}
		}
		documents.Close()
		for _, item := range matches {
			key := item.hash
			if key == "" {
				key = item.id
			}
			if existing := result[key]; existing != nil {
				existing.ExactAnchors = appendUnique(existing.ExactAnchors, term)
				continue
			}
			row, err := database.GetBestDocumentChunk(ctx, item.id, term, filter)
			if err != nil {
				return nil, err
			}
			if row != nil {
				row.ExactAnchors = []string{term}
				result[key] = row
				order = append(order, key)
			}
		}
	}
	rows := make([]LexicalCandidateRow, 0, min(limit, len(order)))
	for _, key := range order {
		if row := result[key]; row != nil {
			rows = append(rows, *row)
			if len(rows) >= limit {
				break
			}
		}
	}
	return rows, nil
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func (database *IndexDatabase) GetBestDocumentChunk(ctx context.Context, documentID, term string, filter CandidateSourceFilter) (*LexicalCandidateRow, error) {
	sourceWhere, sourceValues := candidateSourceWhere(filter, "d")
	query := `SELECT ` + candidateSelect + ` FROM chunks c JOIN documents d ON d.id=c.document_id
WHERE d.id=? AND d.deleted=0` + sourceWhere + ` ORDER BY CASE WHEN instr(LOWER(c.heading_path_json),?)>0 THEN 0 WHEN instr(LOWER(c.text),?)>0 THEN 1 ELSE 2 END,c.ordinal ASC LIMIT 1`
	args := []any{documentID}
	args = append(args, sourceValues...)
	args = append(args, term, term)
	row, err := scanCandidate(database.db.QueryRowContext(ctx, query, args...), 0)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &row, err
}

func (database *IndexDatabase) DocumentCandidates(ctx context.Context, documentIDs, terms []string, perDocumentLimit int, filter CandidateSourceFilter) ([]LexicalCandidateRow, error) {
	ids := uniqueStrings(documentIDs)
	if len(ids) > 50 {
		ids = ids[:50]
	}
	usable := uniqueStrings(terms)
	if len(usable) > 24 {
		usable = usable[:24]
	}
	limit := min(64, max(1, perDocumentLimit))
	sourceWhere, sourceValues := candidateSourceWhere(filter, "d")
	scoreParts := []string{}
	scoreValues := []any{}
	for _, term := range usable {
		scoreParts = append(scoreParts, `(CASE WHEN instr(LOWER(c.heading_path_json),?)>0 THEN 8 ELSE 0 END
+CASE WHEN instr(LOWER(c.text),?)>0 THEN 4 ELSE 0 END
+CASE WHEN instr(LOWER(d.title),?)>0 THEN 10 ELSE 0 END
+CASE WHEN instr(LOWER(d.relative_path),?)>0 THEN 6 ELSE 0 END)`)
		scoreValues = append(scoreValues, term, term, term, term)
	}
	scoreExpression := "0"
	if len(scoreParts) > 0 {
		scoreExpression = strings.Join(scoreParts, "+")
	}
	result := []LexicalCandidateRow{}
	for _, id := range ids {
		query := `SELECT ` + candidateSelect + `,0.0 FROM chunks c JOIN documents d ON d.id=c.document_id
WHERE d.id=? AND d.deleted=0` + sourceWhere + ` ORDER BY (` + scoreExpression + `) DESC,c.ordinal ASC LIMIT ?`
		args := []any{id}
		args = append(args, sourceValues...)
		args = append(args, scoreValues...)
		args = append(args, limit)
		rows, err := database.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		items, err := collectRankedCandidates(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
	}
	return result, nil
}

func (database *IndexDatabase) GetChunk(ctx context.Context, chunkID string) (*LexicalCandidateRow, error) {
	row, err := scanCandidate(database.db.QueryRowContext(ctx, `SELECT `+candidateSelect+` FROM chunks c JOIN documents d ON d.id=c.document_id WHERE c.id=? AND d.deleted=0`, chunkID), 0)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &row, err
}

func scanStoredDocument(scanner interface{ Scan(...any) error }) (StoredDocument, error) {
	var result StoredDocument
	var stale, needsOCR int
	err := scanner.Scan(&result.ID, &result.CanonicalID, &result.SourceID, &result.SourceLabel, &result.SourceKind, &result.SourceIdentity,
		&result.AbsolutePath, &result.RelativePath, &result.Extension, &result.Title, &result.FamilyKey, &result.FamilyConfidence,
		&result.FilesystemModifiedAt, &result.EffectiveUpdatedAtMS, &result.EffectiveUpdatedAt, &result.DateSource,
		&result.ContentHash, &stale, &result.WarningsJSON, &needsOCR, &result.ChunkCount)
	result.Stale = stale != 0
	result.NeedsOCR = needsOCR != 0
	return result, err
}

const documentSelect = `id,canonical_id,source_id,source_label,source_kind,source_identity,absolute_path,relative_path,extension,title,family_key,family_confidence,filesystem_modified_at,effective_updated_at_ms,effective_updated_at,date_source,content_hash,stale,warnings_json,needs_ocr,chunk_count`

func (database *IndexDatabase) GetDocument(ctx context.Context, documentID string) (*StoredDocument, error) {
	document, err := scanStoredDocument(database.db.QueryRowContext(ctx, `SELECT `+documentSelect+` FROM documents WHERE id=? AND deleted=0`, documentID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &document, err
}

func (database *IndexDatabase) GetVersions(ctx context.Context, familyKey string, limit int, scopes []SourceIdentityScope) ([]StoredDocument, error) {
	where := ""
	args := []any{familyKey}
	if len(scopes) > 0 {
		parts := make([]string, len(scopes))
		for index, scope := range scopes {
			parts[index] = "(source_id=? AND source_identity=?)"
			args = append(args, scope.SourceID, scope.SourceIdentity)
		}
		where = " AND (" + strings.Join(parts, " OR ") + ")"
	}
	args = append(args, limit)
	rows, err := database.db.QueryContext(ctx, `SELECT `+documentSelect+` FROM documents WHERE family_key=? AND deleted=0`+where+` ORDER BY effective_updated_at_ms DESC,relative_path ASC,id ASC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []StoredDocument{}
	for rows.Next() {
		document, err := scanStoredDocument(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, document)
	}
	return result, rows.Err()
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func scanRun(scanner interface{ Scan(...any) error }) (*RunSummary, error) {
	var result RunSummary
	var finished, currentPath, runError sql.NullString
	err := scanner.Scan(&result.RunID, &result.Phase, &result.StartedAt, &finished, &result.Discovered, &result.Indexed, &result.Unchanged, &result.Skipped, &result.Failed, &result.Deleted, &currentPath, &runError)
	if err != nil {
		return nil, err
	}
	result.FinishedAt = nullableString(finished)
	result.CurrentPath = nullableString(currentPath)
	result.Error = nullableString(runError)
	return &result, nil
}

func (database *IndexDatabase) RuntimeStatus(configPath string) (RuntimeIndexStatus, error) {
	revision, err := database.Revision()
	if err != nil {
		return RuntimeIndexStatus{}, err
	}
	status := RuntimeIndexStatus{DatabasePath: database.path, ConfigPath: configPath, IndexRevision: revision, FTS5Available: database.HasTable("chunks_terms"), TrigramAvailable: database.HasTable("chunks_trigram"), SourceCounts: map[string]int{}, RecentIssues: []IndexIssueRecord{}}
	if err := database.db.QueryRow("SELECT COUNT(*) FROM documents WHERE deleted=0 AND id=canonical_id").Scan(&status.DocumentCount); err != nil {
		return status, err
	}
	if err := database.db.QueryRow("SELECT COUNT(*) FROM chunks c JOIN documents d ON d.id=c.document_id WHERE d.deleted=0 AND d.id=d.canonical_id").Scan(&status.ChunkCount); err != nil {
		return status, err
	}
	if err := database.db.QueryRow("SELECT COUNT(*) FROM documents WHERE deleted=0 AND stale=1").Scan(&status.StaleCount); err != nil {
		return status, err
	}
	rows, err := database.db.Query("SELECT source_id,COUNT(*) FROM documents WHERE deleted=0 AND id=canonical_id GROUP BY source_id")
	if err != nil {
		return status, err
	}
	for rows.Next() {
		var sourceID string
		var count int
		if err := rows.Scan(&sourceID, &count); err != nil {
			rows.Close()
			return status, err
		}
		status.SourceCounts[sourceID] = count
	}
	rows.Close()
	runRows, err := database.db.Query("SELECT run_id,phase,started_at,finished_at,discovered,indexed,unchanged,skipped,failed,deleted,current_path,error FROM index_runs ORDER BY started_at DESC LIMIT 20")
	if err != nil {
		return status, err
	}
	runs := []*RunSummary{}
	for runRows.Next() {
		run, scanErr := scanRun(runRows)
		if scanErr != nil {
			runRows.Close()
			return status, scanErr
		}
		runs = append(runs, run)
	}
	runRows.Close()
	if len(runs) > 0 && runs[0].Phase != "complete" && runs[0].Phase != "failed" {
		status.ActiveRun = runs[0]
	}
	for _, run := range runs {
		if (run.Phase == "complete" || run.Phase == "failed") && status.LastRun == nil {
			status.LastRun = run
		}
	}
	var issueRunID any
	if status.ActiveRun != nil {
		issueRunID = status.ActiveRun.RunID
	} else if status.LastRun != nil {
		issueRunID = status.LastRun.RunID
	}
	issueQuery := "SELECT path,source_id,code,message,occurred_at FROM index_issues WHERE run_id IS NULL ORDER BY occurred_at DESC LIMIT 20"
	issueArgs := []any{}
	if issueRunID != nil {
		issueQuery = "SELECT path,source_id,code,message,occurred_at FROM index_issues WHERE run_id=? OR run_id IS NULL ORDER BY occurred_at DESC LIMIT 20"
		issueArgs = append(issueArgs, issueRunID)
	}
	issueRows, err := database.db.Query(issueQuery, issueArgs...)
	if err != nil {
		return status, err
	}
	for issueRows.Next() {
		var issue IndexIssueRecord
		if err := issueRows.Scan(&issue.Path, &issue.SourceID, &issue.Code, &issue.Message, &issue.OccurredAt); err != nil {
			issueRows.Close()
			return status, err
		}
		status.RecentIssues = append(status.RecentIssues, issue)
	}
	issueRows.Close()
	return status, nil
}

func (database *IndexDatabase) TryAcquireMutationLease(ownerID, operation string, ttl time.Duration) (*MutationLease, error) {
	now := time.Now().UnixMilli()
	expires := now + ttl.Milliseconds()
	result, err := database.db.Exec(`INSERT INTO mutation_leases(name,owner_id,operation,acquired_at_ms,heartbeat_at_ms,expires_at_ms)
VALUES('global',?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET owner_id=excluded.owner_id,operation=excluded.operation,acquired_at_ms=excluded.acquired_at_ms,heartbeat_at_ms=excluded.heartbeat_at_ms,expires_at_ms=excluded.expires_at_ms
WHERE mutation_leases.expires_at_ms<=?`, ownerID, operation, now, now, expires, now)
	if err != nil {
		return nil, err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return nil, nil
	}
	return &MutationLease{Name: "global", OwnerID: ownerID, Operation: operation, AcquiredAtMS: now, HeartbeatAtMS: now, ExpiresAtMS: expires}, nil
}

func (database *IndexDatabase) RenewMutationLease(ownerID string, ttl time.Duration) (bool, error) {
	now := time.Now().UnixMilli()
	result, err := database.db.Exec("UPDATE mutation_leases SET heartbeat_at_ms=?,expires_at_ms=? WHERE name='global' AND owner_id=? AND expires_at_ms>?", now, now+ttl.Milliseconds(), ownerID, now)
	if err != nil {
		return false, err
	}
	changed, _ := result.RowsAffected()
	return changed > 0, nil
}

func (database *IndexDatabase) ReleaseMutationLease(ownerID string) error {
	_, err := database.db.Exec("DELETE FROM mutation_leases WHERE name='global' AND owner_id=?", ownerID)
	return err
}

func (database *IndexDatabase) ActiveMutationLease() (*MutationLease, error) {
	var lease MutationLease
	err := database.db.QueryRow("SELECT name,owner_id,operation,acquired_at_ms,heartbeat_at_ms,expires_at_ms FROM mutation_leases WHERE name='global' AND expires_at_ms>?", time.Now().UnixMilli()).Scan(&lease.Name, &lease.OwnerID, &lease.Operation, &lease.AcquiredAtMS, &lease.HeartbeatAtMS, &lease.ExpiresAtMS)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &lease, err
}

func (database *IndexDatabase) ClearLocalCache(ctx context.Context) error {
	if _, err := database.db.ExecContext(ctx, "PRAGMA secure_delete=ON"); err != nil {
		return err
	}
	tx, err := database.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(cause error) error { _ = tx.Rollback(); return cause }
	for _, statement := range []string{
		"INSERT INTO chunks_terms(chunks_terms) VALUES('delete-all')",
		"INSERT INTO chunks_trigram(chunks_trigram) VALUES('delete-all')",
		"DELETE FROM document_embeddings", "DELETE FROM chunks", "DELETE FROM documents",
		"DELETE FROM source_index_state", "DELETE FROM index_issues", "DELETE FROM index_runs",
		"UPDATE index_meta SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT) WHERE key='index_revision'",
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return rollback(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := database.Checkpoint(ctx); err != nil {
		return err
	}
	if _, err = database.db.ExecContext(ctx, "VACUUM"); err != nil {
		return err
	}
	return database.Checkpoint(ctx)
}

func (database *IndexDatabase) InitializeSourceStateBaseline(ctx context.Context, sources []Source) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, source := range sources {
		identity := SourceIndexIdentity(source)
		var exists int
		if err := database.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM source_index_state WHERE source_id=?", source.ID).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		var documents int
		if err := database.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM documents WHERE source_id=? AND source_identity=?", source.ID, identity).Scan(&documents); err != nil {
			return err
		}
		if documents > 0 {
			if _, err := database.db.ExecContext(ctx, "INSERT INTO source_index_state(source_id,source_identity,ready,last_run_id,updated_at) VALUES(?,?,1,NULL,?)", source.ID, identity, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (database *IndexDatabase) ReconcileSourceConfiguration(ctx context.Context, sources []Source) (SourceConfigurationReconciliationResult, error) {
	result := SourceConfigurationReconciliationResult{Purged: PurgeSourcesResult{SourceIDs: []string{}}, InvalidatedSourceIDs: []string{}, RecoverySourceIDs: []string{}}
	configured := map[string]Source{}
	for _, source := range sources {
		configured[source.ID] = source
	}
	rows, err := database.db.QueryContext(ctx, `SELECT source_id FROM documents UNION SELECT source_id FROM source_index_state UNION SELECT source_id FROM index_issues WHERE source_id<>'system'`)
	if err != nil {
		return result, err
	}
	removed := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return result, err
		}
		if _, ok := configured[id]; !ok {
			removed = append(removed, id)
		}
	}
	rows.Close()
	if len(removed) > 0 {
		purged, err := database.PurgeSourcesDetailed(ctx, removed)
		if err != nil {
			return result, err
		}
		result.Purged = purged
	}
	for _, source := range sources {
		if !source.Enabled {
			continue
		}
		identity := SourceIndexIdentity(source)
		var mismatched int64
		if err := database.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM documents WHERE source_id=? AND source_identity<>? AND deleted=0", source.ID, identity).Scan(&mismatched); err != nil {
			return result, err
		}
		if mismatched > 0 {
			if _, err := database.InvalidateSourceIdentities(ctx, []Source{sourceWithIdentity(source)}); err != nil {
				return result, err
			}
			result.InvalidatedSourceIDs = append(result.InvalidatedSourceIDs, source.ID)
		}
		var stateIdentity string
		var ready int
		err := database.db.QueryRowContext(ctx, "SELECT source_identity,ready FROM source_index_state WHERE source_id=?", source.ID).Scan(&stateIdentity, &ready)
		if err == sql.ErrNoRows || stateIdentity != identity || ready == 0 {
			result.RecoverySourceIDs = appendUnique(result.RecoverySourceIDs, source.ID)
		} else if err != nil {
			return result, err
		}
	}
	revision, err := database.Revision()
	if err != nil {
		return result, err
	}
	result.Purged.IndexRevision = revision
	return result, nil
}

func (database *IndexDatabase) PurgeSourcesDetailed(ctx context.Context, sourceIDs []string) (PurgeSourcesResult, error) {
	sourceIDs = uniqueStrings(sourceIDs)
	result := PurgeSourcesResult{SourceIDs: sourceIDs}
	if len(sourceIDs) == 0 {
		revision, err := database.Revision()
		if err != nil {
			return result, err
		}
		result.IndexRevision = revision
		return result, nil
	}
	where := "source_id IN (" + placeholders(len(sourceIDs)) + ")"
	args := stringArgs(sourceIDs)
	queries := []struct {
		query string
		value *int64
	}{
		{"SELECT COUNT(*) FROM documents WHERE " + where, &result.Documents},
		{"SELECT COUNT(*) FROM chunks c JOIN documents d ON d.id=c.document_id WHERE d." + where, &result.Chunks},
		{"SELECT COUNT(*) FROM document_embeddings e JOIN documents d ON d.id=e.document_id WHERE d." + where, &result.Embeddings},
		{"SELECT COUNT(*) FROM index_issues WHERE " + where, &result.Issues},
	}
	for _, item := range queries {
		if err := database.db.QueryRowContext(ctx, item.query, args...).Scan(item.value); err != nil {
			return result, err
		}
	}
	if _, err := database.PurgeSources(ctx, sourceIDs); err != nil {
		return result, err
	}
	revision, err := database.Revision()
	if err != nil {
		return result, err
	}
	result.IndexRevision = revision
	return result, nil
}

func (database *IndexDatabase) SetIndexBackendRun(value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = database.db.Exec(`INSERT INTO index_meta(key,value) VALUES('last_backend_run',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, string(raw))
	return err
}

func (database *IndexDatabase) GetIndexBackendRun() (*IndexBackendRun, error) {
	var raw string
	err := database.db.QueryRow("SELECT value FROM index_meta WHERE key='last_backend_run'").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result IndexBackendRun
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("持久化 Go backend 记录无效: %w", err)
	}
	return &result, nil
}

func (database *IndexDatabase) RecordSystemIssue(pathValue, code, message string) error {
	_, err := database.db.Exec("INSERT INTO index_issues(run_id,source_id,path,code,message,occurred_at) VALUES(NULL,'system',?,?,?,?)", pathValue, code, message, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func sortedScopes(sources []Source) []SourceIdentityScope {
	result := make([]SourceIdentityScope, 0, len(sources))
	for _, source := range sources {
		if source.Enabled {
			result = append(result, SourceIdentityScope{SourceID: source.ID, SourceIdentity: SourceIndexIdentity(source)})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SourceID < result[j].SourceID })
	return result
}
