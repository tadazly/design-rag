import { watch, type FSWatcher } from "node:fs";
import { randomUUID } from "node:crypto";
import type { AppNotice, IndexRunSummary } from "../shared/contracts.js";
import { MutationLeaseBusyError, type KnowledgeBaseService } from "../core/service.js";

type AutomaticIndexReason = "startup" | "filesystem" | "scheduled" | "source-added" | "source-updated";

interface PendingIndex {
  reason: AutomaticIndexReason;
  sourceIds: Set<string> | null;
}

interface IndexCoordinatorCallbacks {
  onProgress(run: IndexRunSummary): void;
  onNotice(notice: AppNotice): void;
  onSnapshot(): void;
  onError(message: string): void;
}

const WATCH_DEBOUNCE_MS = 1_500;
const LEASE_RETRY_BASE_MS = 250;
const LEASE_RETRY_MAX_MS = 5_000;

export class IndexCoordinator {
  private readonly watchers = new Map<string, FSWatcher>();
  private initialTimer: NodeJS.Timeout | null = null;
  private recurringTimer: NodeJS.Timeout | null = null;
  private debounceTimer: NodeJS.Timeout | null = null;
  private retryTimer: NodeJS.Timeout | null = null;
  private pending: PendingIndex | null = null;
  private running = false;
  private stopped = false;
  private leaseRetryAttempt = 0;
  private warnedWatchers = new Set<string>();

  constructor(
    private readonly knowledge: KnowledgeBaseService,
    private readonly callbacks: IndexCoordinatorCallbacks,
  ) {}

  start(): void {
    this.stopped = false;
    this.refresh(true);
  }

  refresh(runInitial = true): void {
    this.clearScheduling();
    if (this.stopped || !this.knowledge.config.indexing.automaticScan) return;
    this.installWatchers();
    if (runInitial) this.initialTimer = setTimeout(() => this.request("startup"), 750);
    this.recurringTimer = setInterval(
      () => this.request("scheduled"),
      this.knowledge.config.indexing.scanIntervalMinutes * 60_000,
    );
  }

  request(reason: AutomaticIndexReason, sourceIds?: string[]): void {
    if (this.stopped || !this.knowledge.config.indexing.automaticScan) return;
    const requested = sourceIds?.length ? new Set(sourceIds) : null;
    if (!this.pending) {
      this.pending = { reason, sourceIds: requested };
    } else {
      this.pending.reason = reason;
      if (this.pending.sourceIds === null || requested === null) {
        this.pending.sourceIds = null;
      } else {
        requested.forEach((sourceId) => this.pending?.sourceIds?.add(sourceId));
      }
    }
    if (!this.retryTimer) this.scheduleDrain(reason === "filesystem" ? WATCH_DEBOUNCE_MS : 0);
  }

  stop(): void {
    this.stopped = true;
    this.pending = null;
    this.clearScheduling();
  }

  private scheduleDrain(delayMs: number): void {
    if (this.debounceTimer) clearTimeout(this.debounceTimer);
    this.debounceTimer = setTimeout(() => {
      this.debounceTimer = null;
      void this.drain();
    }, delayMs);
  }

  private async drain(): Promise<void> {
    if (this.stopped || this.running || !this.pending) return;
    if (this.knowledge.indexer.isRunning) {
      this.scheduleRetry(1_000);
      return;
    }
    const next = this.pending;
    this.pending = null;
    this.running = true;
    try {
      const options = {
        sourceIds: next.sourceIds ? [...next.sourceIds] : undefined,
        onProgress: (run: IndexRunSummary) => this.callbacks.onProgress(run),
      } as Parameters<KnowledgeBaseService["index"]>[0];
      const result = await this.knowledge.index(options);
      this.leaseRetryAttempt = 0;
      this.callbacks.onSnapshot();
      const changed = result.indexed + result.deleted > 0;
      if (changed || next.reason === "startup" || next.reason === "source-added") {
        this.callbacks.onNotice({
          id: `notice_${randomUUID()}`,
          kind: changed ? "index-updated" : "info",
          title: changed ? "索引已自动更新" : "索引已是最新",
          message: changed
            ? `新增或更新 ${result.indexed} 份，移除 ${result.deleted} 份失效索引。`
            : `已检查 ${result.discovered} 份资料，没有发现内容变化。`,
          createdAt: Date.now(),
        });
      }
    } catch (error) {
      if (error instanceof MutationLeaseBusyError && !this.stopped) {
        this.restorePending(next);
        const retryDelay = Math.min(LEASE_RETRY_MAX_MS, LEASE_RETRY_BASE_MS * (2 ** this.leaseRetryAttempt));
        this.leaseRetryAttempt += 1;
        this.scheduleRetry(retryDelay);
      } else {
        this.leaseRetryAttempt = 0;
        this.callbacks.onError(error instanceof Error ? error.message : String(error));
      }
    } finally {
      this.running = false;
      if (!this.stopped && this.pending && !this.retryTimer) this.scheduleDrain(0);
    }
  }

  private restorePending(previous: PendingIndex): void {
    if (!this.pending) {
      this.pending = previous;
      return;
    }
    if (this.pending.sourceIds === null || previous.sourceIds === null) {
      this.pending.sourceIds = null;
      return;
    }
    previous.sourceIds.forEach((sourceId) => this.pending?.sourceIds?.add(sourceId));
  }

  private scheduleRetry(delayMs: number): void {
    if (this.retryTimer) return;
    if (this.debounceTimer) clearTimeout(this.debounceTimer);
    this.debounceTimer = null;
    this.retryTimer = setTimeout(() => {
      this.retryTimer = null;
      this.scheduleDrain(0);
    }, delayMs);
  }

  private installWatchers(): void {
    for (const source of this.knowledge.config.sources) {
      if (!source.enabled) continue;
      try {
        const watcher = watch(source.rootPath, { recursive: true, persistent: false }, () => {
          this.request("filesystem", [source.id]);
        });
        watcher.on("error", (error) => this.reportWatcherError(source.id, source.label, error));
        this.watchers.set(source.id, watcher);
      } catch (error) {
        this.reportWatcherError(source.id, source.label, error);
      }
    }
  }

  private reportWatcherError(sourceId: string, label: string, error: unknown): void {
    if (this.warnedWatchers.has(sourceId)) return;
    this.warnedWatchers.add(sourceId);
    this.callbacks.onNotice({
      id: `notice_${randomUUID()}`,
      kind: "warning",
      title: `${label}实时监听不可用`,
      message: `仍会按计划执行增量扫描：${error instanceof Error ? error.message : String(error)}`,
      createdAt: Date.now(),
    });
  }

  private clearScheduling(): void {
    if (this.initialTimer) clearTimeout(this.initialTimer);
    if (this.recurringTimer) clearInterval(this.recurringTimer);
    if (this.debounceTimer) clearTimeout(this.debounceTimer);
    if (this.retryTimer) clearTimeout(this.retryTimer);
    this.initialTimer = null;
    this.recurringTimer = null;
    this.debounceTimer = null;
    this.retryTimer = null;
    this.leaseRetryAttempt = 0;
    for (const watcher of this.watchers.values()) watcher.close();
    this.watchers.clear();
  }
}
