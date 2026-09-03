import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { existsSync } from "node:fs";
import path from "node:path";
import { createInterface } from "node:readline";
import { fileURLToPath } from "node:url";
import type { AppConfig, IndexRunSummary } from "../shared/contracts.js";
import { processIndexTask, type IndexTaskInput } from "./index-task.js";
import { sourceIndexIdentity } from "./paths.js";

export const GO_CORE_PROTOCOL_VERSION = 3;

export interface GoCoreMetrics {
  backend: "go";
  backendVersion: string;
  protocolVersion: number;
  wallClockMs: number;
  discoverMs: number;
  extractAndIndexMs: number;
  finalizeMs: number;
  bytesRead: number;
  peakHeapAllocBytes: number;
  peakHeapSystemBytes: number;
  peakGoroutines: number;
  workerCount: number;
  documentsPerSecond: number;
  chunksWritten: number;
  fallbackDocuments: number;
  workerTaskMsTotal: number;
  maxTaskMs: number;
  fallbackTaskMsTotal: number;
  sqliteWriteMs: number;
  peakWorkingSetBytes: number;
  cpuTimeMs: number;
  peakCpuPercent: number;
}

export interface GoCoreHello {
  protocolVersion: number;
  backendVersion: string;
  pid: number;
  platform: string;
  arch: string;
  capabilities: string[];
}

export interface GoCoreIndexResult {
  summary: IndexRunSummary;
  metrics: GoCoreMetrics;
  hello: GoCoreHello;
}

interface GoCoreMessage {
  type?: string;
  id?: string;
  protocolVersion?: number;
  backendVersion?: string;
  pid?: number;
  platform?: string;
  arch?: string;
  capabilities?: string[];
  summary?: IndexRunSummary;
  metrics?: GoCoreMetrics;
  code?: string;
  message?: string;
  input?: IndexTaskInput;
}

function executableName(): string {
  return process.platform === "win32" ? "drag-core.exe" : "drag-core";
}

export function resolveGoCorePath(): string {
  const configured = process.env.DESIGN_RAG_GO_CORE_PATH?.trim();
  const dirname = path.dirname(fileURLToPath(import.meta.url));
  const candidateResourcesPath = (process as NodeJS.Process & { resourcesPath?: unknown }).resourcesPath;
  const resourcesPath = typeof candidateResourcesPath === "string" ? candidateResourcesPath : null;
  const candidates = [
    configured,
    resourcesPath ? path.join(resourcesPath, "app.asar.unpacked", "dist", "native", executableName()) : null,
    resourcesPath ? path.join(resourcesPath, "dist", "native", executableName()) : null,
    path.resolve(dirname, "..", "native", executableName()),
    path.resolve(dirname, "..", "..", "..", "dist", "native", executableName()),
    path.resolve(process.cwd(), "dist", "native", executableName()),
  ].filter((value): value is string => Boolean(value));
  const resolved = candidates.find((candidate) => existsSync(candidate));
  if (!resolved) {
    throw new Error(`未找到 Go 索引核心 ${executableName()}；已检查：${candidates.join("；")}`);
  }
  return resolved;
}

function asHello(message: GoCoreMessage): GoCoreHello {
  if (
    message.type !== "hello"
    || message.protocolVersion !== GO_CORE_PROTOCOL_VERSION
    || typeof message.backendVersion !== "string"
    || typeof message.pid !== "number"
    || typeof message.platform !== "string"
    || typeof message.arch !== "string"
    || !Array.isArray(message.capabilities)
  ) {
    throw new Error(`Go 核心握手不兼容：host=${GO_CORE_PROTOCOL_VERSION} backend=${String(message.protocolVersion)}`);
  }
  return {
    protocolVersion: message.protocolVersion,
    backendVersion: message.backendVersion,
    pid: message.pid,
    platform: message.platform,
    arch: message.arch,
    capabilities: message.capabilities,
  };
}

export class GoCoreClient {
  readonly binaryPath: string;
  private child: ChildProcessWithoutNullStreams | null = null;
  private currentHello: GoCoreHello | null = null;
  private stderr = "";

  constructor(binaryPath = resolveGoCorePath()) {
    this.binaryPath = binaryPath;
  }

  get pid(): number | null {
    return this.child?.pid ?? null;
  }

  get hello(): GoCoreHello | null {
    return this.currentHello;
  }

  pause(): void {
    this.send({ type: "control", command: "pause" });
  }

  resume(): void {
    this.send({ type: "control", command: "resume" });
  }

  cancel(): void {
    this.send({ type: "control", command: "cancel" });
  }

  async index(input: {
    databasePath: string;
    config: AppConfig;
    full: boolean;
    sourceIds?: string[];
    signal?: AbortSignal;
    onProgress?: (summary: IndexRunSummary) => void;
  }): Promise<GoCoreIndexResult> {
    if (this.child) throw new Error("Go 索引核心任务已在运行");
    input.signal?.throwIfAborted();
    const child = spawn(this.binaryPath, [], {
      cwd: process.cwd(),
      env: process.env,
      shell: false,
      windowsHide: true,
      stdio: ["pipe", "pipe", "pipe"],
    });
    this.child = child;
    this.currentHello = null;
    this.stderr = "";
    child.stderr.setEncoding("utf8");
    child.stderr.on("data", (chunk: string) => {
      this.stderr = `${this.stderr}${chunk}`.slice(-65_536);
      process.stderr.write(chunk);
    });

    const requestId = `index-${process.pid}-${Date.now()}`;
    const lines = createInterface({ input: child.stdout, crlfDelay: Infinity });
    let settled = false;
    let helloTimer: NodeJS.Timeout | null = setTimeout(() => {
      if (!this.currentHello && !settled) child.kill();
    }, 10_000);
    helloTimer.unref();

    const abort = () => {
      this.cancel();
      const timer = setTimeout(() => child.kill(), 5_000);
      timer.unref();
    };
    input.signal?.addEventListener("abort", abort, { once: true });

    try {
      return await new Promise<GoCoreIndexResult>((resolve, reject) => {
        const finish = (action: () => void) => {
          if (settled) return;
          settled = true;
          if (helloTimer) clearTimeout(helloTimer);
          helloTimer = null;
          action();
        };

        const handleFallback = async (message: GoCoreMessage) => {
          if (!message.id || !message.input) {
            this.send({ type: "fallback_result", id: message.id ?? "", ok: false, error: "fallback_request 缺少 id 或 input" });
            return;
          }
          try {
            const result = await processIndexTask(message.input);
            if (result.kind !== "draft") throw new Error("fallback extractor 意外返回 unchanged");
            this.send({ type: "fallback_result", id: message.id, ok: true, draft: result.draft });
          } catch (error) {
            this.send({
              type: "fallback_result",
              id: message.id,
              ok: false,
              error: error instanceof Error ? error.message : String(error),
            });
          }
        };

        lines.on("line", (line) => {
          if (!line.trim() || settled) return;
          let message: GoCoreMessage;
          try {
            message = JSON.parse(line) as GoCoreMessage;
          } catch (error) {
            finish(() => reject(new Error(`Go 核心 stdout 出现非协议内容：${line.slice(0, 300)}`, { cause: error })));
            child.kill();
            return;
          }
          try {
            if (message.type === "hello") {
              this.currentHello = asHello(message);
              if (helloTimer) clearTimeout(helloTimer);
              helloTimer = null;
              this.send({
                type: "request",
                id: requestId,
                method: "index",
                protocolVersion: GO_CORE_PROTOCOL_VERSION,
                payload: {
                  databasePath: input.databasePath,
                  config: {
                    ...input.config,
                    sources: input.config.sources.map((source) => ({
                      ...source,
                      indexIdentity: sourceIndexIdentity(source),
                    })),
                  },
                  options: { full: input.full, ...(input.sourceIds ? { sourceIds: input.sourceIds } : {}) },
                },
              });
              return;
            }
            if (message.type === "progress" && message.summary) {
              input.onProgress?.(message.summary);
              return;
            }
            if (message.type === "fallback_request") {
              void handleFallback(message);
              return;
            }
            if (message.type === "result" && message.summary && message.metrics && this.currentHello) {
              finish(() => resolve({ summary: message.summary!, metrics: message.metrics!, hello: this.currentHello! }));
              return;
            }
            if (message.type === "error") {
              const detail = message.message ?? message.code ?? "Go 索引核心失败";
              finish(() => reject(new Error(detail)));
            }
          } catch (error) {
            finish(() => reject(error instanceof Error ? error : new Error(String(error))));
            child.kill();
          }
        });

        child.once("error", (error) => finish(() => reject(error)));
        child.once("exit", (code, signal) => {
          if (settled) return;
          const suffix = this.stderr.trim() ? `\n${this.stderr.trim()}` : "";
          finish(() => reject(new Error(`Go 索引核心意外退出（code=${String(code)}, signal=${String(signal)}）${suffix}`)));
        });
      });
    } finally {
      input.signal?.removeEventListener("abort", abort);
      lines.close();
      if (!child.killed && child.exitCode === null) child.kill();
      this.child = null;
    }
  }

  private send(value: unknown): void {
    const stream = this.child?.stdin;
    if (!stream || stream.destroyed) return;
    stream.write(`${JSON.stringify(value)}\n`);
  }
}
