import { mkdirSync, statSync } from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { DatabaseSync } from "node:sqlite";
import type {
  IndexIssue,
  IndexRunSummary,
  IndexStatus,
  KnowledgeSourceConfig,
  SectionType,
  SourceKind,
} from "../shared/contracts.js";
import { sourceIndexIdentity } from "./paths.js";
import { buildSearchTerms } from "./text.js";
import type { DocumentDraft, FileCandidate, StoredChunkRow, StoredDocumentRow } from "./types.js";

export interface LexicalCandidateRow extends Omit<StoredChunkRow, "id" | "document_id" | "rowid">, StoredDocumentRow {
  chunk_id: string;
  chunk_document_id: string;
  lexical_rank: number;
  exact_anchors?: string[];
}

export interface PurgeSourcesResult {
  sourceIds: string[];
  documents: number;
  chunks: number;
  embeddings: number;
  issues: number;
  indexRevision: number;
}

export interface SourceConfigurationReconciliationResult {
  purged: PurgeSourcesResult;
  invalidatedSourceIds: string[];
  recoverySourceIds: string[];
}

export interface SourceIdentityScope {
  sourceId: string;
  sourceIdentity: string;
}

export interface MutationLease {
  name: "global";
  ownerId: string;
  operation: string;
  acquiredAtMs: number;
  heartbeatAtMs: number;
  expiresAtMs: number;
}

type SqliteValue = string | number | bigint | Uint8Array | null;

interface CandidateSourceFilter {
  sourceIds?: string[];
  sourceKinds?: SourceKind[];
  sourceScopes?: SourceIdentityScope[];
}

function candidateSourceWhere(filter: CandidateSourceFilter, alias = "d"): { sql: string; values: string[] } {
  const clauses: string[] = [];
  const values: string[] = [];
  const sourceIds = [...new Set(filter.sourceIds ?? [])];
  const sourceKinds = [...new Set(filter.sourceKinds ?? [])];
  if (sourceIds.length > 0) {
    clauses.push(`${alias}.source_id IN (${sourceIds.map(() => "?").join(",")})`);
    values.push(...sourceIds);
  }
  if (sourceKinds.length > 0) {
    clauses.push(`${alias}.source_kind IN (${sourceKinds.map(() => "?").join(",")})`);
    values.push(...sourceKinds);
  }
  const sourceScopes = filter.sourceScopes ?? [];
  if (sourceScopes.length > 0) {
    clauses.push(`(${sourceScopes.map(() => `(${alias}.source_id = ? AND ${alias}.source_identity = ?)`).join(" OR ")})`);
    for (const scope of sourceScopes) values.push(scope.sourceId, scope.sourceIdentity);
  }
  return { sql: clauses.length > 0 ? ` AND ${clauses.join(" AND ")}` : "", values };
}

function candidateCanonicalWhere(filter: CandidateSourceFilter): { sql: string; values: string[] } {
  const hasSourceFilter = (filter.sourceIds?.length ?? 0) > 0
    || (filter.sourceKinds?.length ?? 0) > 0
    || (filter.sourceScopes?.length ?? 0) > 0;
  if (!hasSourceFilter) return { sql: " AND d.id = d.canonical_id", values: [] };
  const preferredSourceWhere = candidateSourceWhere(filter, "preferred");
  return {
    sql: ` AND d.id = (
      SELECT preferred.id
      FROM documents preferred
      WHERE preferred.content_hash = d.content_hash AND preferred.deleted = 0${preferredSourceWhere.sql}
      ORDER BY preferred.effective_updated_at_ms DESC, preferred.relative_path ASC, preferred.id ASC
      LIMIT 1
    )`,
    values: preferredSourceWhere.values,
  };
}

function asRecord(value: unknown): Record<string, SqliteValue> {
  return value as Record<string, SqliteValue>;
}

function parseRun(row: Record<string, SqliteValue> | undefined): IndexRunSummary | null {
  if (!row) return null;
  return {
    runId: String(row.run_id),
    phase: String(row.phase) as IndexRunSummary["phase"],
    startedAt: String(row.started_at),
    finishedAt: row.finished_at === null ? null : String(row.finished_at),
    discovered: Number(row.discovered),
    indexed: Number(row.indexed),
    unchanged: Number(row.unchanged),
    skipped: Number(row.skipped),
    failed: Number(row.failed),
    deleted: Number(row.deleted),
    currentPath: row.current_path === null ? null : String(row.current_path),
    error: row.error === null ? null : String(row.error),
  };
}

function readOnlyDatabaseLocation(databasePath: string): string {
  try {
    if (statSync(`${databasePath}-wal`).size > 0) return databasePath;
  } catch (error) {
    const missing = error instanceof Error && "code" in error && error.code === "ENOENT";
    if (!missing) throw error;
  }
  const location = pathToFileURL(databasePath);
  location.searchParams.set("immutable", "1");
  return location.href;
}

export class IndexDatabase {
  readonly databasePath: string;
  readonly db: DatabaseSync;
  readonly fts5Available: boolean;
  readonly trigramAvailable: boolean;

  constructor(databasePath: string, options: { readOnly?: boolean } = {}) {
    this.databasePath = databasePath;
    if (!options.readOnly) mkdirSync(path.dirname(databasePath), { recursive: true });
    this.db = new DatabaseSync(options.readOnly ? readOnlyDatabaseLocation(databasePath) : databasePath, {
      timeout: 5_000,
      allowExtension: false,
      defensive: true,
      readOnly: options.readOnly ?? false,
    });
    try {
      this.db.exec(options.readOnly
        ? "PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000;"
        : "PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL; PRAGMA synchronous = NORMAL; PRAGMA busy_timeout = 5000;");
      if (this.hasCurrentSchema()) {
        this.fts5Available = this.hasTable("chunks_terms");
        this.trigramAvailable = this.fts5Available && this.hasTable("chunks_trigram");
      } else if (options.readOnly) {
        throw new Error("只读模式无法初始化或迁移索引 schema；请先用 DRAG GUI 或写入模式完成修复");
      } else {
        this.fts5Available = this.initializeSchema();
        this.trigramAvailable = this.initializeTrigram();
      }
    } catch (error) {
      try { this.db.close(); } catch { /* preserve the original database error */ }
      throw error;
    }
  }

  private hasTable(name: string): boolean {
    return Boolean(this.db.prepare("SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?").get(name));
  }

  private hasCurrentSchema(): boolean {
    if (!this.hasTable("index_meta")) return false;
    const version = this.db.prepare("SELECT value FROM index_meta WHERE key = 'schema_version'").get();
    if (!version || String(asRecord(version).value) !== "3") return false;
    const requiredTables = [
      "documents",
      "chunks",
      "document_embeddings",
      "index_runs",
      "index_issues",
      "mutation_leases",
      "source_index_state",
    ];
    if (requiredTables.some((name) => !this.hasTable(name))) return false;
    return this.db.prepare("PRAGMA table_info(documents)").all()
      .some((row) => String(asRecord(row).name) === "source_identity");
  }

  private initializeSchema(): boolean {
    const existingMeta = this.db.prepare("SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'index_meta'").get();
    if (existingMeta) {
      const existingVersion = this.db.prepare("SELECT value FROM index_meta WHERE key = 'schema_version'").get();
      if (!existingVersion) {
        throw new Error("索引 index_meta 缺少 schema_version；请删除可重建缓存后重试");
      }
      const value = String(asRecord(existingVersion).value);
      if (!new Set(["1", "2", "3"]).has(value)) {
        throw new Error(`不支持的索引 schema_version=${value}；请升级 DRAG 或删除可重建缓存后重试`);
      }
    }
    this.db.exec(`
      CREATE TABLE IF NOT EXISTS index_meta (
        key TEXT PRIMARY KEY,
        value TEXT NOT NULL
      ) STRICT;

      INSERT OR IGNORE INTO index_meta(key, value) VALUES
        ('schema_version', '3'),
        ('index_revision', '0');

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
    `);
    const schemaVersion = String(asRecord(this.db.prepare("SELECT value FROM index_meta WHERE key = 'schema_version'").get()).value ?? "1");
    const documentColumns = new Set(this.db.prepare("PRAGMA table_info(documents)").all().map((row) => String(asRecord(row).name)));
    if (!documentColumns.has("source_identity")) {
      this.db.exec("ALTER TABLE documents ADD COLUMN source_identity TEXT NOT NULL DEFAULT '';");
    }
    if (schemaVersion === "1") {
      this.db.exec(`
        DROP TABLE IF EXISTS chunks_terms;
        DROP TABLE IF EXISTS chunks_trigram;
      `);
    }
    if (schemaVersion !== "3") {
      this.db.prepare("UPDATE index_meta SET value = '3' WHERE key = 'schema_version'").run();
    }
    this.db.exec(`
      CREATE TABLE IF NOT EXISTS source_index_state (
        source_id TEXT PRIMARY KEY,
        source_identity TEXT NOT NULL,
        ready INTEGER NOT NULL,
        last_run_id TEXT,
        updated_at TEXT NOT NULL
      ) STRICT;
    `);
    try {
      this.db.exec(`
        CREATE VIRTUAL TABLE IF NOT EXISTS chunks_terms USING fts5(
          title_terms,
          heading_terms,
          path_terms,
          body_terms,
          tokenize='unicode61 remove_diacritics 2',
          content='',
          contentless_delete=1
        );
      `);
      return true;
    } catch {
      return false;
    }
  }

  private initializeTrigram(): boolean {
    if (!this.fts5Available) return false;
    try {
      this.db.exec(`
        CREATE VIRTUAL TABLE IF NOT EXISTS chunks_trigram USING fts5(
          title,
          heading,
          path,
          tokenize='trigram case_sensitive 0',
          content='',
          contentless_delete=1
        );
      `);
      return true;
    } catch {
      return false;
    }
  }

  close(): void {
    if (this.db.isOpen) this.db.close();
  }

  getRevision(): number {
    const row = this.db.prepare("SELECT value FROM index_meta WHERE key = 'index_revision'").get();
    return Number(asRecord(row).value ?? 0);
  }

  private bumpRevision(): number {
    const next = this.getRevision() + 1;
    this.db.prepare("UPDATE index_meta SET value = ? WHERE key = 'index_revision'").run(String(next));
    return next;
  }

  tryAcquireMutationLease(
    ownerId: string,
    operation: string,
    ttlMs: number,
    nowMs = Date.now(),
  ): MutationLease | null {
    const expiresAtMs = nowMs + ttlMs;
    this.db.exec("BEGIN IMMEDIATE");
    try {
      const current = this.db.prepare("SELECT * FROM mutation_leases WHERE name = 'global'").get();
      if (current && Number(asRecord(current).expires_at_ms) > nowMs) {
        this.db.exec("COMMIT");
        return null;
      }
      this.db.prepare(`
        INSERT INTO mutation_leases(name, owner_id, operation, acquired_at_ms, heartbeat_at_ms, expires_at_ms)
        VALUES ('global', ?, ?, ?, ?, ?)
        ON CONFLICT(name) DO UPDATE SET
          owner_id=excluded.owner_id,
          operation=excluded.operation,
          acquired_at_ms=excluded.acquired_at_ms,
          heartbeat_at_ms=excluded.heartbeat_at_ms,
          expires_at_ms=excluded.expires_at_ms
      `).run(ownerId, operation, nowMs, nowMs, expiresAtMs);
      this.db.exec("COMMIT");
      return {
        name: "global",
        ownerId,
        operation,
        acquiredAtMs: nowMs,
        heartbeatAtMs: nowMs,
        expiresAtMs,
      };
    } catch (error) {
      if (this.db.isTransaction) this.db.exec("ROLLBACK");
      throw error;
    }
  }

  renewMutationLease(ownerId: string, ttlMs: number, nowMs = Date.now()): boolean {
    const result = this.db.prepare(`
      UPDATE mutation_leases
      SET heartbeat_at_ms = ?, expires_at_ms = ?
      WHERE name = 'global' AND owner_id = ? AND expires_at_ms > ?
    `).run(nowMs, nowMs + ttlMs, ownerId, nowMs);
    return Number(result.changes) === 1;
  }

  releaseMutationLease(ownerId: string): boolean {
    const result = this.db.prepare("DELETE FROM mutation_leases WHERE name = 'global' AND owner_id = ?").run(ownerId);
    return Number(result.changes) === 1;
  }

  getActiveMutationLease(nowMs = Date.now()): MutationLease | null {
    const row = this.db.prepare("SELECT * FROM mutation_leases WHERE name = 'global' AND expires_at_ms > ?").get(nowMs);
    if (!row) return null;
    const record = asRecord(row);
    return {
      name: "global",
      ownerId: String(record.owner_id),
      operation: String(record.operation),
      acquiredAtMs: Number(record.acquired_at_ms),
      heartbeatAtMs: Number(record.heartbeat_at_ms),
      expiresAtMs: Number(record.expires_at_ms),
    };
  }

  startRun(summary: IndexRunSummary): void {
    this.db.prepare(`
      INSERT INTO index_runs(
        run_id, phase, started_at, finished_at, discovered, indexed, unchanged,
        skipped, failed, deleted, current_path, error
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `).run(
      summary.runId,
      summary.phase,
      summary.startedAt,
      summary.finishedAt,
      summary.discovered,
      summary.indexed,
      summary.unchanged,
      summary.skipped,
      summary.failed,
      summary.deleted,
      summary.currentPath,
      summary.error,
    );
  }

  updateRun(summary: IndexRunSummary): void {
    this.db.prepare(`
      UPDATE index_runs SET
        phase = ?, finished_at = ?, discovered = ?, indexed = ?, unchanged = ?,
        skipped = ?, failed = ?, deleted = ?, current_path = ?, error = ?
      WHERE run_id = ?
    `).run(
      summary.phase,
      summary.finishedAt,
      summary.discovered,
      summary.indexed,
      summary.unchanged,
      summary.skipped,
      summary.failed,
      summary.deleted,
      summary.currentPath,
      summary.error,
      summary.runId,
    );
  }

  recordIssue(issue: IndexIssue, runId: string | null): void {
    this.db.prepare(`
      INSERT INTO index_issues(run_id, source_id, path, code, message, occurred_at)
      VALUES (?, ?, ?, ?, ?, ?)
    `).run(runId, issue.sourceId, issue.path, issue.code, issue.message, issue.occurredAt);
  }

  resetIndexContent(): void {
    this.db.exec("BEGIN IMMEDIATE");
    try {
      if (this.fts5Available) this.db.exec("INSERT INTO chunks_terms(chunks_terms) VALUES('delete-all')");
      if (this.trigramAvailable) this.db.exec("INSERT INTO chunks_trigram(chunks_trigram) VALUES('delete-all')");
      this.db.exec("DELETE FROM document_embeddings; DELETE FROM chunks; DELETE FROM documents; DELETE FROM source_index_state;");
      this.db.exec("COMMIT");
    } catch (error) {
      this.db.exec("ROLLBACK");
      throw error;
    }
  }

  clearLocalCache(): number {
    this.db.exec("BEGIN IMMEDIATE");
    try {
      if (this.fts5Available) this.db.exec("INSERT INTO chunks_terms(chunks_terms) VALUES('delete-all')");
      if (this.trigramAvailable) this.db.exec("INSERT INTO chunks_trigram(chunks_trigram) VALUES('delete-all')");
      this.db.exec(`
        DELETE FROM document_embeddings;
        DELETE FROM chunks;
        DELETE FROM documents;
        DELETE FROM index_issues;
        DELETE FROM index_runs;
        DELETE FROM source_index_state;
      `);
      const revision = this.bumpRevision();
      this.db.exec("COMMIT");
      this.db.exec("PRAGMA wal_checkpoint(TRUNCATE)");
      this.db.exec("VACUUM");
      this.db.exec("PRAGMA wal_checkpoint(TRUNCATE)");
      return revision;
    } catch (error) {
      if (this.db.isTransaction) this.db.exec("ROLLBACK");
      throw error;
    }
  }

  purgeSource(sourceId: string, heartbeat?: () => void): PurgeSourcesResult {
    return this.purgeSources([sourceId], heartbeat);
  }

  softDeleteSources(sourceIds: readonly string[], heartbeat?: () => void): number {
    const uniqueSourceIds = [...new Set(sourceIds.map((sourceId) => sourceId.trim()).filter(Boolean))];
    if (uniqueSourceIds.length === 0) return 0;
    const placeholders = uniqueSourceIds.map(() => "?").join(",");
    this.db.exec("BEGIN IMMEDIATE");
    try {
      heartbeat?.();
      const changed = Number(this.db.prepare(`
        UPDATE documents SET deleted = 1, stale = 1
        WHERE source_id IN (${placeholders}) AND (deleted = 0 OR stale = 0)
      `).run(...uniqueSourceIds).changes);
      if (changed > 0) this.bumpRevision();
      heartbeat?.();
      this.db.exec("COMMIT");
      return changed;
    } catch (error) {
      this.db.exec("ROLLBACK");
      throw error;
    }
  }

  markSourceReconciliationPending(scopes: readonly SourceIdentityScope[], heartbeat?: () => void): void {
    if (scopes.length === 0) return;
    this.db.exec("BEGIN IMMEDIATE");
    try {
      const now = new Date().toISOString();
      const upsert = this.db.prepare(`
        INSERT INTO source_index_state(source_id, source_identity, ready, last_run_id, updated_at)
        VALUES (?, ?, 0, NULL, ?)
        ON CONFLICT(source_id) DO UPDATE SET
          source_identity=excluded.source_identity,
          ready=0,
          last_run_id=NULL,
          updated_at=excluded.updated_at
      `);
      for (const [index, scope] of scopes.entries()) {
        if (index % 256 === 0) heartbeat?.();
        upsert.run(scope.sourceId, scope.sourceIdentity, now);
      }
      this.db.exec("COMMIT");
    } catch (error) {
      this.db.exec("ROLLBACK");
      throw error;
    }
  }

  initializeSourceStateBaseline(sources: readonly KnowledgeSourceConfig[]): void {
    if (this.db.prepare("SELECT value FROM index_meta WHERE key = 'source_state_initialized'").get()) return;
    this.db.exec("BEGIN IMMEDIATE");
    try {
      if (!this.db.prepare("SELECT value FROM index_meta WHERE key = 'source_state_initialized'").get()) {
        const now = new Date().toISOString();
        const insert = this.db.prepare(`
          INSERT OR IGNORE INTO source_index_state(source_id, source_identity, ready, last_run_id, updated_at)
          VALUES (?, ?, 1, NULL, ?)
        `);
        for (const source of sources) insert.run(source.id, sourceIndexIdentity(source), now);
        this.db.prepare("INSERT INTO index_meta(key, value) VALUES ('source_state_initialized', '1')").run();
      }
      this.db.exec("COMMIT");
    } catch (error) {
      this.db.exec("ROLLBACK");
      throw error;
    }
  }

  markSourceScansReady(
    scopes: readonly SourceIdentityScope[],
    sourceIds: readonly string[],
    runId: string,
    heartbeat?: () => void,
  ): void {
    const readyIds = new Set(sourceIds);
    const selected = scopes.filter((scope) => readyIds.has(scope.sourceId));
    if (selected.length === 0) return;
    this.db.exec("BEGIN IMMEDIATE");
    try {
      const now = new Date().toISOString();
      const upsert = this.db.prepare(`
        INSERT INTO source_index_state(source_id, source_identity, ready, last_run_id, updated_at)
        VALUES (?, ?, 1, ?, ?)
        ON CONFLICT(source_id) DO UPDATE SET
          source_identity=excluded.source_identity,
          ready=1,
          last_run_id=excluded.last_run_id,
          updated_at=excluded.updated_at
      `);
      for (const [index, scope] of selected.entries()) {
        if (index % 256 === 0) heartbeat?.();
        upsert.run(scope.sourceId, scope.sourceIdentity, runId, now);
      }
      this.db.exec("COMMIT");
    } catch (error) {
      this.db.exec("ROLLBACK");
      throw error;
    }
  }

  reconcileSourceConfiguration(
    sources: readonly KnowledgeSourceConfig[],
    heartbeat?: () => void,
  ): SourceConfigurationReconciliationResult {
    const configuredById = new Map(sources.map((source) => [source.id, source]));
    const persistedSourceIds = new Set<string>();
    for (const row of this.db.prepare("SELECT DISTINCT source_id FROM documents").all()) {
      persistedSourceIds.add(String(asRecord(row).source_id));
    }
    for (const row of this.db.prepare("SELECT DISTINCT source_id FROM index_issues WHERE source_id <> 'system'").all()) {
      persistedSourceIds.add(String(asRecord(row).source_id));
    }
    for (const row of this.db.prepare("SELECT source_id FROM source_index_state").all()) {
      persistedSourceIds.add(String(asRecord(row).source_id));
    }
    const removedSourceIds = [...persistedSourceIds].filter((sourceId) => !configuredById.has(sourceId));
    const purged = this.purgeSources(removedSourceIds, heartbeat);

    const rows = this.db.prepare(`
      SELECT id, source_id, source_identity, deleted, stale
      FROM documents
    `).all().map((row) => asRecord(row));
    const invalidRows: Array<{ id: string; sourceId: string; wasVisible: boolean }> = [];
    const invalidatedSourceIds = new Set<string>();
    const newlyInvalidatedSourceIds = new Set<string>();
    for (const row of rows) {
      const sourceId = String(row.source_id);
      const source = configuredById.get(sourceId);
      if (!source || !source.enabled) continue;
      if (String(row.source_identity) !== sourceIndexIdentity(source)) {
        const wasVisible = Number(row.deleted) === 0;
        invalidRows.push({ id: String(row.id), sourceId, wasVisible });
        invalidatedSourceIds.add(sourceId);
        if (wasVisible) newlyInvalidatedSourceIds.add(sourceId);
      }
    }

    if (invalidRows.length > 0) {
      this.db.exec("BEGIN IMMEDIATE");
      try {
        const invalidate = this.db.prepare(`
          UPDATE documents SET deleted = 1, stale = 1
          WHERE id = ? AND (deleted = 0 OR stale = 0)
        `);
        for (const [index, row] of invalidRows.entries()) {
          if (index % 512 === 0) heartbeat?.();
          invalidate.run(row.id);
        }
        if (invalidRows.some((row) => row.wasVisible)) {
          this.reconcileDuplicates();
          this.bumpRevision();
        }
        heartbeat?.();
        this.db.exec("COMMIT");
      } catch (error) {
        this.db.exec("ROLLBACK");
        throw error;
      }
    }

    const recoverySourceIds = new Set(newlyInvalidatedSourceIds);
    const statesById = new Map<string, Record<string, SqliteValue>>();
    for (const row of this.db.prepare("SELECT source_id, source_identity, ready FROM source_index_state").all()) {
      const state = asRecord(row);
      statesById.set(String(state.source_id), state);
    }
    for (const source of sources) {
      if (!source.enabled) continue;
      const state = statesById.get(source.id);
      if (!state || String(state.source_identity) !== sourceIndexIdentity(source) || Number(state.ready) === 0) {
        recoverySourceIds.add(source.id);
      }
    }
    return {
      purged,
      invalidatedSourceIds: [...invalidatedSourceIds],
      recoverySourceIds: sources.filter((source) => source.enabled && recoverySourceIds.has(source.id)).map((source) => source.id),
    };
  }

  purgeSources(sourceIds: readonly string[], heartbeat?: () => void): PurgeSourcesResult {
    const uniqueSourceIds = [...new Set(sourceIds.map((sourceId) => sourceId.trim()).filter(Boolean))];
    if (uniqueSourceIds.length === 0) {
      return {
        sourceIds: [],
        documents: 0,
        chunks: 0,
        embeddings: 0,
        issues: 0,
        indexRevision: this.getRevision(),
      };
    }
    const placeholders = uniqueSourceIds.map(() => "?").join(",");
    this.db.exec("BEGIN IMMEDIATE");
    try {
      heartbeat?.();
      const documents = Number(asRecord(this.db.prepare(`
        SELECT COUNT(*) AS count FROM documents WHERE source_id IN (${placeholders})
      `).get(...uniqueSourceIds)).count);
      const chunks = Number(asRecord(this.db.prepare(`
        SELECT COUNT(*) AS count
        FROM chunks c JOIN documents d ON d.id = c.document_id
        WHERE d.source_id IN (${placeholders})
      `).get(...uniqueSourceIds)).count);
      const embeddings = Number(asRecord(this.db.prepare(`
        SELECT COUNT(*) AS count
        FROM document_embeddings e JOIN documents d ON d.id = e.document_id
        WHERE d.source_id IN (${placeholders})
      `).get(...uniqueSourceIds)).count);
      const issues = Number(asRecord(this.db.prepare(`
        SELECT COUNT(*) AS count FROM index_issues WHERE source_id IN (${placeholders})
      `).get(...uniqueSourceIds)).count);
      let lastRowid = 0;
      while (true) {
        heartbeat?.();
        const batch = this.db.prepare(`
          SELECT c.rowid
          FROM chunks c JOIN documents d ON d.id = c.document_id
          WHERE d.source_id IN (${placeholders}) AND c.rowid > ?
          ORDER BY c.rowid LIMIT 512
        `).all(...uniqueSourceIds, lastRowid).map((row) => Number(asRecord(row).rowid));
        if (batch.length === 0) break;
        const batchPlaceholders = batch.map(() => "?").join(",");
        if (this.fts5Available) this.db.prepare(`DELETE FROM chunks_terms WHERE rowid IN (${batchPlaceholders})`).run(...batch);
        if (this.trigramAvailable) this.db.prepare(`DELETE FROM chunks_trigram WHERE rowid IN (${batchPlaceholders})`).run(...batch);
        lastRowid = batch[batch.length - 1] ?? lastRowid;
      }
      this.db.prepare(`DELETE FROM documents WHERE source_id IN (${placeholders})`).run(...uniqueSourceIds);
      this.db.prepare(`DELETE FROM index_issues WHERE source_id IN (${placeholders})`).run(...uniqueSourceIds);
      this.db.prepare(`DELETE FROM source_index_state WHERE source_id IN (${placeholders})`).run(...uniqueSourceIds);
      heartbeat?.();
      this.reconcileDuplicates();
      const indexRevision = documents > 0 ? this.bumpRevision() : this.getRevision();
      this.db.exec("COMMIT");
      return {
        sourceIds: uniqueSourceIds,
        documents,
        chunks,
        embeddings,
        issues,
        indexRevision,
      };
    } catch (error) {
      this.db.exec("ROLLBACK");
      throw error;
    }
  }

  getDocumentByPath(absolutePath: string): StoredDocumentRow | null {
    const row = this.db.prepare("SELECT * FROM documents WHERE absolute_path = ?").get(absolutePath);
    return row ? (asRecord(row) as unknown as StoredDocumentRow) : null;
  }

  replaceDocument(draft: DocumentDraft, scanGeneration: string, heartbeat?: () => void): number {
    const now = new Date().toISOString();
    this.db.exec("BEGIN IMMEDIATE");
    try {
      const existingChunks = this.db.prepare("SELECT rowid, id FROM chunks WHERE document_id = ?").all(draft.id);
      const deleteTerms = this.fts5Available ? this.db.prepare("DELETE FROM chunks_terms WHERE rowid = ?") : null;
      const deleteTrigram = this.trigramAvailable ? this.db.prepare("DELETE FROM chunks_trigram WHERE rowid = ?") : null;
      for (const [index, row] of existingChunks.entries()) {
        if (index % 512 === 0) heartbeat?.();
        const chunkRowid = Number(asRecord(row).rowid);
        deleteTerms?.run(chunkRowid);
        deleteTrigram?.run(chunkRowid);
      }
      this.db.prepare("DELETE FROM chunks WHERE document_id = ?").run(draft.id);

      this.db.prepare(`
        INSERT INTO documents(
          id, canonical_id, source_id, source_label, source_kind, source_identity, absolute_path,
          relative_path, extension, title, family_key, family_confidence,
          size_bytes, filesystem_mtime_ms, filesystem_modified_at,
          effective_updated_at_ms, effective_updated_at, date_source, content_hash,
          indexed_at, stale, deleted, extraction_error, warnings_json, needs_ocr,
          chunk_count, scan_generation
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, NULL, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
          canonical_id=excluded.canonical_id,
          source_id=excluded.source_id,
          source_label=excluded.source_label,
          source_kind=excluded.source_kind,
          source_identity=excluded.source_identity,
          absolute_path=excluded.absolute_path,
          relative_path=excluded.relative_path,
          extension=excluded.extension,
          title=excluded.title,
          family_key=excluded.family_key,
          family_confidence=excluded.family_confidence,
          size_bytes=excluded.size_bytes,
          filesystem_mtime_ms=excluded.filesystem_mtime_ms,
          filesystem_modified_at=excluded.filesystem_modified_at,
          effective_updated_at_ms=excluded.effective_updated_at_ms,
          effective_updated_at=excluded.effective_updated_at,
          date_source=excluded.date_source,
          content_hash=excluded.content_hash,
          indexed_at=excluded.indexed_at,
          stale=0,
          deleted=0,
          extraction_error=NULL,
          warnings_json=excluded.warnings_json,
          needs_ocr=excluded.needs_ocr,
          chunk_count=excluded.chunk_count,
          scan_generation=excluded.scan_generation
      `).run(
        draft.id,
        draft.id,
        draft.candidate.sourceId,
        draft.candidate.sourceLabel,
        draft.candidate.sourceKind,
        draft.candidate.sourceIdentity,
        draft.candidate.absolutePath,
        draft.candidate.relativePath,
        draft.candidate.extension,
        draft.title,
        draft.familyKey,
        draft.familyConfidence,
        draft.candidate.sizeBytes,
        draft.candidate.filesystemMtimeMs,
        new Date(draft.candidate.filesystemMtimeMs).toISOString(),
        draft.date.effectiveUpdatedAtMs,
        new Date(draft.date.effectiveUpdatedAtMs).toISOString(),
        draft.date.dateSource,
        draft.contentHash,
        now,
        JSON.stringify(draft.warnings),
        draft.needsOcr ? 1 : 0,
        draft.chunks.length,
        scanGeneration,
      );

      const insertChunk = this.db.prepare(`
        INSERT INTO chunks(
          id, document_id, ordinal, section_type, heading_path_json, locator,
          text, normalized_text, search_terms, content_hash
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      `);
      const insertTerms = this.fts5Available
        ? this.db.prepare(`INSERT INTO chunks_terms(rowid, title_terms, heading_terms, path_terms, body_terms) VALUES (?, ?, ?, ?, ?)`)
        : null;
      const insertTrigram = this.trigramAvailable
        ? this.db.prepare(`INSERT INTO chunks_trigram(rowid, title, heading, path) VALUES (?, ?, ?, ?)`)
        : null;
      const titleTerms = buildSearchTerms(draft.title);
      const pathTerms = buildSearchTerms(draft.candidate.relativePath);
      const headingTerms = new Map<string, string>();
      for (const [index, chunk] of draft.chunks.entries()) {
        if (index % 512 === 0) heartbeat?.();
        const chunkId = `chunk_${chunk.contentHash.slice(0, 16)}_${draft.id.slice(-8)}_${chunk.ordinal}`;
        const heading = chunk.headingPath.join(" / ");
        const inserted = insertChunk.run(
          chunkId,
          draft.id,
          chunk.ordinal,
          chunk.sectionType,
          JSON.stringify(chunk.headingPath),
          chunk.locator,
          chunk.text,
          "",
          "",
          chunk.contentHash,
        );
        const chunkRowid = Number(inserted.lastInsertRowid);
        let cachedHeadingTerms = headingTerms.get(heading);
        if (cachedHeadingTerms === undefined) {
          cachedHeadingTerms = buildSearchTerms(heading);
          headingTerms.set(heading, cachedHeadingTerms);
        }
        insertTerms?.run(
          chunkRowid,
          titleTerms,
          cachedHeadingTerms,
          pathTerms,
          buildSearchTerms(chunk.text),
        );
        insertTrigram?.run(
          chunkRowid,
          draft.title,
          heading,
          draft.candidate.relativePath,
        );
      }
      const revision = this.bumpRevision();
      this.db.exec("COMMIT");
      return revision;
    } catch (error) {
      this.db.exec("ROLLBACK");
      throw error;
    }
  }

  markDocumentUnchanged(documentId: string, scanGeneration: string, candidate: FileCandidate): void {
    this.db.prepare(`
      UPDATE documents
      SET scan_generation = ?, deleted = 0,
          source_id = ?, source_label = ?, source_kind = ?, source_identity = ?,
          relative_path = ?, extension = ?,
          size_bytes = ?, filesystem_mtime_ms = ?, filesystem_modified_at = ?
      WHERE id = ?
    `).run(
      scanGeneration,
      candidate.sourceId,
      candidate.sourceLabel,
      candidate.sourceKind,
      candidate.sourceIdentity,
      candidate.relativePath,
      candidate.extension,
      candidate.sizeBytes,
      candidate.filesystemMtimeMs,
      new Date(candidate.filesystemMtimeMs).toISOString(),
      documentId,
    );
  }

  markDocumentFailed(documentId: string, scanGeneration: string, error: string): void {
    this.db.prepare(`
      UPDATE documents
      SET stale = 1, extraction_error = ?, scan_generation = ?, deleted = 0
      WHERE id = ?
    `).run(error, scanGeneration, documentId);
  }

  markMissingDeleted(scanGeneration: string, sourceIds: string[], heartbeat?: () => void): number {
    if (sourceIds.length === 0) return 0;
    const placeholders = sourceIds.map(() => "?").join(",");
    const rows = this.db.prepare(`
      SELECT id FROM documents
      WHERE source_id IN (${placeholders}) AND scan_generation <> ? AND deleted = 0
    `).all(...sourceIds, scanGeneration);
    for (const [index, row] of rows.entries()) {
      if (index % 512 === 0) heartbeat?.();
      const documentId = String(asRecord(row).id);
      this.db.prepare("UPDATE documents SET deleted = 1 WHERE id = ?").run(documentId);
    }
    if (rows.length > 0) this.bumpRevision();
    return rows.length;
  }

  reconcileDuplicates(): void {
    const groups = this.db.prepare(`
      SELECT content_hash
      FROM documents
      WHERE deleted = 0 AND content_hash <> ''
      GROUP BY content_hash HAVING COUNT(*) > 1
    `).all();
    const update = this.db.prepare("UPDATE documents SET canonical_id = ? WHERE content_hash = ? AND deleted = 0");
    for (const group of groups) {
      const hash = String(asRecord(group).content_hash);
      const canonical = this.db.prepare(`
        SELECT id FROM documents
        WHERE content_hash = ? AND deleted = 0
        ORDER BY effective_updated_at_ms DESC, relative_path ASC LIMIT 1
      `).get(hash);
      if (canonical) update.run(String(asRecord(canonical).id), hash);
    }
    this.db.prepare("UPDATE documents SET canonical_id = id WHERE deleted = 0 AND content_hash NOT IN (SELECT content_hash FROM documents WHERE deleted = 0 GROUP BY content_hash HAVING COUNT(*) > 1)").run();
  }

  lexicalCandidates(matchQuery: string, limit: number, filter: CandidateSourceFilter = {}): LexicalCandidateRow[] {
    if (!this.fts5Available || !matchQuery.trim()) return [];
    const sourceWhere = candidateSourceWhere(filter);
    const canonicalWhere = candidateCanonicalWhere(filter);
    const rows = this.db.prepare(`
      SELECT
        c.id AS chunk_id, c.document_id AS chunk_document_id, c.ordinal,
        c.section_type, c.heading_path_json, c.locator, c.text,
        c.text AS normalized_text, '' AS search_terms, c.content_hash AS chunk_content_hash,
        d.*,
        bm25(chunks_terms, 8.0, 6.0, 5.0, 1.0) AS lexical_rank
      FROM chunks_terms
      JOIN chunks c ON c.rowid = chunks_terms.rowid
      JOIN documents d ON d.id = c.document_id
      WHERE chunks_terms MATCH ? AND d.deleted = 0${sourceWhere.sql}${canonicalWhere.sql}
      ORDER BY lexical_rank ASC
      LIMIT ?
    `).all(matchQuery, ...sourceWhere.values, ...canonicalWhere.values, limit);
    return rows.map((row) => {
      const record = asRecord(row);
      if (record.chunk_content_hash !== undefined) record.content_hash = record.chunk_content_hash;
      return record as unknown as LexicalCandidateRow;
    });
  }

  trigramCandidates(matchQuery: string, limit: number, filter: CandidateSourceFilter = {}): LexicalCandidateRow[] {
    if (!this.trigramAvailable || !matchQuery.trim()) return [];
    const sourceWhere = candidateSourceWhere(filter);
    const canonicalWhere = candidateCanonicalWhere(filter);
    const rows = this.db.prepare(`
      SELECT
        c.id AS chunk_id, c.document_id AS chunk_document_id, c.ordinal,
        c.section_type, c.heading_path_json, c.locator, c.text,
        c.text AS normalized_text, '' AS search_terms, c.content_hash AS chunk_content_hash,
        d.*,
        bm25(chunks_trigram, 8.0, 6.0, 5.0) AS lexical_rank
      FROM chunks_trigram
      JOIN chunks c ON c.rowid = chunks_trigram.rowid
      JOIN documents d ON d.id = c.document_id
      WHERE chunks_trigram MATCH ? AND d.deleted = 0${sourceWhere.sql}${canonicalWhere.sql}
      ORDER BY lexical_rank ASC
      LIMIT ?
    `).all(matchQuery, ...sourceWhere.values, ...canonicalWhere.values, limit);
    return rows.map((row) => {
      const record = asRecord(row);
      if (record.chunk_content_hash !== undefined) record.content_hash = record.chunk_content_hash;
      return record as unknown as LexicalCandidateRow;
    });
  }

  documentExactCandidates(terms: string[], limit: number, filter: CandidateSourceFilter = {}): LexicalCandidateRow[] {
    const usable = [...new Set(terms.map((term) => term.trim().toLowerCase()).filter((term) => term.length >= 2))].slice(0, 12);
    if (usable.length === 0) return [];
    const sourceWhere = candidateSourceWhere(filter);
    const perTermLimit = Math.max(4, Math.ceil(limit / usable.length));
    const maximumRows = perTermLimit * usable.length;
    const termValues = usable.map((_, index) => `(?, ${index})`).join(",");
    const result = new Map<string, LexicalCandidateRow>();
    const selectDocuments = this.db.prepare(`
      WITH exact_terms(term, term_order) AS (
        VALUES ${termValues}
      ), matched AS (
        SELECT
          d.id,
          d.content_hash AS document_content_hash,
          d.canonical_id,
          exact_terms.term,
          exact_terms.term_order,
          d.effective_updated_at_ms,
          d.relative_path,
          CASE
            WHEN LOWER(d.title) = exact_terms.term THEN 0
            WHEN instr(LOWER(d.title), exact_terms.term) > 0 THEN 1
            WHEN instr(LOWER(d.relative_path), exact_terms.term) > 0 THEN 2
            ELSE 3
          END AS match_rank
        FROM documents d CROSS JOIN exact_terms
        WHERE d.deleted = 0${sourceWhere.sql}
          AND (
            instr(LOWER(d.title), exact_terms.term) > 0
            OR instr(LOWER(d.relative_path), exact_terms.term) > 0
            OR EXISTS (
              SELECT 1 FROM chunks exact_heading
              WHERE exact_heading.document_id = d.id
                AND instr(LOWER(exact_heading.heading_path_json), exact_terms.term) > 0
            )
          )
      ), deduplicated AS (
        SELECT *, ROW_NUMBER() OVER (
          PARTITION BY term_order, CASE WHEN document_content_hash = '' THEN id ELSE document_content_hash END
          ORDER BY match_rank, effective_updated_at_ms DESC, relative_path ASC, id ASC
        ) AS content_rank
        FROM matched
      ), ranked AS (
        SELECT *, ROW_NUMBER() OVER (
          PARTITION BY term_order
          ORDER BY match_rank, effective_updated_at_ms DESC, relative_path ASC, id ASC
        ) AS term_rank
        FROM deduplicated
        WHERE content_rank = 1
      )
      SELECT id, document_content_hash, canonical_id, term, term_order, match_rank,
             effective_updated_at_ms, relative_path
      FROM ranked
      WHERE term_rank <= ?
      ORDER BY term_order, match_rank, effective_updated_at_ms DESC, relative_path ASC, id ASC
      LIMIT ?
    `);
    const selectChunk = this.db.prepare(`
      SELECT
        c.id AS chunk_id, c.document_id AS chunk_document_id, c.ordinal,
        c.section_type, c.heading_path_json, c.locator, c.text,
        c.text AS normalized_text, '' AS search_terms, c.content_hash AS chunk_content_hash,
        d.*, 0.0 AS lexical_rank
      FROM chunks c JOIN documents d ON d.id = c.document_id
      WHERE d.id = ? AND d.deleted = 0
      ORDER BY CASE
        WHEN instr(LOWER(c.heading_path_json), ?) > 0 THEN 0
        WHEN instr(LOWER(c.text), ?) > 0 THEN 1
        ELSE 2
      END, c.ordinal ASC
      LIMIT 1
    `);
    const documents = selectDocuments.all(
      ...usable,
      ...sourceWhere.values,
      perTermLimit,
      maximumRows,
    );
    for (const document of documents) {
      const documentRecord = asRecord(document);
      const term = String(documentRecord.term);
      const dedupKey = String(documentRecord.document_content_hash || documentRecord.id);
      const existing = result.get(dedupKey);
      if (existing) {
        existing.exact_anchors = [...new Set([...(existing.exact_anchors ?? []), term])];
        continue;
      }
      const row = selectChunk.get(String(documentRecord.id), term, term);
      if (!row) continue;
      const record = asRecord(row);
      if (record.chunk_content_hash !== undefined) record.content_hash = record.chunk_content_hash;
      const candidate = record as unknown as LexicalCandidateRow;
      candidate.exact_anchors = [term];
      result.set(dedupKey, candidate);
    }
    return [...result.values()].slice(0, limit);
  }

  documentCandidates(
    documentIds: readonly string[],
    terms: readonly string[],
    perDocumentLimit: number,
    filter: CandidateSourceFilter = {},
  ): LexicalCandidateRow[] {
    const ids = [...new Set(documentIds.map((value) => value.trim()).filter(Boolean))].slice(0, 50);
    if (ids.length === 0) return [];
    const usableTerms = [...new Set(terms.map((term) => term.trim().toLowerCase()).filter((term) => term.length >= 2))].slice(0, 24);
    const sourceWhere = candidateSourceWhere(filter);
    const scoreExpression = usableTerms.length > 0
      ? usableTerms.map(() => `(
          CASE WHEN instr(LOWER(c.heading_path_json), ?) > 0 THEN 8 ELSE 0 END
          + CASE WHEN instr(LOWER(c.text), ?) > 0 THEN 4 ELSE 0 END
          + CASE WHEN instr(LOWER(d.title), ?) > 0 THEN 10 ELSE 0 END
          + CASE WHEN instr(LOWER(d.relative_path), ?) > 0 THEN 6 ELSE 0 END
        )`).join(" + ")
      : "0";
    const scoreValues = usableTerms.flatMap((term) => [term, term, term, term]);
    const limit = Math.min(64, Math.max(1, Math.trunc(perDocumentLimit)));
    const select = this.db.prepare(`
      SELECT
        c.id AS chunk_id, c.document_id AS chunk_document_id, c.ordinal,
        c.section_type, c.heading_path_json, c.locator, c.text,
        c.text AS normalized_text, '' AS search_terms, c.content_hash AS chunk_content_hash,
        d.*, 0.0 AS lexical_rank
      FROM chunks c JOIN documents d ON d.id = c.document_id
      WHERE d.id = ? AND d.deleted = 0${sourceWhere.sql}
      ORDER BY (${scoreExpression}) DESC, c.ordinal ASC
      LIMIT ?
    `);
    const result: LexicalCandidateRow[] = [];
    for (const documentId of ids) {
      const rows = select.all(documentId, ...sourceWhere.values, ...scoreValues, limit);
      for (const row of rows) {
        const record = asRecord(row);
        if (record.chunk_content_hash !== undefined) record.content_hash = record.chunk_content_hash;
        result.push(record as unknown as LexicalCandidateRow);
      }
    }
    return result;
  }

  likeCandidates(terms: string[], limit: number, filter: CandidateSourceFilter = {}): LexicalCandidateRow[] {
    const usable = terms.filter((term) => term.length >= 2).slice(0, 12);
    if (usable.length === 0) return [];
    const sourceWhere = candidateSourceWhere(filter);
    const canonicalWhere = candidateCanonicalWhere(filter);
    const predicates = usable.flatMap(() => ["LOWER(c.text) LIKE ?", "LOWER(d.title) LIKE ?", "LOWER(d.relative_path) LIKE ?"]);
    const args = usable.flatMap((term) => [`%${term}%`, `%${term}%`, `%${term}%`]);
    const rows = this.db.prepare(`
      SELECT
        c.id AS chunk_id, c.document_id AS chunk_document_id, c.ordinal,
        c.section_type, c.heading_path_json, c.locator, c.text,
        c.text AS normalized_text, '' AS search_terms, c.content_hash AS chunk_content_hash,
        d.*, 10.0 AS lexical_rank
      FROM chunks c JOIN documents d ON d.id = c.document_id
      WHERE d.deleted = 0${sourceWhere.sql}${canonicalWhere.sql} AND (${predicates.join(" OR ")})
      ORDER BY d.effective_updated_at_ms DESC LIMIT ?
    `).all(...sourceWhere.values, ...canonicalWhere.values, ...args, limit);
    return rows.map((row) => {
      const record = asRecord(row);
      if (record.chunk_content_hash !== undefined) record.content_hash = record.chunk_content_hash;
      return record as unknown as LexicalCandidateRow;
    });
  }

  getChunk(chunkId: string): LexicalCandidateRow | null {
    const row = this.db.prepare(`
      SELECT
        c.id AS chunk_id, c.document_id AS chunk_document_id, c.ordinal,
        c.section_type, c.heading_path_json, c.locator, c.text,
        c.text AS normalized_text, '' AS search_terms, c.content_hash AS chunk_content_hash,
        d.*
      FROM chunks c JOIN documents d ON d.id = c.document_id
      WHERE c.id = ? AND d.deleted = 0
    `).get(chunkId);
    if (!row) return null;
    const record = asRecord(row);
    if (record.chunk_content_hash !== undefined) record.content_hash = record.chunk_content_hash;
    return { ...(record as unknown as LexicalCandidateRow), lexical_rank: 0 };
  }

  getDocument(documentId: string): StoredDocumentRow | null {
    const row = this.db.prepare("SELECT * FROM documents WHERE id = ? AND deleted = 0").get(documentId);
    return row ? (asRecord(row) as unknown as StoredDocumentRow) : null;
  }

  getDocumentChunks(documentId: string, limit = 200): StoredChunkRow[] {
    return this.db.prepare("SELECT * FROM chunks WHERE document_id = ? ORDER BY ordinal LIMIT ?")
      .all(documentId, limit)
      .map((row) => asRecord(row) as unknown as StoredChunkRow);
  }

  getVersions(
    familyKey: string,
    limit = 50,
    sourceIds?: readonly string[],
    sourceScopes?: readonly SourceIdentityScope[],
  ): StoredDocumentRow[] {
    const uniqueSourceIds = sourceIds === undefined
      ? undefined
      : [...new Set(sourceIds.map((sourceId) => sourceId.trim()).filter(Boolean))];
    if (uniqueSourceIds?.length === 0) return [];
    const scopes = sourceScopes ?? [];
    const sourceIdWhere = uniqueSourceIds
      ? `source_id IN (${uniqueSourceIds.map(() => "?").join(",")})`
      : "";
    const scopeWhere = scopes.length > 0
      ? `(${scopes.map(() => "(source_id = ? AND source_identity = ?)").join(" OR ")})`
      : "";
    const sourceWhere = [sourceIdWhere, scopeWhere].filter(Boolean).length > 0
      ? ` AND ${[sourceIdWhere, scopeWhere].filter(Boolean).join(" AND ")}`
      : "";
    const scopeValues = scopes.flatMap((scope) => [scope.sourceId, scope.sourceIdentity]);
    return this.db.prepare(`
      SELECT * FROM documents WHERE family_key = ? AND deleted = 0${sourceWhere}
      ORDER BY effective_updated_at_ms DESC, relative_path ASC LIMIT ?
    `).all(familyKey, ...(uniqueSourceIds ?? []), ...scopeValues, limit).map((row) => asRecord(row) as unknown as StoredDocumentRow);
  }

  putDocumentEmbedding(documentId: string, providerId: string, contentHash: string, vector: number[]): void {
    this.db.prepare(`
      INSERT INTO document_embeddings(document_id, provider_id, content_hash, vector_json, dimensions, indexed_at)
      VALUES (?, ?, ?, ?, ?, ?)
      ON CONFLICT(document_id) DO UPDATE SET
        provider_id=excluded.provider_id,
        content_hash=excluded.content_hash,
        vector_json=excluded.vector_json,
        dimensions=excluded.dimensions,
        indexed_at=excluded.indexed_at
    `).run(documentId, providerId, contentHash, JSON.stringify(vector), vector.length, new Date().toISOString());
  }

  getDocumentEmbeddings(providerId: string): Array<{ documentId: string; contentHash: string; vector: number[] }> {
    return this.db.prepare(`
      SELECT e.document_id, e.content_hash, e.vector_json
      FROM document_embeddings e JOIN documents d ON d.id = e.document_id
      WHERE e.provider_id = ? AND d.deleted = 0 AND d.id = d.canonical_id
    `).all(providerId).flatMap((row) => {
      const record = asRecord(row);
      try {
        return [{
          documentId: String(record.document_id),
          contentHash: String(record.content_hash),
          vector: JSON.parse(String(record.vector_json)) as number[],
        }];
      } catch {
        return [];
      }
    });
  }

  status(configPath: string): IndexStatus {
    const documentCount = Number(asRecord(this.db.prepare("SELECT COUNT(*) AS count FROM documents WHERE deleted = 0 AND id = canonical_id").get()).count);
    const chunkCount = Number(asRecord(this.db.prepare("SELECT COUNT(*) AS count FROM chunks c JOIN documents d ON d.id = c.document_id WHERE d.deleted = 0 AND d.id = d.canonical_id").get()).count);
    const staleCount = Number(asRecord(this.db.prepare("SELECT COUNT(*) AS count FROM documents WHERE deleted = 0 AND stale = 1").get()).count);
    const sourceCounts: Record<string, number> = {};
    for (const row of this.db.prepare("SELECT source_id, COUNT(*) AS count FROM documents WHERE deleted = 0 AND id = canonical_id GROUP BY source_id").all()) {
      const record = asRecord(row);
      sourceCounts[String(record.source_id)] = Number(record.count);
    }
    const latestRows = this.db.prepare("SELECT * FROM index_runs ORDER BY started_at DESC LIMIT 20").all().map(asRecord);
    const newestRun = latestRows[0];
    const activeRun = newestRun && !["complete", "failed"].includes(String(newestRun.phase)) ? parseRun(newestRun) : null;
    const lastRun = parseRun(latestRows.find((row) => ["complete", "failed"].includes(String(row.phase))));
    const issueRunId = activeRun?.runId ?? lastRun?.runId ?? null;
    const issueRows = issueRunId === null
      ? this.db.prepare(`
          SELECT source_id, path, code, message, occurred_at
          FROM index_issues WHERE run_id IS NULL ORDER BY occurred_at DESC LIMIT 20
        `).all()
      : this.db.prepare(`
      SELECT source_id, path, code, message, occurred_at
      FROM index_issues WHERE run_id = ? OR run_id IS NULL ORDER BY occurred_at DESC LIMIT 20
    `).all(issueRunId);
    const recentIssues: IndexIssue[] = issueRows.map((row) => {
        const record = asRecord(row);
        return {
          sourceId: String(record.source_id),
          path: String(record.path),
          code: String(record.code),
          message: String(record.message),
          occurredAt: String(record.occurred_at),
        };
      });
    return {
      databasePath: this.databasePath,
      configPath,
      indexRevision: this.getRevision(),
      fts5Available: this.fts5Available,
      trigramAvailable: this.trigramAvailable,
      documentCount,
      chunkCount,
      staleCount,
      sourceCounts,
      activeRun,
      lastRun,
      recentIssues,
    };
  }

  countSections(documentId: string): SectionType[] {
    return this.db.prepare("SELECT DISTINCT section_type FROM chunks WHERE document_id = ? ORDER BY section_type")
      .all(documentId)
      .map((row) => String(asRecord(row).section_type) as SectionType);
  }

  setIndexBackendRun(value: { runId?: string; [key: string]: unknown }): void {
    const serialized = JSON.stringify(value);
    this.db.prepare(`
      INSERT INTO index_meta(key, value) VALUES ('last_backend_run', ?)
      ON CONFLICT(key) DO UPDATE SET value=excluded.value
    `).run(serialized);
    if (value.runId) {
      this.db.prepare(`
        INSERT INTO index_meta(key, value) VALUES (?, ?)
        ON CONFLICT(key) DO UPDATE SET value=excluded.value
      `).run(`backend_run:${value.runId}`, serialized);
    }
  }

  getIndexBackendRun<T>(): T | null {
    const row = this.db.prepare("SELECT value FROM index_meta WHERE key = 'last_backend_run'").get();
    if (!row) return null;
    try {
      return JSON.parse(String(asRecord(row).value)) as T;
    } catch {
      return null;
    }
  }
}
