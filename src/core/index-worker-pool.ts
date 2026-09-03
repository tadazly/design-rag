import { Worker } from "node:worker_threads";
import type { IndexTaskInput, IndexTaskResult, IndexWorkerRequest, IndexWorkerResponse } from "./index-task.js";

interface PendingJob {
  id: number;
  input: IndexTaskInput;
  resolve: (result: IndexTaskResult) => void;
  reject: (error: Error) => void;
}

interface WorkerSlot {
  worker: Worker;
  current: PendingJob | null;
  dead: boolean;
  completedJobs: number;
}

export class IndexWorkerTaskError extends Error {
  constructor(readonly code: string, message: string, options: ErrorOptions = {}) {
    super(message, options);
    this.name = "IndexWorkerTaskError";
  }
}

export class IndexWorkerPool {
  private readonly slots: WorkerSlot[] = [];
  private readonly queue: PendingJob[] = [];
  private nextId = 1;
  private started = false;
  private closed = false;

  private readonly maxJobsPerWorker: number;
  private readonly maxOldGenerationSizeMb: number;

  constructor(private readonly size: number, options: { maxJobsPerWorker?: number; maxOldGenerationSizeMb?: number } = {}) {
    if (!Number.isInteger(size) || size < 1) throw new Error("worker 数量必须是正整数");
    this.maxJobsPerWorker = Math.max(1, options.maxJobsPerWorker ?? 250);
    this.maxOldGenerationSizeMb = Math.max(128, options.maxOldGenerationSizeMb ?? 768);
  }

  run(input: IndexTaskInput): Promise<IndexTaskResult> {
    if (this.closed) return Promise.reject(new Error("索引 worker pool 已关闭"));
    if (!this.started) this.start();
    return new Promise<IndexTaskResult>((resolve, reject) => {
      this.queue.push({ id: this.nextId++, input, resolve, reject });
      this.dispatch();
    });
  }

  async close(): Promise<void> {
    if (this.closed) return;
    this.closed = true;
    const error = new Error("索引 worker pool 已关闭");
    for (const job of this.queue.splice(0)) job.reject(error);
    for (const slot of this.slots) {
      slot.current?.reject(error);
      slot.current = null;
      slot.dead = true;
    }
    await Promise.allSettled(this.slots.map((slot) => slot.worker.terminate()));
    this.slots.length = 0;
  }

  private start(): void {
    this.started = true;
    for (let index = 0; index < this.size; index += 1) this.spawn(index);
  }

  private spawn(index: number): void {
    if (this.closed) return;
    const worker = new Worker(new URL("./index-worker.js", import.meta.url), {
      name: `drag-index-${index + 1}`,
      execArgv: [],
      resourceLimits: { maxOldGenerationSizeMb: this.maxOldGenerationSizeMb },
    });
    const slot: WorkerSlot = { worker, current: null, dead: false, completedJobs: 0 };
    this.slots.push(slot);
    worker.on("message", (response: IndexWorkerResponse) => this.handleMessage(slot, response));
    worker.once("error", (error) => this.handleWorkerFailure(slot, error instanceof Error ? error : new Error(String(error))));
    worker.once("exit", (code) => {
      if (!slot.dead && !this.closed) this.handleWorkerFailure(slot, new Error(`索引 worker 异常退出（code ${code}）`));
    });
  }

  private handleMessage(slot: WorkerSlot, response: IndexWorkerResponse): void {
    const job = slot.current;
    if (!job || response.id !== job.id) {
      this.handleWorkerFailure(slot, new Error("索引 worker 返回了无法匹配的任务"));
      return;
    }
    slot.current = null;
    slot.completedJobs += 1;
    if (response.ok) {
      job.resolve(response.result);
    } else {
      const cause = response.stack ? new Error(response.stack) : undefined;
      job.reject(new IndexWorkerTaskError(response.code, response.message, { cause }));
    }
    if (slot.completedJobs >= this.maxJobsPerWorker) {
      this.replaceWorker(slot);
      return;
    }
    this.dispatch();
  }

  private replaceWorker(slot: WorkerSlot): void {
    if (slot.dead) return;
    slot.dead = true;
    const position = this.slots.indexOf(slot);
    if (position >= 0) this.slots.splice(position, 1);
    void slot.worker.terminate().catch(() => undefined);
    if (!this.closed) {
      this.spawn(this.slots.length);
      this.dispatch();
    }
  }

  private handleWorkerFailure(slot: WorkerSlot, error: Error): void {
    if (slot.dead) return;
    slot.dead = true;
    slot.current?.reject(error);
    slot.current = null;
    const position = this.slots.indexOf(slot);
    if (position >= 0) this.slots.splice(position, 1);
    void slot.worker.terminate().catch(() => undefined);
    if (!this.closed) {
      this.spawn(this.slots.length);
      this.dispatch();
    }
  }

  private dispatch(): void {
    if (this.closed) return;
    for (const slot of this.slots) {
      if (slot.dead || slot.current) continue;
      const job = this.queue.shift();
      if (!job) return;
      slot.current = job;
      const request: IndexWorkerRequest = { id: job.id, input: job.input };
      slot.worker.postMessage(request);
    }
  }
}
