import { spawn, execFile, type ChildProcessWithoutNullStreams } from "node:child_process";
import { EventEmitter } from "node:events";
import { existsSync } from "node:fs";
import path from "node:path";
import { promisify } from "node:util";
import { APP_VERSION, type AccountStatus, type ModelOption } from "../shared/contracts.js";

const execFileAsync = promisify(execFile);

interface PendingRequest {
  resolve: (value: unknown) => void;
  reject: (error: Error) => void;
  timer: NodeJS.Timeout;
}

interface ProtocolMessage {
  id?: number | string;
  method?: string;
  params?: Record<string, unknown>;
  result?: unknown;
  error?: { code?: number; message?: string; data?: unknown };
}

export interface DynamicToolCall {
  threadId: string;
  turnId: string;
  callId: string;
  namespace: string | null;
  tool: string;
  arguments: unknown;
}

export type DynamicToolHandler = (call: DynamicToolCall) => Promise<unknown>;

function protocolError(message: ProtocolMessage): Error {
  return new Error(message.error?.message ?? `app-server 请求失败：${JSON.stringify(message.error)}`);
}

async function findOnPath(command: string): Promise<string | null> {
  const locator = process.platform === "win32" ? "where.exe" : "which";
  try {
    const { stdout } = await execFileAsync(locator, [command], { windowsHide: true, timeout: 5_000 });
    return stdout.split(/\r?\n/).map((line) => line.trim()).find(Boolean) ?? null;
  } catch {
    return null;
  }
}

export async function resolveCodexPath(configuredPath: string | null): Promise<string> {
  if (configuredPath) {
    const resolved = path.resolve(configuredPath);
    if (!existsSync(resolved)) throw new Error(`配置的 Codex 不存在：${resolved}`);
    return resolved;
  }
  const fromPath = await findOnPath(process.platform === "win32" ? "codex.exe" : "codex");
  if (fromPath) return fromPath;
  if (process.platform === "win32" && process.env.LOCALAPPDATA) {
    const stableDesktopPath = path.join(process.env.LOCALAPPDATA, "Programs", "OpenAI", "Codex", "bin", "codex.exe");
    if (existsSync(stableDesktopPath)) return stableDesktopPath;
  }
  throw new Error("未找到 Codex CLI。请安装 Codex，或在设置中指定 codex.exe 路径。");
}

export class CodexAppServerClient extends EventEmitter {
  private process: ChildProcessWithoutNullStreams | null = null;
  private nextId = 1;
  private pending = new Map<number | string, PendingRequest>();
  private stdoutBuffer = "";
  private dynamicToolHandler: DynamicToolHandler | null = null;
  private started = false;
  codexPath: string | null = null;
  codexVersion: string | null = null;

  setDynamicToolHandler(handler: DynamicToolHandler): void {
    this.dynamicToolHandler = handler;
  }

  async start(configuredPath: string | null): Promise<void> {
    if (this.started) return;
    this.codexPath = await resolveCodexPath(configuredPath);
    const { stdout: versionOutput } = await execFileAsync(this.codexPath, ["--version"], {
      windowsHide: true,
      timeout: 8_000,
    });
    this.codexVersion = versionOutput.trim();
    this.process = spawn(this.codexPath, ["app-server", "--listen", "stdio://"], {
      shell: false,
      windowsHide: true,
      stdio: ["pipe", "pipe", "pipe"],
    });
    this.process.stdout.setEncoding("utf8");
    this.process.stderr.setEncoding("utf8");
    this.process.stdout.on("data", (chunk: string) => this.handleStdout(chunk));
    this.process.stderr.on("data", (chunk: string) => this.emit("log", chunk));
    this.process.once("error", (error) => this.handleExit(error));
    this.process.once("exit", (code, signal) => this.handleExit(new Error(`Codex app-server 已退出（code=${code}, signal=${signal}）`)));

    await this.request("initialize", {
      clientInfo: {
        name: "drag_knowledge",
        title: "DRAG 游戏策划知识库",
        version: APP_VERSION,
      },
      capabilities: {
        experimentalApi: true,
        requestAttestation: false,
      },
    }, 15_000);
    this.notify("initialized", {});
    this.started = true;
  }

  async stop(): Promise<void> {
    const child = this.process;
    this.process = null;
    this.started = false;
    if (!child) return;
    child.stdin.end();
    await new Promise<void>((resolve) => {
      const timer = setTimeout(() => {
        child.kill();
        resolve();
      }, 3_000);
      child.once("exit", () => {
        clearTimeout(timer);
        resolve();
      });
    });
  }

  async accountStatus(): Promise<AccountStatus> {
    const result = await this.request("account/read", { refreshToken: false }) as {
      account?: { type?: string; planType?: string | null } | null;
    };
    return {
      connected: Boolean(result.account),
      authMode: result.account?.type ?? null,
      planType: result.account?.planType ?? null,
      codexVersion: this.codexVersion,
      error: null,
    };
  }

  async listModels(): Promise<ModelOption[]> {
    const models: ModelOption[] = [];
    let cursor: string | null = null;
    for (let page = 0; page < 20; page += 1) {
      const result = await this.request("model/list", {
        cursor,
        limit: 100,
        includeHidden: false,
      }) as {
        data?: Array<{
          id?: string;
          model?: string;
          displayName?: string;
          description?: string;
          hidden?: boolean;
          isDefault?: boolean;
          defaultReasoningEffort?: string;
          supportedReasoningEfforts?: Array<{ reasoningEffort?: string; description?: string }>;
        }>;
        nextCursor?: string | null;
      };
      for (const item of result.data ?? []) {
        const model = item.model?.trim();
        if (!model || item.hidden) continue;
        models.push({
          id: item.id?.trim() || model,
          model,
          displayName: item.displayName?.trim() || model,
          description: item.description?.trim() || "",
          hidden: Boolean(item.hidden),
          isDefault: Boolean(item.isDefault),
          defaultReasoningEffort: item.defaultReasoningEffort?.trim() || "medium",
          supportedReasoningEfforts: (item.supportedReasoningEfforts ?? [])
            .flatMap((option) => option.reasoningEffort?.trim()
              ? [{ value: option.reasoningEffort.trim(), description: option.description?.trim() || "" }]
              : []),
        });
      }
      cursor = result.nextCursor ?? null;
      if (!cursor) break;
    }
    return models;
  }

  request(method: string, params: Record<string, unknown> = {}, timeoutMs = 60_000): Promise<unknown> {
    const id = this.nextId;
    this.nextId += 1;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`app-server 请求超时：${method}`));
      }, timeoutMs);
      this.pending.set(id, { resolve, reject, timer });
      try {
        this.write({ id, method, params });
      } catch (error) {
        clearTimeout(timer);
        this.pending.delete(id);
        reject(error instanceof Error ? error : new Error(String(error)));
      }
    });
  }

  notify(method: string, params: Record<string, unknown> = {}): void {
    this.write({ method, params });
  }

  private write(message: ProtocolMessage): void {
    if (!this.process?.stdin.writable) throw new Error("Codex app-server 尚未连接");
    this.process.stdin.write(`${JSON.stringify(message)}\n`);
  }

  private handleStdout(chunk: string): void {
    this.stdoutBuffer += chunk;
    while (true) {
      const newline = this.stdoutBuffer.indexOf("\n");
      if (newline < 0) return;
      const line = this.stdoutBuffer.slice(0, newline).trim();
      this.stdoutBuffer = this.stdoutBuffer.slice(newline + 1);
      if (!line) continue;
      try {
        this.handleMessage(JSON.parse(line) as ProtocolMessage);
      } catch (error) {
        this.emit("protocol-error", new Error(`无法解析 app-server JSONL：${error instanceof Error ? error.message : String(error)}`));
      }
    }
  }

  private handleMessage(message: ProtocolMessage): void {
    if (message.id !== undefined && !message.method) {
      const pending = this.pending.get(message.id);
      if (!pending) return;
      this.pending.delete(message.id);
      clearTimeout(pending.timer);
      if (message.error) pending.reject(protocolError(message));
      else pending.resolve(message.result);
      return;
    }
    if (message.id !== undefined && message.method) {
      void this.handleServerRequest(message);
      return;
    }
    if (message.method) this.emit("notification", message.method, message.params ?? {});
  }

  private async handleServerRequest(message: ProtocolMessage): Promise<void> {
    const requestId = message.id;
    if (requestId === undefined) return;
    if (message.method !== "item/tool/call" || !this.dynamicToolHandler) {
      this.write({
        id: requestId,
        error: { code: -32601, message: `不支持的 app-server 请求：${message.method}` },
      });
      return;
    }
    try {
      const call = message.params as unknown as DynamicToolCall;
      const result = await this.dynamicToolHandler(call);
      this.write({
        id: requestId,
        result: {
          success: true,
          contentItems: [{ type: "inputText", text: JSON.stringify(result) }],
        },
      });
    } catch (error) {
      this.write({
        id: requestId,
        result: {
          success: false,
          contentItems: [{ type: "inputText", text: error instanceof Error ? error.message : String(error) }],
        },
      });
    }
  }

  private handleExit(error: Error): void {
    if (!this.process && !this.started) return;
    this.process = null;
    this.started = false;
    for (const pending of this.pending.values()) {
      clearTimeout(pending.timer);
      pending.reject(error);
    }
    this.pending.clear();
    this.emit("exit", error);
  }
}
