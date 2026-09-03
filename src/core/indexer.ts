import { createHash } from "node:crypto";
import { lstat, stat } from "node:fs/promises";
import path from "node:path";
import fg from "fast-glob";
import type { AppConfig, IndexRunSummary, KnowledgeSourceConfig } from "../shared/contracts.js";
import { IndexDatabase } from "./database.js";
import {
  GoCoreClient,
  resolveGoCorePath,
  type GoCoreHello,
  type GoCoreMetrics,
} from "./go-core-client.js";
import { isPathInside, sourceIndexIdentity } from "./paths.js";
import type { FileCandidate } from "./types.js";

export interface IndexOptions {
  full?: boolean;
  sourceIds?: string[];
  signal?: AbortSignal;
  onProgress?: (summary: IndexRunSummary) => void;
  mutationHeartbeat?: () => void;
}

export interface SourceSnapshot {
  sourceId: string;
  enabled: boolean;
  available: boolean;
  fileCount: number;
  totalBytes: number;
  latestMtimeMs: number | null;
  fingerprint: string | null;
  checkedAt: string;
  error: string | null;
}

export interface InspectSourcesOptions {
  sourceIds?: string[];
  includeDisabled?: boolean;
  signal?: AbortSignal;
}

function cloneSummary(summary: IndexRunSummary): IndexRunSummary {
  return { ...summary };
}

function isTemporaryFile(filePath: string): boolean {
  const name = path.basename(filePath);
  return name.startsWith("~$") || /^~WRL.*\.tmp$/i.test(name) || /\.(?:tmp|bak)$/i.test(name);
}

function sourceIgnorePatterns(source: KnowledgeSourceConfig): string[] {
  return source.excludeDirectoryNames.flatMap((name) => [...new Set([name, name.toLowerCase(), name.toUpperCase()])]
    .flatMap((variant) => [`**/${variant}`, `**/${variant}/**`]));
}

async function discoverSource(source: KnowledgeSourceConfig, summary: Pick<IndexRunSummary, "skipped">): Promise<FileCandidate[]> {
  const candidates: FileCandidate[] = [];
  const rootStat = await stat(source.rootPath);
  if (!rootStat.isDirectory()) throw new Error(`资料源不是目录：${source.rootPath}`);
  const extensionSet = new Set(source.includeExtensions.map((extension) => extension.toLowerCase()));
  const files = await fg("**/*", {
    cwd: source.rootPath,
    absolute: true,
    onlyFiles: true,
    dot: true,
    followSymbolicLinks: false,
    suppressErrors: false,
    ignore: sourceIgnorePatterns(source),
  });
  for (const discoveredPath of files) {
    const absolutePath = path.resolve(discoveredPath);
    if (isTemporaryFile(absolutePath)) {
      summary.skipped += 1;
      continue;
    }
    const extension = path.extname(absolutePath).toLowerCase();
    if (!extensionSet.has(extension)) continue;
    if (!isPathInside(source.rootPath, absolutePath)) {
      summary.skipped += 1;
      continue;
    }
    const fileStat = await lstat(absolutePath);
    if (!fileStat.isFile() || fileStat.isSymbolicLink()) {
      summary.skipped += 1;
      continue;
    }
    if (fileStat.size > source.maxFileBytes) {
      summary.skipped += 1;
      continue;
    }
    candidates.push({
      sourceId: source.id,
      sourceLabel: source.label,
      sourceKind: source.kind,
      sourceIdentity: sourceIndexIdentity(source),
      rootPath: source.rootPath,
      absolutePath,
      relativePath: path.relative(source.rootPath, absolutePath),
      extension,
      sizeBytes: fileStat.size,
      filesystemMtimeMs: Math.trunc(fileStat.mtimeMs),
    });
  }
  return candidates;
}

export async function inspectSources(config: AppConfig, options: InspectSourcesOptions = {}): Promise<SourceSnapshot[]> {
  const requestedIds = options.sourceIds === undefined ? null : new Set(options.sourceIds);
  const selectedSources = config.sources.filter((source) =>
    (options.includeDisabled || source.enabled)
    && (requestedIds === null || requestedIds.has(source.id))
  );
  const snapshots: SourceSnapshot[] = [];
  for (const source of selectedSources) {
    options.signal?.throwIfAborted();
    const checkedAt = new Date().toISOString();
    try {
      const scratch = { skipped: 0 };
      const candidates = await discoverSource(source, scratch);
      candidates.sort((left, right) => left.relativePath.localeCompare(right.relativePath, "zh-CN"));
      const hash = createHash("sha256");
      let totalBytes = 0;
      let latestMtimeMs: number | null = null;
      for (const candidate of candidates) {
        options.signal?.throwIfAborted();
        totalBytes += candidate.sizeBytes;
        latestMtimeMs = Math.max(latestMtimeMs ?? candidate.filesystemMtimeMs, candidate.filesystemMtimeMs);
        hash.update(candidate.relativePath.normalize("NFC"));
        hash.update("\0");
        hash.update(String(candidate.sizeBytes));
        hash.update("\0");
        hash.update(String(candidate.filesystemMtimeMs));
        hash.update("\n");
      }
      snapshots.push({
        sourceId: source.id,
        enabled: source.enabled,
        available: true,
        fileCount: candidates.length,
        totalBytes,
        latestMtimeMs,
        fingerprint: hash.digest("hex"),
        checkedAt,
        error: null,
      });
    } catch (error) {
      snapshots.push({
        sourceId: source.id,
        enabled: source.enabled,
        available: false,
        fileCount: 0,
        totalBytes: 0,
        latestMtimeMs: null,
        fingerprint: null,
        checkedAt,
        error: error instanceof Error ? error.message : String(error),
      });
    }
  }
  return snapshots;
}

export class KnowledgeIndexer {
  private running = false;
  private paused = false;
  private resumePhase: IndexRunSummary["phase"] = "extract";
  private currentSummary: IndexRunSummary | null = null;
  private progressCallback: ((summary: IndexRunSummary) => void) | null = null;
  private client: GoCoreClient | null = null;
  private lastMetricsValue: GoCoreMetrics | null = null;
  private lastHelloValue: GoCoreHello | null = null;
  readonly binaryPath: string;

  constructor(private readonly database: IndexDatabase) {
    this.binaryPath = resolveGoCorePath();
  }

  get isRunning(): boolean {
    return this.running;
  }

  get isPaused(): boolean {
    return this.paused;
  }

  get summary(): IndexRunSummary | null {
    return this.currentSummary ? cloneSummary(this.currentSummary) : null;
  }

  get lastMetrics(): GoCoreMetrics | null {
    return this.lastMetricsValue ? { ...this.lastMetricsValue } : null;
  }

  get lastHello(): GoCoreHello | null {
    return this.lastHelloValue ? { ...this.lastHelloValue, capabilities: [...this.lastHelloValue.capabilities] } : null;
  }

  get pid(): number | null {
    return this.client?.pid ?? null;
  }

  pause(): IndexRunSummary | null {
    if (!this.running || !this.currentSummary) return null;
    if (!this.paused) {
      this.paused = true;
      this.resumePhase = ["discover", "extract", "chunk", "index"].includes(this.currentSummary.phase)
        ? this.currentSummary.phase
        : "extract";
      this.currentSummary.phase = "paused";
      this.client?.pause();
      this.progressCallback?.(cloneSummary(this.currentSummary));
    }
    return cloneSummary(this.currentSummary);
  }

  resume(): IndexRunSummary | null {
    if (!this.running || !this.currentSummary) return null;
    if (this.paused) {
      this.paused = false;
      this.currentSummary.phase = this.resumePhase;
      this.client?.resume();
      this.progressCallback?.(cloneSummary(this.currentSummary));
    }
    return cloneSummary(this.currentSummary);
  }

  async run(config: AppConfig, options: IndexOptions = {}): Promise<IndexRunSummary> {
    if (this.running) throw new Error("索引任务已在运行");
    if (options.full && options.sourceIds !== undefined) {
      throw new Error("full 与 sourceIds 不能同时使用；来源级更新请使用增量索引");
    }
    this.running = true;
    this.paused = false;
    this.lastMetricsValue = null;
    const provisional: IndexRunSummary = {
      runId: `go-pending-${process.pid}-${Date.now()}`,
      phase: "discover",
      startedAt: new Date().toISOString(),
      finishedAt: null,
      discovered: 0,
      indexed: 0,
      unchanged: 0,
      skipped: 0,
      failed: 0,
      deleted: 0,
      currentPath: null,
      error: null,
    };
    this.currentSummary = provisional;
    this.progressCallback = options.onProgress ?? null;
    const client = new GoCoreClient(this.binaryPath);
    this.client = client;

    try {
      const requestedSourceIds = options.sourceIds === undefined ? null : new Set(options.sourceIds);
      this.database.markSourceReconciliationPending(config.sources
        .filter((source) => source.enabled && (requestedSourceIds === null || requestedSourceIds.has(source.id)))
        .map((source) => ({ sourceId: source.id, sourceIdentity: sourceIndexIdentity(source) })), options.mutationHeartbeat);
      const result = await client.index({
        databasePath: this.database.databasePath,
        config,
        full: Boolean(options.full),
        ...(options.sourceIds ? { sourceIds: options.sourceIds } : {}),
        ...(options.signal ? { signal: options.signal } : {}),
        onProgress: (summary) => {
          this.currentSummary = cloneSummary(summary);
          if (this.paused && !["complete", "failed"].includes(summary.phase)) {
            this.currentSummary.phase = "paused";
          }
          this.progressCallback?.(cloneSummary(this.currentSummary));
        },
      });
      this.lastMetricsValue = { ...result.metrics };
      this.lastHelloValue = { ...result.hello, capabilities: [...result.hello.capabilities] };
      this.database.setIndexBackendRun({
        runId: result.summary.runId,
        finishedAt: result.summary.finishedAt,
        hello: this.lastHelloValue,
        metrics: this.lastMetricsValue,
      });
      this.currentSummary = cloneSummary(result.summary);
      return cloneSummary(result.summary);
    } catch (error) {
      const summary = this.currentSummary ?? provisional;
      summary.phase = "failed";
      summary.error = error instanceof Error ? error.message : String(error);
      summary.finishedAt ??= new Date().toISOString();
      summary.currentPath = null;
      this.progressCallback?.(cloneSummary(summary));
      throw error;
    } finally {
      this.client = null;
      this.paused = false;
      this.currentSummary = null;
      this.progressCallback = null;
      this.running = false;
    }
  }
}
