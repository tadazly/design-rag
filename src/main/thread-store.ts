import { randomUUID } from "node:crypto";
import { mkdir, readFile } from "node:fs/promises";
import path from "node:path";
import writeFileAtomic from "write-file-atomic";
import type { ChatMessage, SearchResponse, ThreadSummary } from "../shared/contracts.js";

export interface LocalThreadRecord {
  id: string;
  codexThreadId: string | null;
  title: string;
  preview: string;
  createdAt: number;
  updatedAt: number;
  messages: ChatMessage[];
  evidence: SearchResponse | null;
  archivedAt: number | null;
}

interface ThreadFile {
  schemaVersion: 3;
  activeThreadId: string | null;
  threads: LocalThreadRecord[];
}

function emptyThread(): LocalThreadRecord {
  const now = Date.now();
  return {
    id: `local_${randomUUID()}`,
    codexThreadId: null,
    title: "新对话",
    preview: "",
    createdAt: now,
    updatedAt: now,
    messages: [],
    evidence: null,
    archivedAt: null,
  };
}

export class ThreadStore {
  private readonly filePath: string;
  private value: ThreadFile = { schemaVersion: 3, activeThreadId: null, threads: [] };
  private saveInFlight: Promise<void> | null = null;
  private saveRequested = false;

  constructor(dataDir: string) {
    this.filePath = path.join(dataDir, "threads.json");
  }

  async load(): Promise<void> {
    await mkdir(path.dirname(this.filePath), { recursive: true });
    try {
      const parsed = JSON.parse(await readFile(this.filePath, "utf8")) as {
        activeThreadId?: string | null;
        schemaVersion?: number;
        threads?: Array<Omit<LocalThreadRecord, "evidence" | "archivedAt"> & { evidence?: SearchResponse | null; archivedAt?: number | null }>;
      };
      if (![1, 2, 3].includes(parsed.schemaVersion ?? 0) || !Array.isArray(parsed.threads)) throw new Error("thread store schema 不兼容");
      this.value = {
        schemaVersion: 3,
        activeThreadId: parsed.activeThreadId ?? null,
        threads: parsed.threads.map((thread) => ({ ...thread, evidence: thread.evidence ?? null, archivedAt: thread.archivedAt ?? null })),
      };
      if (parsed.schemaVersion !== 3) await this.save();
    } catch (error) {
      const missing = error instanceof Error && "code" in error && error.code === "ENOENT";
      if (!missing) throw error;
      const thread = emptyThread();
      this.value = { schemaVersion: 3, activeThreadId: thread.id, threads: [thread] };
      await this.save();
    }
    const previousActiveThreadId = this.value.activeThreadId;
    const previousThreadCount = this.value.threads.length;
    this.ensureActiveThread();
    if (this.value.activeThreadId !== previousActiveThreadId || this.value.threads.length !== previousThreadCount) await this.save();
  }

  get activeThreadId(): string | null {
    return this.value.activeThreadId;
  }

  list(activeThreadId = this.value.activeThreadId): ThreadSummary[] {
    return [...this.value.threads]
      .sort((left, right) => right.updatedAt - left.updatedAt)
      .map((thread) => ({
        id: thread.id,
        title: thread.title,
        preview: thread.preview,
        createdAt: thread.createdAt,
        updatedAt: thread.updatedAt,
        active: thread.id === activeThreadId,
        archived: thread.archivedAt !== null,
      }));
  }

  active(): LocalThreadRecord {
    const thread = this.value.threads.find((item) => item.id === this.value.activeThreadId);
    if (!thread) throw new Error("没有活动 thread");
    return thread;
  }

  get(id: string): LocalThreadRecord | null {
    return this.value.threads.find((thread) => thread.id === id) ?? null;
  }

  create(): LocalThreadRecord {
    const thread = emptyThread();
    this.value.threads.push(thread);
    this.value.activeThreadId = thread.id;
    void this.save().catch(() => undefined);
    return thread;
  }

  archive(id: string): LocalThreadRecord {
    const thread = this.get(id);
    if (!thread) throw new Error(`thread 不存在：${id}`);
    thread.archivedAt = Date.now();
    thread.updatedAt = Date.now();
    this.ensureActiveThread();
    void this.save().catch(() => undefined);
    return thread;
  }

  restore(id: string): LocalThreadRecord {
    const thread = this.get(id);
    if (!thread) throw new Error(`thread 不存在：${id}`);
    thread.archivedAt = null;
    thread.updatedAt = Date.now();
    void this.save().catch(() => undefined);
    return thread;
  }

  remove(id: string): void {
    const index = this.value.threads.findIndex((thread) => thread.id === id);
    if (index < 0) throw new Error(`thread 不存在：${id}`);
    this.value.threads.splice(index, 1);
    this.ensureActiveThread();
    void this.save().catch(() => undefined);
  }

  select(id: string): LocalThreadRecord {
    const thread = this.get(id);
    if (!thread) throw new Error(`thread 不存在：${id}`);
    if (thread.archivedAt !== null) throw new Error("请先恢复已归档对话");
    this.value.activeThreadId = id;
    void this.save().catch(() => undefined);
    return thread;
  }

  update(
    id: string,
    update: Partial<Pick<LocalThreadRecord, "codexThreadId" | "title" | "preview" | "messages" | "evidence" | "updatedAt">>,
    options: { persist?: boolean } = {},
  ): LocalThreadRecord {
    const thread = this.get(id);
    if (!thread) throw new Error(`thread 不存在：${id}`);
    Object.assign(thread, update);
    if (options.persist !== false) void this.save().catch(() => undefined);
    return thread;
  }

  clearEvidence(): void {
    for (const thread of this.value.threads) thread.evidence = null;
    void this.save().catch(() => undefined);
  }

  private ensureActiveThread(): LocalThreadRecord {
    const current = this.value.threads.find((thread) => thread.id === this.value.activeThreadId && thread.archivedAt === null);
    if (current) return current;
    const next = [...this.value.threads]
      .filter((thread) => thread.archivedAt === null)
      .sort((left, right) => right.updatedAt - left.updatedAt)[0];
    if (next) {
      this.value.activeThreadId = next.id;
      return next;
    }
    const thread = emptyThread();
    this.value.threads.push(thread);
    this.value.activeThreadId = thread.id;
    return thread;
  }

  save(): Promise<void> {
    this.saveRequested = true;
    if (!this.saveInFlight) {
      this.saveInFlight = this.flushSaves().finally(() => {
        this.saveInFlight = null;
      });
    }
    return this.saveInFlight;
  }

  private async flushSaves(): Promise<void> {
    while (this.saveRequested) {
      this.saveRequested = false;
      const serialized = `${JSON.stringify(this.value, null, 2)}\n`;
      await writeFileAtomic(this.filePath, serialized, { encoding: "utf8", mode: 0o600 });
    }
  }
}
