package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schemaSQL = `
PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 5000;

CREATE TABLE IF NOT EXISTS index_meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
) STRICT;
INSERT OR IGNORE INTO index_meta(key, value) VALUES ('schema_version', '3'), ('index_revision', '0');

CREATE TABLE IF NOT EXISTS documents (
  id TEXT PRIMARY KEY,
  canonical_id TEXT NOT NULL,
  source_id TEXT NOT NULL,
  source_label TEXT NOT NULL,
  source_kind TEXT NOT NULL,
  source_identity TEXT NOT NULL,
  absolute_path TEXT NOT NULL UNIQUE,
  relative_path TEXT NOT NULL,
  extension TEXT NOT NULL,
  title TEXT NOT NULL,
  family_key TEXT NOT NULL,
  family_confidence REAL NOT NULL,
  size_bytes INTEGER NOT NULL,
  filesystem_mtime_ms INTEGER NOT NULL,
  filesystem_modified_at TEXT NOT NULL,
  effective_updated_at_ms INTEGER NOT NULL,
  effective_updated_at TEXT NOT NULL,
  date_source TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  indexed_at TEXT NOT NULL,
  stale INTEGER NOT NULL DEFAULT 0,
  deleted INTEGER NOT NULL DEFAULT 0,
  extraction_error TEXT,
  warnings_json TEXT NOT NULL DEFAULT '[]',
  needs_ocr INTEGER NOT NULL DEFAULT 0,
  chunk_count INTEGER NOT NULL DEFAULT 0,
  scan_generation TEXT NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS documents_source_idx ON documents(source_id, deleted);
CREATE INDEX IF NOT EXISTS documents_hash_idx ON documents(content_hash, deleted);
CREATE INDEX IF NOT EXISTS documents_family_idx ON documents(family_key, effective_updated_at_ms DESC);
CREATE INDEX IF NOT EXISTS documents_date_idx ON documents(effective_updated_at_ms DESC);

CREATE TABLE IF NOT EXISTS chunks (
  id TEXT PRIMARY KEY,
  document_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  ordinal INTEGER NOT NULL,
  section_type TEXT NOT NULL,
  heading_path_json TEXT NOT NULL,
  locator TEXT NOT NULL,
  text TEXT NOT NULL,
  normalized_text TEXT NOT NULL,
  search_terms TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  UNIQUE(document_id, ordinal)
) STRICT;
DROP INDEX IF EXISTS chunks_document_idx;
DROP INDEX IF EXISTS chunks_hash_idx;
CREATE TABLE IF NOT EXISTS document_embeddings (
  document_id TEXT PRIMARY KEY REFERENCES documents(id) ON DELETE CASCADE,
  provider_id TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  vector_json TEXT NOT NULL,
  dimensions INTEGER NOT NULL,
  indexed_at TEXT NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS index_runs (
  run_id TEXT PRIMARY KEY,
  phase TEXT NOT NULL,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  discovered INTEGER NOT NULL DEFAULT 0,
  indexed INTEGER NOT NULL DEFAULT 0,
  unchanged INTEGER NOT NULL DEFAULT 0,
  skipped INTEGER NOT NULL DEFAULT 0,
  failed INTEGER NOT NULL DEFAULT 0,
  deleted INTEGER NOT NULL DEFAULT 0,
  current_path TEXT,
  error TEXT
) STRICT;

CREATE TABLE IF NOT EXISTS index_issues (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id TEXT,
  source_id TEXT NOT NULL,
  path TEXT NOT NULL,
  code TEXT NOT NULL,
  message TEXT NOT NULL,
  occurred_at TEXT NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS index_issues_time_idx ON index_issues(occurred_at DESC);

CREATE TABLE IF NOT EXISTS mutation_leases (
  name TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL,
  operation TEXT NOT NULL,
  acquired_at_ms INTEGER NOT NULL,
  heartbeat_at_ms INTEGER NOT NULL,
  expires_at_ms INTEGER NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS source_index_state (
  source_id TEXT PRIMARY KEY,
  source_identity TEXT NOT NULL,
  ready INTEGER NOT NULL,
  last_run_id TEXT,
  updated_at TEXT NOT NULL
) STRICT;

CREATE VIRTUAL TABLE IF NOT EXISTS chunks_terms USING fts5(
  title_terms,
  heading_terms,
  path_terms,
  body_terms,
  tokenize='unicode61 remove_diacritics 2 tokenchars ''_./:-''',
  content='',
  detail=column,
  contentless_delete=1
);

CREATE VIRTUAL TABLE IF NOT EXISTS chunks_trigram USING fts5(
  title,
  heading,
  path,
  tokenize='trigram case_sensitive 0',
  content='',
  contentless_delete=1
);
`

type IndexDatabase struct {
	db   *sql.DB
	path string
}

func (database *IndexDatabase) Path() string { return database.path }

// IntegrityCheck runs SQLite's non-mutating integrity check and returns its
// diagnostic rows. A healthy database returns a single "ok" row.
func (database *IndexDatabase) IntegrityCheck() ([]string, error) {
	rows, err := database.db.Query("PRAGMA integrity_check")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (database *IndexDatabase) EmptySourceIdentityCount() (int64, error) {
	var count int64
	err := database.db.QueryRow("SELECT COUNT(*) FROM documents WHERE deleted=0 AND source_identity=''").Scan(&count)
	return count, err
}

func sqliteWriteDSN(absolutePath string) string {
	slashPath := filepath.ToSlash(absolutePath)
	if len(slashPath) >= 2 && slashPath[1] == ':' {
		slashPath = "/" + slashPath
	}
	return (&url.URL{Scheme: "file", Path: slashPath, RawQuery: "_txlock=immediate"}).String()
}

func OpenIndexDatabase(databasePath string) (*IndexDatabase, error) {
	abs, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, err
	}
	// Every transaction in the Go writer may read metadata before its first
	// mutation. BEGIN IMMEDIATE reserves the single SQLite writer slot up front,
	// so the Node host's short mutation-lease heartbeat cannot invalidate that
	// read snapshot and trigger SQLITE_BUSY_SNAPSHOT midway through a batch.
	db, err := sql.Open("sqlite", sqliteWriteDSN(abs))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var existingMeta int
	if err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='index_meta'").Scan(&existingMeta); err != nil {
		db.Close()
		return nil, err
	}
	if existingMeta > 0 {
		var version string
		if err = db.QueryRow("SELECT value FROM index_meta WHERE key='schema_version'").Scan(&version); err != nil {
			db.Close()
			if err == sql.ErrNoRows {
				return nil, fmt.Errorf("索引 index_meta 缺少 schema_version；请删除可重建缓存后重试")
			}
			return nil, err
		}
		if version != "1" && version != "2" && version != "3" {
			db.Close()
			return nil, fmt.Errorf("不支持的索引 schema_version=%s；请升级 DRAG 或删除可重建缓存后重试", version)
		}
	}
	if _, err = db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化 SQLite/FTS5 失败: %w", err)
	}
	columns, err := db.Query("PRAGMA table_info(documents)")
	if err != nil {
		db.Close()
		return nil, err
	}
	hasSourceIdentity := false
	for columns.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err = columns.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			columns.Close()
			db.Close()
			return nil, err
		}
		if name == "source_identity" {
			hasSourceIdentity = true
		}
	}
	if err = columns.Close(); err != nil {
		db.Close()
		return nil, err
	}
	if !hasSourceIdentity {
		if _, err = db.Exec("ALTER TABLE documents ADD COLUMN source_identity TEXT NOT NULL DEFAULT ''"); err != nil {
			db.Close()
			return nil, err
		}
	}
	if _, err = db.Exec("UPDATE index_meta SET value='3' WHERE key='schema_version'"); err != nil {
		db.Close()
		return nil, err
	}
	database := &IndexDatabase{db: db, path: abs}
	if _, err := database.Revision(); err != nil {
		db.Close()
		return nil, fmt.Errorf("索引 revision 检查失败: %w", err)
	}
	return database, nil
}

func OpenIndexDatabaseReadOnly(databasePath string) (*IndexDatabase, error) {
	abs, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, err
	}
	query := url.Values{"mode": []string{"ro"}}
	if info, statErr := os.Stat(abs + "-wal"); os.IsNotExist(statErr) || statErr == nil && info.Size() == 0 {
		query.Set("immutable", "1")
	}
	slashPath := filepath.ToSlash(abs)
	if len(slashPath) >= 2 && slashPath[1] == ':' {
		slashPath = "/" + slashPath
	}
	dsn := (&url.URL{Scheme: "file", Path: slashPath, RawQuery: query.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var version string
	if err = db.QueryRow("SELECT value FROM index_meta WHERE key='schema_version'").Scan(&version); err != nil {
		db.Close()
		return nil, fmt.Errorf("只读索引 schema 检查失败: %w", err)
	}
	if version != "3" {
		db.Close()
		return nil, fmt.Errorf("只读索引 schema_version=%s 需要先由可写 DRAG 完成迁移", version)
	}
	database := &IndexDatabase{db: db, path: abs}
	if _, err := database.Revision(); err != nil {
		db.Close()
		return nil, fmt.Errorf("只读索引 revision 检查失败: %w", err)
	}
	return database, nil
}

func (database *IndexDatabase) Close() error { return database.db.Close() }

func (database *IndexDatabase) ConfigureIndexing(ctx context.Context, full bool) error {
	_ = full
	statements := []string{
		"PRAGMA busy_timeout=30000",
		"PRAGMA cache_size=-262144",
		"PRAGMA temp_store=MEMORY",
		"PRAGMA mmap_size=1073741824",
		"PRAGMA wal_autocheckpoint=16384",
		"PRAGMA synchronous=NORMAL",
	}
	for _, statement := range statements {
		if _, err := database.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (database *IndexDatabase) Checkpoint(ctx context.Context) error {
	var busy, logFrames, checkpointedFrames int
	if err := database.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		return err
	}
	if busy == 0 {
		return nil
	}
	return database.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &logFrames, &checkpointedFrames)
}

func (database *IndexDatabase) PrepareCleanRebuild(ctx context.Context) error {
	var hasRemainingChunks bool
	if err := database.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM chunks LIMIT 1)").Scan(&hasRemainingChunks); err != nil {
		return err
	}
	// A scoped full rebuild may leave other sources in the shared index. In
	// that case their global FTS rows must remain intact; only a truly empty
	// chunk store may replace the search-index representation.
	if hasRemainingChunks {
		return nil
	}
	_, err := database.db.ExecContext(ctx, `
DROP TABLE IF EXISTS chunks_terms;
DROP TABLE IF EXISTS chunks_trigram;
CREATE VIRTUAL TABLE chunks_terms USING fts5(
  title_terms,
  heading_terms,
  path_terms,
  body_terms,
  tokenize='unicode61 remove_diacritics 2 tokenchars ''_./:-''',
  content='',
  detail=column,
  contentless_delete=1
);
CREATE VIRTUAL TABLE chunks_trigram USING fts5(
  title,
  heading,
  path,
  tokenize='trigram case_sensitive 0',
  content='',
  contentless_delete=1
);`)
	return err
}

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func stringArgs(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func (database *IndexDatabase) LoadExisting(ctx context.Context, sourceIDs []string) (map[string]*ExistingDocument, error) {
	result := map[string]*ExistingDocument{}
	if len(sourceIDs) == 0 {
		return result, nil
	}
	query := fmt.Sprintf("SELECT id, absolute_path, content_hash, size_bytes, filesystem_mtime_ms, stale, deleted, source_identity FROM documents WHERE source_id IN (%s)", placeholders(len(sourceIDs)))
	rows, err := database.db.QueryContext(ctx, query, stringArgs(sourceIDs)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item ExistingDocument
		var absolutePath string
		var stale, deleted int
		if err := rows.Scan(&item.ID, &absolutePath, &item.ContentHash, &item.SizeBytes, &item.FilesystemMtimeMS, &stale, &deleted, &item.SourceIdentity); err != nil {
			return nil, err
		}
		item.Stale = stale != 0
		item.Deleted = deleted != 0
		copyItem := item
		result[CanonicalPathKey(absolutePath)] = &copyItem
	}
	return result, rows.Err()
}

func (database *IndexDatabase) InvalidateSourceIdentities(ctx context.Context, sources []Source) (int64, error) {
	if len(sources) == 0 {
		return 0, nil
	}
	tx, err := database.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	rollback := func(cause error) (int64, error) { _ = tx.Rollback(); return 0, cause }
	var visible int64
	for _, source := range sources {
		var count int64
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM documents WHERE source_id=? AND source_identity<>? AND deleted=0`, source.ID, source.IndexIdentity).Scan(&count); err != nil {
			return rollback(err)
		}
		visible += count
		if _, err = tx.ExecContext(ctx, `UPDATE documents SET deleted=1, stale=1 WHERE source_id=? AND source_identity<>? AND (deleted=0 OR stale=0)`, source.ID, source.IndexIdentity); err != nil {
			return rollback(err)
		}
	}
	if visible > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE index_meta SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT) WHERE key='index_revision'`); err != nil {
			return rollback(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return visible, nil
}

func (database *IndexDatabase) MarkSourceScansReady(ctx context.Context, sources []Source, sourceIDs []string, runID string) error {
	if len(sourceIDs) == 0 {
		return nil
	}
	ready := map[string]struct{}{}
	for _, sourceID := range sourceIDs {
		ready[sourceID] = struct{}{}
	}
	tx, err := database.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	statement, err := tx.PrepareContext(ctx, `
INSERT INTO source_index_state(source_id, source_identity, ready, last_run_id, updated_at)
VALUES (?, ?, 1, ?, ?)
ON CONFLICT(source_id) DO UPDATE SET source_identity=excluded.source_identity, ready=1, last_run_id=excluded.last_run_id, updated_at=excluded.updated_at`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer statement.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, source := range sources {
		if _, ok := ready[source.ID]; !ok {
			continue
		}
		if _, err = statement.ExecContext(ctx, source.ID, source.IndexIdentity, runID, now); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (database *IndexDatabase) MarkSourceScansPending(ctx context.Context, sources []Source) error {
	if len(sources) == 0 {
		return nil
	}
	tx, err := database.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	statement, err := tx.PrepareContext(ctx, `
INSERT INTO source_index_state(source_id, source_identity, ready, last_run_id, updated_at)
VALUES (?, ?, 0, NULL, ?)
ON CONFLICT(source_id) DO UPDATE SET source_identity=excluded.source_identity, ready=0, last_run_id=NULL, updated_at=excluded.updated_at`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer statement.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, source := range sources {
		if _, err = statement.ExecContext(ctx, source.ID, source.IndexIdentity, now); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (database *IndexDatabase) StartRun(ctx context.Context, summary RunSummary) error {
	_, err := database.db.ExecContext(ctx, `
INSERT INTO index_runs(run_id, phase, started_at, finished_at, discovered, indexed, unchanged, skipped, failed, deleted, current_path, error)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(run_id) DO UPDATE SET phase=excluded.phase, started_at=excluded.started_at, finished_at=excluded.finished_at,
discovered=excluded.discovered, indexed=excluded.indexed, unchanged=excluded.unchanged, skipped=excluded.skipped,
failed=excluded.failed, deleted=excluded.deleted, current_path=excluded.current_path, error=excluded.error`,
		summary.RunID, summary.Phase, summary.StartedAt, summary.FinishedAt, summary.Discovered, summary.Indexed,
		summary.Unchanged, summary.Skipped, summary.Failed, summary.Deleted, summary.CurrentPath, summary.Error)
	return err
}

func (database *IndexDatabase) UpdateRun(ctx context.Context, summary RunSummary) error {
	_, err := database.db.ExecContext(ctx, `UPDATE index_runs SET phase=?, finished_at=?, discovered=?, indexed=?, unchanged=?, skipped=?, failed=?, deleted=?, current_path=?, error=? WHERE run_id=?`,
		summary.Phase, summary.FinishedAt, summary.Discovered, summary.Indexed, summary.Unchanged, summary.Skipped,
		summary.Failed, summary.Deleted, summary.CurrentPath, summary.Error, summary.RunID)
	return err
}

func (database *IndexDatabase) RecordIssues(ctx context.Context, runID string, issues []Issue) error {
	if len(issues) == 0 {
		return nil
	}
	tx, err := database.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	statement, err := tx.PrepareContext(ctx, `INSERT INTO index_issues(run_id, source_id, path, code, message, occurred_at) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer statement.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, issue := range issues {
		if _, err = statement.ExecContext(ctx, runID, issue.SourceID, issue.Path, issue.Code, issue.Message, now); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (database *IndexDatabase) PurgeSources(ctx context.Context, sourceIDs []string) (int64, error) {
	if len(sourceIDs) == 0 {
		return 0, nil
	}
	tx, err := database.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	rollback := func(err error) (int64, error) { _ = tx.Rollback(); return 0, err }
	args := stringArgs(sourceIDs)
	where := fmt.Sprintf("source_id IN (%s)", placeholders(len(sourceIDs)))
	var otherDocuments int64
	if err = tx.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM documents WHERE NOT (%s)", where), args...).Scan(&otherDocuments); err != nil {
		return rollback(err)
	}
	var documents int64
	if err = tx.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM documents WHERE %s", where), args...).Scan(&documents); err != nil {
		return rollback(err)
	}
	if documents == 0 {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM index_issues WHERE %s", where), args...); err != nil {
			return rollback(err)
		}
		if _, err = tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM source_index_state WHERE %s", where), args...); err != nil {
			return rollback(err)
		}
		if err = tx.Commit(); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if otherDocuments == 0 {
		if _, err = tx.ExecContext(ctx, "INSERT INTO chunks_terms(chunks_terms) VALUES('delete-all')"); err != nil {
			return rollback(err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO chunks_trigram(chunks_trigram) VALUES('delete-all')"); err != nil {
			return rollback(err)
		}
	} else {
		for {
			rows, queryErr := tx.QueryContext(ctx, fmt.Sprintf(`SELECT c.rowid FROM chunks c JOIN documents d ON d.id=c.document_id WHERE d.%s LIMIT 512`, where), args...)
			if queryErr != nil {
				return rollback(queryErr)
			}
			rowIDs := []int64{}
			for rows.Next() {
				var rowID int64
				if scanErr := rows.Scan(&rowID); scanErr != nil {
					rows.Close()
					return rollback(scanErr)
				}
				rowIDs = append(rowIDs, rowID)
			}
			rowsErr := rows.Err()
			rows.Close()
			if rowsErr != nil {
				return rollback(rowsErr)
			}
			if len(rowIDs) == 0 {
				break
			}
			rowArgs := make([]any, len(rowIDs))
			for index, rowID := range rowIDs {
				rowArgs[index] = rowID
			}
			for _, table := range []string{"chunks_terms", "chunks_trigram", "chunks"} {
				if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE rowid IN ("+placeholders(len(rowIDs))+")", rowArgs...); err != nil {
					return rollback(err)
				}
			}
		}
	}
	if _, err = tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM documents WHERE %s", where), args...); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM index_issues WHERE %s", where), args...); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM source_index_state WHERE %s", where), args...); err != nil {
		return rollback(err)
	}
	if _, err = reconcileDuplicatesTx(ctx, tx); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE index_meta SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT) WHERE key='index_revision'`); err != nil {
		return rollback(err)
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return documents, nil
}

type batchWriter struct {
	database         *IndexDatabase
	tx               *sql.Tx
	deleteTerms      *sql.Stmt
	deleteTrigram    *sql.Stmt
	deleteChunks     *sql.Stmt
	markUnchanged    *sql.Stmt
	markFailed       *sql.Stmt
	insertIssue      *sql.Stmt
	chunksWritten    int64
	nextRowID        int64
	cleanFull        bool
	pendingDocuments [][]any
	pendingChunks    []pendingChunkRow
	dirty            bool
}

type pendingChunkRow struct {
	chunkArgs   []any
	termArgs    []any
	trigramArgs []any
}

const chunkInsertBatchSize = 512

func (database *IndexDatabase) BeginBatch(ctx context.Context, cleanFull bool) (*batchWriter, error) {
	tx, err := database.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	writer := &batchWriter{
		database:         database,
		tx:               tx,
		cleanFull:        cleanFull,
		pendingDocuments: make([][]any, 0, 64),
		pendingChunks:    make([]pendingChunkRow, 0, chunkInsertBatchSize),
	}
	prepare := func(target **sql.Stmt, query string) error {
		*target, err = tx.PrepareContext(ctx, query)
		return err
	}
	type statementSpec struct {
		target **sql.Stmt
		query  string
	}
	statements := []statementSpec{
		{&writer.markUnchanged, `UPDATE documents SET scan_generation=?, deleted=0, source_id=?, source_label=?, source_kind=?, source_identity=?, relative_path=?, extension=?, size_bytes=?, filesystem_mtime_ms=?, filesystem_modified_at=? WHERE id=?`},
		{&writer.markFailed, `UPDATE documents SET stale=1, extraction_error=?, scan_generation=?, deleted=0 WHERE id=?`},
		{&writer.insertIssue, `INSERT INTO index_issues(run_id, source_id, path, code, message, occurred_at) VALUES (?, ?, ?, ?, ?, ?)`},
	}
	if !cleanFull {
		statements = append(statements,
			statementSpec{&writer.deleteTerms, "DELETE FROM chunks_terms WHERE rowid=?"},
			statementSpec{&writer.deleteTrigram, "DELETE FROM chunks_trigram WHERE rowid=?"},
			statementSpec{&writer.deleteChunks, "DELETE FROM chunks WHERE document_id=?"},
		)
	}
	for _, item := range statements {
		if prepare(item.target, item.query) != nil {
			writer.Rollback()
			return nil, err
		}
	}
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(rowid), 0) FROM chunks").Scan(&writer.nextRowID); err != nil {
		writer.Rollback()
		return nil, err
	}
	return writer, nil
}

func isoMilliseconds(milliseconds int64) string {
	return time.UnixMilli(milliseconds).UTC().Format("2006-01-02T15:04:05.000Z")
}

func (writer *batchWriter) existingChunkRows(ctx context.Context, documentID string) ([]int64, error) {
	rows, err := writer.tx.QueryContext(ctx, "SELECT rowid FROM chunks WHERE document_id=?", documentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []int64{}
	for rows.Next() {
		var rowID int64
		if err := rows.Scan(&rowID); err != nil {
			return nil, err
		}
		result = append(result, rowID)
	}
	return result, rows.Err()
}

func (writer *batchWriter) Write(ctx context.Context, runID, generation string, result TaskResult) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if result.Unchanged {
		if result.Existing == nil {
			return fmt.Errorf("内容未变化但索引中不存在对应文档")
		}
		candidate := result.Candidate
		if result.Existing.Stale || result.Existing.Deleted {
			writer.dirty = true
		}
		_, err := writer.markUnchanged.ExecContext(ctx, generation, candidate.SourceID, candidate.SourceLabel, candidate.SourceKind, candidate.SourceIdentity, candidate.RelativePath, candidate.Extension, candidate.SizeBytes, candidate.FilesystemMtimeMS, isoMilliseconds(candidate.FilesystemMtimeMS), result.Existing.ID)
		return err
	}
	if result.Issue != nil {
		if result.Existing != nil {
			writer.dirty = true
			if _, err := writer.markFailed.ExecContext(ctx, result.Issue.Message, generation, result.Existing.ID); err != nil {
				return err
			}
		}
		_, err := writer.insertIssue.ExecContext(ctx, runID, result.Issue.SourceID, result.Issue.Path, result.Issue.Code, result.Issue.Message, now)
		return err
	}
	if result.Draft == nil {
		return fmt.Errorf("索引任务既没有 draft 也没有 issue")
	}
	draft := result.Draft
	writer.dirty = true
	if !writer.cleanFull {
		rowIDs, err := writer.existingChunkRows(ctx, draft.ID)
		if err != nil {
			return err
		}
		for _, rowID := range rowIDs {
			if _, err = writer.deleteTerms.ExecContext(ctx, rowID); err != nil {
				return err
			}
			if _, err = writer.deleteTrigram.ExecContext(ctx, rowID); err != nil {
				return err
			}
		}
		if _, err = writer.deleteChunks.ExecContext(ctx, draft.ID); err != nil {
			return err
		}
	}
	warnings, _ := json.Marshal(draft.Warnings)
	writer.pendingDocuments = append(writer.pendingDocuments, []any{
		draft.ID, draft.ID, draft.Candidate.SourceID, draft.Candidate.SourceLabel, draft.Candidate.SourceKind, draft.Candidate.SourceIdentity,
		draft.Candidate.AbsolutePath, draft.Candidate.RelativePath, draft.Candidate.Extension, draft.Title,
		draft.FamilyKey, draft.FamilyConfidence, draft.Candidate.SizeBytes, draft.Candidate.FilesystemMtimeMS,
		isoMilliseconds(draft.Candidate.FilesystemMtimeMS), draft.Date.EffectiveUpdatedAtMS,
		isoMilliseconds(draft.Date.EffectiveUpdatedAtMS), draft.Date.DateSource, draft.ContentHash, now,
		string(warnings), boolInt(draft.NeedsOCR), len(draft.Chunks), generation,
	})
	titleTerms := BuildSearchTerms(draft.Title)
	pathTerms := BuildSearchTerms(draft.Candidate.RelativePath)
	headingCache := map[string]string{}
	seenHeading := map[string]bool{}
	for _, chunk := range draft.Chunks {
		writer.nextRowID++
		rowID := writer.nextRowID
		headingJSON, _ := json.Marshal(chunk.HeadingPath)
		heading := strings.Join(chunk.HeadingPath, " / ")
		chunkID := fmt.Sprintf("chunk_%s_%s_%d", chunk.ContentHash[:16], draft.ID[len(draft.ID)-8:], chunk.Ordinal)
		chunkArgs := []any{rowID, chunkID, draft.ID, chunk.Ordinal, chunk.SectionType, string(headingJSON), chunk.Locator, chunk.Text, "", "", chunk.ContentHash}
		headingTerms, ok := headingCache[heading]
		if !ok {
			headingTerms = BuildSearchTerms(heading)
			headingCache[heading] = headingTerms
		}
		firstDocumentChunk := chunk.Ordinal == 0
		firstHeadingChunk := !seenHeading[heading]
		seenHeading[heading] = true
		indexedTitleTerms := ""
		indexedPathTerms := ""
		indexedHeadingTerms := ""
		trigramTitle := ""
		trigramHeading := ""
		trigramPath := ""
		if firstDocumentChunk {
			indexedTitleTerms = titleTerms
			indexedPathTerms = pathTerms
			trigramTitle = draft.Title
			trigramPath = draft.Candidate.RelativePath
		}
		if firstHeadingChunk {
			indexedHeadingTerms = headingTerms
			trigramHeading = heading
		}
		bodyTerms := chunk.SearchTerms
		if bodyTerms == "" {
			bodyTerms = BuildBodySearchTerms(chunk.Text)
		}
		termArgs := []any{rowID, indexedTitleTerms, indexedHeadingTerms, indexedPathTerms, bodyTerms}
		var trigramArgs []any
		if trigramTitle != "" || trigramHeading != "" || trigramPath != "" {
			trigramArgs = []any{rowID, trigramTitle, trigramHeading, trigramPath}
		}
		writer.pendingChunks = append(writer.pendingChunks, pendingChunkRow{chunkArgs: chunkArgs, termArgs: termArgs, trigramArgs: trigramArgs})
		writer.chunksWritten++
		if len(writer.pendingChunks) >= chunkInsertBatchSize {
			if err := writer.flushChunks(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func (writer *batchWriter) flushChunks(ctx context.Context) error {
	if err := writer.flushDocuments(ctx); err != nil {
		return err
	}
	if len(writer.pendingChunks) == 0 {
		return nil
	}
	chunkArgs := make([]any, 0, len(writer.pendingChunks)*11)
	termArgs := make([]any, 0, len(writer.pendingChunks)*5)
	trigramArgs := make([]any, 0, len(writer.pendingChunks)*4)
	trigramRows := 0
	for _, pending := range writer.pendingChunks {
		chunkArgs = append(chunkArgs, pending.chunkArgs...)
		termArgs = append(termArgs, pending.termArgs...)
		if len(pending.trigramArgs) > 0 {
			trigramArgs = append(trigramArgs, pending.trigramArgs...)
			trigramRows++
		}
	}
	rows := len(writer.pendingChunks)
	chunkSQL := `INSERT INTO chunks(rowid, id, document_id, ordinal, section_type, heading_path_json, locator, text, normalized_text, search_terms, content_hash) VALUES ` + valuesClause(rows, 11)
	if _, err := writer.tx.ExecContext(ctx, chunkSQL, chunkArgs...); err != nil {
		return err
	}
	termSQL := `INSERT INTO chunks_terms(rowid, title_terms, heading_terms, path_terms, body_terms) VALUES ` + valuesClause(rows, 5)
	if _, err := writer.tx.ExecContext(ctx, termSQL, termArgs...); err != nil {
		return err
	}
	if trigramRows > 0 {
		trigramSQL := `INSERT INTO chunks_trigram(rowid, title, heading, path) VALUES ` + valuesClause(trigramRows, 4)
		if _, err := writer.tx.ExecContext(ctx, trigramSQL, trigramArgs...); err != nil {
			return err
		}
	}
	writer.pendingChunks = writer.pendingChunks[:0]
	return nil
}

func (writer *batchWriter) flushDocuments(ctx context.Context) error {
	if len(writer.pendingDocuments) == 0 {
		return nil
	}
	arguments := make([]any, 0, len(writer.pendingDocuments)*24)
	for _, document := range writer.pendingDocuments {
		arguments = append(arguments, document...)
	}
	row := "(" + strings.TrimSuffix(strings.Repeat("?,", 20), ",") + ",0,0,NULL,?,?,?,?)"
	values := strings.TrimSuffix(strings.Repeat(row+",", len(writer.pendingDocuments)), ",")
	query := `INSERT INTO documents(id, canonical_id, source_id, source_label, source_kind, source_identity, absolute_path, relative_path, extension, title, family_key, family_confidence, size_bytes, filesystem_mtime_ms, filesystem_modified_at, effective_updated_at_ms, effective_updated_at, date_source, content_hash, indexed_at, stale, deleted, extraction_error, warnings_json, needs_ocr, chunk_count, scan_generation)
VALUES ` + values + `
ON CONFLICT(id) DO UPDATE SET canonical_id=excluded.canonical_id, source_id=excluded.source_id, source_label=excluded.source_label, source_kind=excluded.source_kind, source_identity=excluded.source_identity, absolute_path=excluded.absolute_path, relative_path=excluded.relative_path, extension=excluded.extension, title=excluded.title, family_key=excluded.family_key, family_confidence=excluded.family_confidence, size_bytes=excluded.size_bytes, filesystem_mtime_ms=excluded.filesystem_mtime_ms, filesystem_modified_at=excluded.filesystem_modified_at, effective_updated_at_ms=excluded.effective_updated_at_ms, effective_updated_at=excluded.effective_updated_at, date_source=excluded.date_source, content_hash=excluded.content_hash, indexed_at=excluded.indexed_at, stale=0, deleted=0, extraction_error=NULL, warnings_json=excluded.warnings_json, needs_ocr=excluded.needs_ocr, chunk_count=excluded.chunk_count, scan_generation=excluded.scan_generation`
	if _, err := writer.tx.ExecContext(ctx, query, arguments...); err != nil {
		return err
	}
	writer.pendingDocuments = writer.pendingDocuments[:0]
	return nil
}

func valuesClause(rows, columns int) string {
	row := "(" + strings.TrimSuffix(strings.Repeat("?,", columns), ",") + ")"
	return strings.TrimSuffix(strings.Repeat(row+",", rows), ",")
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (writer *batchWriter) Commit(ctx context.Context) error {
	if err := writer.flushChunks(ctx); err != nil {
		writer.Rollback()
		return err
	}
	for _, statement := range []*sql.Stmt{writer.deleteTerms, writer.deleteTrigram, writer.deleteChunks, writer.markUnchanged, writer.markFailed, writer.insertIssue} {
		if statement != nil {
			_ = statement.Close()
		}
	}
	if writer.dirty {
		if _, err := writer.tx.ExecContext(ctx, `UPDATE index_meta SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT) WHERE key='index_revision'`); err != nil {
			writer.Rollback()
			return err
		}
	}
	return writer.tx.Commit()
}

func (writer *batchWriter) Rollback() {
	for _, statement := range []*sql.Stmt{writer.deleteTerms, writer.deleteTrigram, writer.deleteChunks, writer.markUnchanged, writer.markFailed, writer.insertIssue} {
		if statement != nil {
			_ = statement.Close()
		}
	}
	_ = writer.tx.Rollback()
}

func (database *IndexDatabase) MarkMissingDeleted(ctx context.Context, generation string, sourceIDs []string) (int, error) {
	if len(sourceIDs) == 0 {
		return 0, nil
	}
	tx, err := database.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	rollback := func(cause error) (int, error) { _ = tx.Rollback(); return 0, cause }
	args := append(stringArgs(sourceIDs), generation)
	query := fmt.Sprintf("UPDATE documents SET deleted=1 WHERE source_id IN (%s) AND scan_generation<>? AND deleted=0", placeholders(len(sourceIDs)))
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return rollback(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return rollback(err)
	}
	if count > 0 {
		if _, err := reconcileDuplicatesTx(ctx, tx); err != nil {
			return rollback(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE index_meta SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT) WHERE key='index_revision'`); err != nil {
			return rollback(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(count), nil
}

func reconcileDuplicatesTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	result, err := tx.ExecContext(ctx, `
WITH ranked AS (
  SELECT id, first_value(id) OVER (PARTITION BY content_hash ORDER BY effective_updated_at_ms DESC, relative_path ASC, id ASC) AS canonical_id
  FROM documents WHERE deleted=0 AND content_hash<>''
)
UPDATE documents SET canonical_id=(SELECT ranked.canonical_id FROM ranked WHERE ranked.id=documents.id)
WHERE deleted=0 AND content_hash<>''
  AND canonical_id<>(SELECT ranked.canonical_id FROM ranked WHERE ranked.id=documents.id)`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (database *IndexDatabase) ReconcileDuplicates(ctx context.Context) error {
	tx, err := database.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(cause error) error { _ = tx.Rollback(); return cause }
	changed, err := reconcileDuplicatesTx(ctx, tx)
	if err != nil {
		return rollback(err)
	}
	if changed > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE index_meta SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT) WHERE key='index_revision'`); err != nil {
			return rollback(err)
		}
	}
	return tx.Commit()
}

func (database *IndexDatabase) BumpRevision(ctx context.Context) error {
	_, err := database.db.ExecContext(ctx, `UPDATE index_meta SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT) WHERE key='index_revision'`)
	return err
}

func (database *IndexDatabase) CountChunks(ctx context.Context) (int64, error) {
	var count int64
	err := database.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM chunks").Scan(&count)
	return count, err
}
