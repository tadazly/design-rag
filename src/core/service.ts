import { randomUUID } from "node:crypto";
import path from "node:path";
import { open, readFile, rm, stat } from "node:fs/promises";
import writeFileAtomic from "write-file-atomic";
import type {
  AppConfig,
  IndexRunSummary,
  IndexBackendMetrics,
  IndexBackendStatus,
  IndexStatus,
  KnowledgeSourceConfig,
  RetrievalBundle,
  RetrievalRequest,
  SearchRequest,
  SearchResponse,
} from "../shared/contracts.js";
import {
  ConfigStore,
  type ConfigFileFingerprint,
  type ConfigSnapshot,
} from "./config.js";
import { IndexDatabase, type PurgeSourcesResult } from "./database.js";
import {
  inspectSources,
  KnowledgeIndexer,
  type IndexOptions,
  type InspectSourcesOptions,
  type SourceSnapshot,
} from "./indexer.js";
import { canonicalPathKey, sourceIndexIdentity } from "./paths.js";
import { SearchEngine } from "./search.js";

export interface SourceReconciliationPlan {
  addedSourceIds: string[];
  removedSourceIds: string[];
  replacedSourceIds: string[];
  modifiedSourceIds: string[];
  enabledSourceIds: string[];
  disabledSourceIds: string[];
  purgeSourceIds: string[];
  incrementalSourceIds: string[];
}

export interface ReconcileSourcesOptions {
  runIncrementalIndex?: boolean;
  signal?: AbortSignal;
  onProgress?: (summary: IndexRunSummary) => void;
}

export interface SourceReconciliationResult {
  config: AppConfig;
  plan: SourceReconciliationPlan;
  purged: PurgeSourcesResult;
  indexRun: IndexRunSummary | null;
}

export interface SourceChangeDetection {
  snapshots: SourceSnapshot[];
  changedSourceIds: string[];
  removedSourceIds: string[];
  unavailableSourceIds: string[];
}

export interface ConfigReloadResult {
  changed: boolean;
  deferred: boolean;
  config: AppConfig;
  fingerprint: ConfigFileFingerprint;
}

export class MutationLeaseBusyError extends Error {
  readonly code = "MUTATION_LEASE_BUSY";

  constructor(readonly requestedOperation: string, readonly activeOperation: string | null) {
    super(activeOperation
      ? `另一个进程正在执行 ${activeOperation}，当前操作 ${requestedOperation} 暂不能开始`
      : `另一个进程正在更新配置或索引，当前操作 ${requestedOperation} 暂不能开始`);
    this.name = "MutationLeaseBusyError";
  }
}

const INDEX_POINTER_FILE = "index.active.json";
const INDEX_RECOVERY_LOCK_FILE = "index.recovery.lock";
const INDEX_RECOVERY_LOCK_STALE_MS = 30_000;
const INDEX_RECOVERY_WAIT_MS = 10_000;

interface ActiveIndexPointer {
  schemaVersion: 1;
  fileName: string;
  activatedAt: string;
  reason: "corruption-recovery";
}

function isSafeIndexFileName(fileName: string): boolean {
  return path.basename(fileName) === fileName
    && /^index(?:\.[a-z0-9-]+)?\.sqlite$/i.test(fileName);
}

async function resolveActiveDatabasePath(dataDir: string): Promise<string> {
  const pointerPath = path.join(dataDir, INDEX_POINTER_FILE);
  try {
    const parsed = JSON.parse(await readFile(pointerPath, "utf8")) as Partial<ActiveIndexPointer>;
    if (parsed.schemaVersion !== 1 || typeof parsed.fileName !== "string" || !isSafeIndexFileName(parsed.fileName)) {
      throw new Error(`索引指针无效：${pointerPath}`);
    }
    return path.join(dataDir, parsed.fileName);
  } catch (error) {
    const missing = error instanceof Error && "code" in error && error.code === "ENOENT";
    if (!missing) throw error;
    return path.join(dataDir, "index.sqlite");
  }
}

async function wait(milliseconds: number): Promise<void> {
  await new Promise<void>((resolve) => setTimeout(resolve, milliseconds));
}

async function acquireIndexRecoveryLock(dataDir: string, failedDatabasePath: string): Promise<
  { acquired: true; lockPath: string } | { acquired: false; databasePath: string }
> {
  const lockPath = path.join(dataDir, INDEX_RECOVERY_LOCK_FILE);
  const startedAt = Date.now();
  while (Date.now() - startedAt < INDEX_RECOVERY_WAIT_MS) {
    const currentDatabasePath = await resolveActiveDatabasePath(dataDir);
    if (currentDatabasePath !== failedDatabasePath) {
      return { acquired: false, databasePath: currentDatabasePath };
    }
    try {
      const handle = await open(lockPath, "wx", 0o600);
      try {
        await handle.writeFile(JSON.stringify({ pid: process.pid, createdAt: new Date().toISOString() }));
      } finally {
        await handle.close();
      }
      return { acquired: true, lockPath };
    } catch (error) {
      const exists = error instanceof Error && "code" in error && error.code === "EEXIST";
      if (!exists) throw error;
      try {
        const lockStat = await stat(lockPath);
        if (Date.now() - lockStat.mtimeMs > INDEX_RECOVERY_LOCK_STALE_MS) {
          await rm(lockPath, { force: true });
          continue;
        }
      } catch (statError) {
        const missing = statError instanceof Error && "code" in statError && statError.code === "ENOENT";
        if (!missing) throw statError;
      }
      await wait(100);
    }
  }
  throw new Error("另一个进程正在恢复损坏的本地索引，请稍后重试");
}

async function recoverCorruptDatabase(
  dataDir: string,
  failedDatabasePath: string,
): Promise<{ database: IndexDatabase; failedDatabasePath: string }> {
  const lock = await acquireIndexRecoveryLock(dataDir, failedDatabasePath);
  if (!lock.acquired) {
    return { database: new IndexDatabase(lock.databasePath), failedDatabasePath };
  }
  let recoveredDatabase: IndexDatabase | null = null;
  try {
    const latestDatabasePath = await resolveActiveDatabasePath(dataDir);
    if (latestDatabasePath !== failedDatabasePath) {
      return { database: new IndexDatabase(latestDatabasePath), failedDatabasePath };
    }
    const fileName = `index.recovered-${Date.now()}-${randomUUID()}.sqlite`;
    recoveredDatabase = new IndexDatabase(path.join(dataDir, fileName));
    const pointer: ActiveIndexPointer = {
      schemaVersion: 1,
      fileName,
      activatedAt: new Date().toISOString(),
      reason: "corruption-recovery",
    };
    await writeFileAtomic(path.join(dataDir, INDEX_POINTER_FILE), `${JSON.stringify(pointer, null, 2)}\n`, {
      encoding: "utf8",
      mode: 0o600,
    });
    return { database: recoveredDatabase, failedDatabasePath };
  } catch (error) {
    try { recoveredDatabase?.close(); } catch { /* preserve the recovery error */ }
    throw error;
  } finally {
    await rm(lock.lockPath, { force: true }).catch(() => undefined);
  }
}

const MUTATION_LEASE_TTL_MS = 5 * 60_000;
const MUTATION_HEARTBEAT_MS = 10_000;

function normalizedStringSet(values: readonly string[]): string {
  return [...new Set(values.map((value) => value.trim().toLowerCase()).filter(Boolean))].sort().join("\0");
}

function isSourceReplacement(previous: KnowledgeSourceConfig, next: KnowledgeSourceConfig): boolean {
  return previous.kind !== next.kind || canonicalPathKey(previous.rootPath) !== canonicalPathKey(next.rootPath);
}

function sourceMetadataChanged(previous: KnowledgeSourceConfig, next: KnowledgeSourceConfig): boolean {
  return previous.label !== next.label
    || previous.maxFileBytes !== next.maxFileBytes
    || normalizedStringSet(previous.includeExtensions) !== normalizedStringSet(next.includeExtensions)
    || normalizedStringSet(previous.excludeDirectoryNames) !== normalizedStringSet(next.excludeDirectoryNames);
}

export function planSourceReconciliation(previous: AppConfig, next: AppConfig): SourceReconciliationPlan {
  const previousById = new Map(previous.sources.map((source) => [source.id, source]));
  const nextById = new Map(next.sources.map((source) => [source.id, source]));
  const addedSourceIds = next.sources.filter((source) => !previousById.has(source.id)).map((source) => source.id);
  const removedSourceIds = previous.sources.filter((source) => !nextById.has(source.id)).map((source) => source.id);
  const replacedSourceIds: string[] = [];
  const modifiedSourceIds: string[] = [];
  const enabledSourceIds: string[] = [];
  const disabledSourceIds: string[] = [];
  for (const nextSource of next.sources) {
    const previousSource = previousById.get(nextSource.id);
    if (!previousSource) continue;
    if (isSourceReplacement(previousSource, nextSource)) replacedSourceIds.push(nextSource.id);
    else if (sourceMetadataChanged(previousSource, nextSource)) modifiedSourceIds.push(nextSource.id);
    if (!previousSource.enabled && nextSource.enabled) enabledSourceIds.push(nextSource.id);
    if (previousSource.enabled && !nextSource.enabled) disabledSourceIds.push(nextSource.id);
  }
  const purgeSourceIds = [...removedSourceIds];
  const incrementalCandidates = [...new Set([
    ...addedSourceIds,
    ...replacedSourceIds,
    ...modifiedSourceIds,
    ...enabledSourceIds,
  ])];
  const incrementalSourceIds = incrementalCandidates.filter((sourceId) => nextById.get(sourceId)?.enabled);
  return {
    addedSourceIds,
    removedSourceIds,
    replacedSourceIds,
    modifiedSourceIds,
    enabledSourceIds,
    disabledSourceIds,
    purgeSourceIds,
    incrementalSourceIds,
  };
}

export class KnowledgeBaseService {
  private configValue: AppConfig;
  private configFingerprint: ConfigFileFingerprint;
  readonly configStore: ConfigStore;
  readonly database: IndexDatabase;
  readonly indexer: KnowledgeIndexer;
  readonly searchEngine: SearchEngine;

  private constructor(
    configStore: ConfigStore,
    config: AppConfig,
    configFingerprint: ConfigFileFingerprint,
    database: IndexDatabase,
  ) {
    this.configStore = configStore;
    this.configValue = config;
    this.configFingerprint = configFingerprint;
    this.database = database;
    this.indexer = new KnowledgeIndexer(database);
    this.searchEngine = new SearchEngine(database, () => this.configValue);
  }

  static async create(options: { configDir?: string; dataDir?: string; readOnly?: boolean } = {}): Promise<KnowledgeBaseService> {
    const configStore = new ConfigStore(options);
    const configSnapshot = await configStore.loadSnapshot();
    const databasePath = await resolveActiveDatabasePath(configStore.dataDir);
    let database: IndexDatabase | null = null;
    try {
      database = new IndexDatabase(databasePath, { readOnly: options.readOnly ?? false });
      database.status(configStore.configPath);
    } catch (error) {
      try { database?.close(); } catch { /* best effort before quarantine */ }
      if (options.readOnly || !isCorruptDatabaseError(error)) throw error;
      const recovered = await recoverCorruptDatabase(configStore.dataDir, databasePath);
      database = recovered.database;
      database.recordIssue({
        path: recovered.failedDatabasePath,
        sourceId: "system",
        code: "cache_corrupt_recovered",
        message: "检测到损坏的本地检索缓存，已切换到新的索引文件；旧缓存保持原样，请重新建立索引。",
        occurredAt: new Date().toISOString(),
      }, null);
    }
    if (!options.readOnly) {
      try {
        database.initializeSourceStateBaseline(configSnapshot.config.sources);
      } catch (error) {
        try { database.close(); } catch { /* preserve baseline error */ }
        throw error;
      }
    }
    return new KnowledgeBaseService(configStore, configSnapshot.config, configSnapshot.fingerprint, database);
  }

  get config(): AppConfig {
    return this.configValue;
  }

  async reloadConfigIfChanged(): Promise<ConfigReloadResult> {
    if (this.database.getActiveMutationLease()) {
      return {
        changed: false,
        deferred: true,
        config: this.configValue,
        fingerprint: this.configFingerprint,
      };
    }
    const fingerprint = await this.configStore.fingerprint();
    if (fingerprint.sha256 === this.configFingerprint.sha256) {
      this.configFingerprint = fingerprint;
      return { changed: false, deferred: false, config: this.configValue, fingerprint };
    }
    const snapshot = await this.configStore.loadSnapshot();
    if (this.database.getActiveMutationLease()) {
      return {
        changed: false,
        deferred: true,
        config: this.configValue,
        fingerprint: this.configFingerprint,
      };
    }
    const changed = snapshot.fingerprint.sha256 !== this.configFingerprint.sha256;
    if (changed) this.applyConfigSnapshot(snapshot);
    else this.configFingerprint = snapshot.fingerprint;
    return {
      changed,
      deferred: false,
      config: this.configValue,
      fingerprint: this.configFingerprint,
    };
  }

  async saveConfig(config: AppConfig): Promise<AppConfig> {
    return this.withMutationLease("save-config", async () => {
      const snapshot = await this.configStore.saveSnapshot(config);
      this.applyConfigSnapshot(snapshot);
      return this.configValue;
    });
  }

  validateConfig(config: AppConfig): Promise<AppConfig> {
    return this.configStore.validate(config);
  }

  async reconcileSources(config: AppConfig, options: ReconcileSourcesOptions = {}): Promise<SourceReconciliationResult> {
    const nextConfig = await this.configStore.validate(config);
    return this.withMutationLease("reconcile-config", async (leaseSignal, heartbeat) => {
      const currentSnapshot = await this.configStore.loadSnapshot();
      this.applyConfigSnapshot(currentSnapshot);
      const plan = planSourceReconciliation(this.configValue, nextConfig);
      const plannedIndex = options.runIncrementalIndex !== false && plan.incrementalSourceIds.length > 0;
      if (this.indexer.isRunning && (plan.purgeSourceIds.length > 0 || plannedIndex)) {
        throw new Error("索引任务运行期间不能添加、更新、重新启用或删除资料源，请等待当前任务结束");
      }
      const savedSnapshot = await this.configStore.saveSnapshot(nextConfig);
      let purged: PurgeSourcesResult;
      let indexRun: IndexRunSummary | null = null;
      try {
        const savedSourcesById = new Map(savedSnapshot.config.sources.map((source) => [source.id, source]));
        this.database.markSourceReconciliationPending(plan.incrementalSourceIds.flatMap((sourceId) => {
          const source = savedSourcesById.get(sourceId);
          return source ? [{ sourceId, sourceIdentity: sourceIndexIdentity(source) }] : [];
        }), heartbeat);
        const sourceReconciliation = this.database.reconcileSourceConfiguration(savedSnapshot.config.sources, heartbeat);
        purged = sourceReconciliation.purged;
        plan.incrementalSourceIds = [...new Set([
          ...plan.incrementalSourceIds,
          ...sourceReconciliation.recoverySourceIds,
        ])];
        const shouldIndex = options.runIncrementalIndex !== false && plan.incrementalSourceIds.length > 0;
        if (shouldIndex) {
          indexRun = await this.indexer.run(savedSnapshot.config, {
            sourceIds: plan.incrementalSourceIds,
            signal: leaseSignal,
            mutationHeartbeat: heartbeat,
            ...(options.onProgress ? { onProgress: options.onProgress } : {}),
          });
        }
      } finally {
        this.applyConfigSnapshot(savedSnapshot);
      }
      return { config: this.configValue, plan, purged, indexRun };
    }, options.signal);
  }

  reconcileConfig(config: AppConfig, options: ReconcileSourcesOptions = {}): Promise<SourceReconciliationResult> {
    return this.reconcileSources(config, options);
  }

  status(): IndexStatus {
    const status = this.database.status(this.configStore.configPath);
    const persisted = this.database.getIndexBackendRun<{
      hello?: { protocolVersion?: number; backendVersion?: string; platform?: string; arch?: string; capabilities?: string[] };
      metrics?: IndexBackendMetrics;
    }>();
    const hello = this.indexer.lastHello ?? persisted?.hello ?? null;
    const lastMetrics = this.indexer.lastMetrics ?? persisted?.metrics ?? null;
    const indexBackend: IndexBackendStatus = {
      engine: "go",
      binaryPath: this.indexer.binaryPath,
      running: this.indexer.isRunning,
      pid: this.indexer.pid,
      protocolVersion: hello?.protocolVersion ?? 2,
      backendVersion: hello?.backendVersion ?? null,
      platform: hello?.platform ?? null,
      arch: hello?.arch ?? null,
      capabilities: [...(hello?.capabilities ?? [])],
      lastMetrics,
    };
    return {
      ...status,
      activeRun: this.indexer.summary ?? status.activeRun,
      indexBackend,
    };
  }

  index(options: IndexOptions = {}): Promise<IndexRunSummary> {
    return this.withMutationLease("index", async (leaseSignal, heartbeat) => {
      this.applyConfigSnapshot(await this.configStore.loadSnapshot());
      const sourceReconciliation = this.database.reconcileSourceConfiguration(this.configValue.sources, heartbeat);
      const sourceIds = !options.full && options.sourceIds !== undefined
        ? [...new Set([...options.sourceIds, ...sourceReconciliation.recoverySourceIds])]
        : options.sourceIds;
      return this.indexer.run(this.configValue, {
        ...options,
        ...(sourceIds !== undefined ? { sourceIds } : {}),
        signal: leaseSignal,
        mutationHeartbeat: heartbeat,
      });
    }, options.signal);
  }

  inspectSources(options: InspectSourcesOptions = {}): Promise<SourceSnapshot[]> {
    return inspectSources(this.configValue, options);
  }

  async detectSourceChanges(
    previousSnapshots: readonly SourceSnapshot[],
    options: InspectSourcesOptions = {},
  ): Promise<SourceChangeDetection> {
    const snapshots = await this.inspectSources(options);
    const previousById = new Map(previousSnapshots.map((snapshot) => [snapshot.sourceId, snapshot]));
    const configuredIds = new Set(this.configValue.sources.map((source) => source.id));
    const changedSourceIds = snapshots
      .filter((snapshot) => {
        const previous = previousById.get(snapshot.sourceId);
        return !previous
          || previous.available !== snapshot.available
          || previous.fingerprint !== snapshot.fingerprint;
      })
      .map((snapshot) => snapshot.sourceId);
    return {
      snapshots,
      changedSourceIds,
      removedSourceIds: previousSnapshots.filter((snapshot) => !configuredIds.has(snapshot.sourceId)).map((snapshot) => snapshot.sourceId),
      unavailableSourceIds: snapshots.filter((snapshot) => !snapshot.available).map((snapshot) => snapshot.sourceId),
    };
  }

  pauseIndex(): IndexRunSummary | null {
    return this.indexer.pause();
  }

  resumeIndex(): IndexRunSummary | null {
    return this.indexer.resume();
  }

  clearIndexCache(): IndexStatus {
    if (this.indexer.isRunning) throw new Error("索引任务运行期间不能删除本地检索缓存，请先等待任务结束");
    return this.withMutationLeaseSync("clear-index-cache", () => {
      this.database.clearLocalCache();
      return this.status();
    });
  }

  purgeSourceIndex(sourceId: string): PurgeSourcesResult {
    if (this.indexer.isRunning) throw new Error("索引任务运行期间不能删除资料源索引，请等待任务结束");
    return this.withMutationLeaseSync("purge-source-index", (heartbeat) => this.database.purgeSource(sourceId, heartbeat));
  }

  deleteSourceIndex(sourceId: string): PurgeSourcesResult {
    return this.purgeSourceIndex(sourceId);
  }

  search(request: SearchRequest): Promise<SearchResponse> {
    return this.searchEngine.search(request);
  }

  retrieve(request: RetrievalRequest): Promise<RetrievalBundle> {
    return this.searchEngine.retrieve(request);
  }

  readCitation(citationId: string, expectedIndexRevision?: number) {
    return this.searchEngine.readCitation(citationId, expectedIndexRevision);
  }

  listVersions(input: { documentId?: string; familyKey?: string; limit?: number }) {
    const enabledSources = this.configValue.sources.filter((source) => source.enabled);
    const enabledSourceIds = enabledSources.map((source) => source.id);
    const enabledSourceScopes = enabledSources.map((source) => ({
      sourceId: source.id,
      sourceIdentity: sourceIndexIdentity(source),
    }));
    const matchesCurrentSource = (item: { source_id: string; source_identity: string }) => {
      const source = enabledSources.find((candidate) => candidate.id === item.source_id);
      return Boolean(source && item.source_identity === sourceIndexIdentity(source));
    };
    const document = input.documentId ? this.database.getDocument(input.documentId) : null;
    if (input.documentId && (!document || !matchesCurrentSource(document))) {
      throw new Error("文档不存在，或所属资料源已禁用、删除或变更");
    }
    const familyKey = input.familyKey ?? document?.family_key;
    if (!familyKey) throw new Error("documentId 或 familyKey 至少提供一个");
    return this.database.getVersions(
      familyKey,
      Math.min(100, Math.max(1, input.limit ?? 30)),
      enabledSourceIds,
      enabledSourceScopes,
    ).filter(matchesCurrentSource).map((item) => ({
      documentId: item.id,
      sourceId: item.source_id,
      sourceLabel: item.source_label,
      sourceKind: item.source_kind,
      title: item.title,
      effectiveUpdatedAt: item.effective_updated_at,
      dateSource: item.date_source,
      relativePath: item.relative_path,
      familyKey: item.family_key,
      familyConfidence: item.family_confidence,
      canonical: item.id === item.canonical_id,
      stale: Boolean(item.stale),
    }));
  }

  private applyConfigSnapshot(snapshot: ConfigSnapshot): void {
    this.configValue = snapshot.config;
    this.configFingerprint = snapshot.fingerprint;
  }

  private async withMutationLease<T>(
    operation: string,
    action: (signal: AbortSignal, heartbeat: () => void) => Promise<T>,
    callerSignal?: AbortSignal,
  ): Promise<T> {
    callerSignal?.throwIfAborted();
    const ownerId = `${process.pid}:${randomUUID()}`;
    let lease;
    try {
      lease = this.database.tryAcquireMutationLease(ownerId, operation, MUTATION_LEASE_TTL_MS);
    } catch (error) {
      if (!isSqliteBusyError(error)) throw error;
      throw new MutationLeaseBusyError(operation, this.database.getActiveMutationLease()?.operation ?? null);
    }
    if (!lease) {
      throw new MutationLeaseBusyError(operation, this.database.getActiveMutationLease()?.operation ?? null);
    }

    const leaseAbort = new AbortController();
    const signal = callerSignal ? AbortSignal.any([callerSignal, leaseAbort.signal]) : leaseAbort.signal;
    let lastHeartbeatAt = Date.now();
    const renew = () => {
      const now = Date.now();
      if (!this.database.renewMutationLease(ownerId, MUTATION_LEASE_TTL_MS, now)) {
        const error = new Error("跨进程 mutation lease 已失效，当前操作已取消");
        leaseAbort.abort(error);
        throw error;
      }
      lastHeartbeatAt = now;
    };
    const heartbeat = setInterval(() => {
      try {
        renew();
      } catch (error) {
        if (Date.now() - lastHeartbeatAt >= MUTATION_LEASE_TTL_MS) {
          leaseAbort.abort(error instanceof Error ? error : new Error(String(error)));
        }
      }
    }, MUTATION_HEARTBEAT_MS);
    heartbeat.unref();

    try {
      signal.throwIfAborted();
      const result = await action(signal, renew);
      signal.throwIfAborted();
      renew();
      return result;
    } finally {
      clearInterval(heartbeat);
      try { this.database.releaseMutationLease(ownerId); } catch { /* database close/loss must not mask the mutation result */ }
    }
  }

  private withMutationLeaseSync<T>(operation: string, action: (heartbeat: () => void) => T): T {
    const ownerId = `${process.pid}:${randomUUID()}`;
    let lease;
    try {
      lease = this.database.tryAcquireMutationLease(ownerId, operation, MUTATION_LEASE_TTL_MS);
    } catch (error) {
      if (!isSqliteBusyError(error)) throw error;
      throw new MutationLeaseBusyError(operation, this.database.getActiveMutationLease()?.operation ?? null);
    }
    if (!lease) {
      throw new MutationLeaseBusyError(operation, this.database.getActiveMutationLease()?.operation ?? null);
    }
    const renew = () => {
      if (!this.database.renewMutationLease(ownerId, MUTATION_LEASE_TTL_MS)) {
        throw new Error("跨进程 mutation lease 已失效，当前操作已取消");
      }
    };
    try {
      return action(renew);
    } finally {
      try { this.database.releaseMutationLease(ownerId); } catch { /* preserve the mutation result */ }
    }
  }

  close(): void {
    this.database.close();
  }
}

function isCorruptDatabaseError(error: unknown): boolean {
  if (!(error instanceof Error)) return false;
  const value = error as Error & { errcode?: number; code?: string };
  return value.errcode === 11 || value.errcode === 26 || /database disk image is malformed|database corruption|file is not a database|SQLITE_CORRUPT|SQLITE_NOTADB/i.test(`${value.code ?? ""} ${value.message}`);
}

function isSqliteBusyError(error: unknown): boolean {
  if (!(error instanceof Error)) return false;
  const value = error as Error & { code?: string; errcode?: number };
  return value.errcode === 5 || /SQLITE_BUSY|database is locked/i.test(`${value.code ?? ""} ${value.message}`);
}
