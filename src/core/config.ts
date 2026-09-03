import { createHash } from "node:crypto";
import { mkdir, readFile, realpath, stat } from "node:fs/promises";
import path from "node:path";
import writeFileAtomic from "write-file-atomic";
import { z } from "zod";
import type { AppConfig, KnowledgeSourceConfig } from "../shared/contracts.js";
import { canonicalPathKey, resolveConfigDir, resolveDataDir } from "./paths.js";

const sourceSchema = z.object({
  id: z.string().min(1).regex(/^[a-z0-9_-]+$/),
  label: z.string().min(1),
  kind: z.enum(["design", "table"]),
  rootPath: z.string().min(1),
  enabled: z.boolean(),
  includeExtensions: z.array(z.string().min(1)),
  excludeDirectoryNames: z.array(z.string().min(1)),
  maxFileBytes: z.number().int().min(1_024).max(2_147_483_647),
});

function pathsOverlap(left: string, right: string): boolean {
  const relative = path.relative(left, right);
  return relative === "" || (!relative.startsWith("..") && !path.isAbsolute(relative));
}

function validateSourceTopology(
  sources: KnowledgeSourceConfig[],
  rootKeys: string[],
  addIssue: (index: number, field: "id" | "rootPath", message: string) => void,
): void {
  const seenIds = new Map<string, number>();
  for (const [index, source] of sources.entries()) {
    const duplicateIndex = seenIds.get(source.id);
    if (duplicateIndex !== undefined) {
      addIssue(index, "id", `资料源 id 与第 ${duplicateIndex + 1} 项重复：${source.id}`);
    } else {
      seenIds.set(source.id, index);
    }
    for (let previousIndex = 0; previousIndex < index; previousIndex += 1) {
      const currentRoot = rootKeys[index];
      const previousRoot = rootKeys[previousIndex];
      if (!currentRoot || !previousRoot) continue;
      if (pathsOverlap(previousRoot, currentRoot) || pathsOverlap(currentRoot, previousRoot)) {
        addIssue(
          index,
          "rootPath",
          `资料源目录不能相同或互为父子目录：${sources[previousIndex]?.rootPath} 与 ${source.rootPath}`,
        );
      }
    }
  }
}

const sourcesSchema = z.array(sourceSchema).superRefine((sources, context) => {
  validateSourceTopology(
    sources,
    sources.map((source) => canonicalPathKey(source.rootPath)),
    (index, field, message) => context.addIssue({
      code: "custom",
      path: [index, field],
      message,
    }),
  );
});

export const appConfigSchema = z.object({
  schemaVersion: z.literal(1),
  sources: sourcesSchema,
  search: z.object({
    defaultSort: z.enum(["newest", "relevance", "hybrid"]),
    defaultLimit: z.number().int().min(1).max(50),
    maxEvidenceChars: z.number().int().min(2_000).max(60_000),
    synonymExpansion: z.boolean(),
    embedding: z.object({
      enabled: z.boolean(),
      provider: z.literal("ollama"),
      endpoint: z.string().url(),
      model: z.string().min(1),
      timeoutMs: z.number().int().min(500).max(120_000),
    }),
  }),
  indexing: z.object({
    automaticScan: z.boolean(),
    scanIntervalMinutes: z.number().int().min(1).max(1_440),
    concurrency: z.number().int().min(1).max(32),
  }),
  codex: z.object({
    codexPath: z.string().min(1).nullable(),
    model: z.string().min(1).nullable(),
    reasoningEffort: z.string().min(1).nullable(),
  }),
});

export interface ConfigFileFingerprint {
  mtimeMs: number;
  sizeBytes: number;
  sha256: string;
}

export interface ConfigSnapshot {
  config: AppConfig;
  fingerprint: ConfigFileFingerprint;
}

function configHash(value: string | Buffer): string {
  return createHash("sha256").update(value).digest("hex");
}

async function validateRealSourceTopology(sources: KnowledgeSourceConfig[]): Promise<void> {
  const rootKeys = await Promise.all(sources.map(async (source) => {
    try {
      return canonicalPathKey(await realpath(source.rootPath));
    } catch {
      // A temporarily offline or permission-restricted source must not block
      // unrelated settings updates. Lexical validation remains authoritative;
      // realpath only strengthens it when the directory is currently readable.
      return canonicalPathKey(source.rootPath);
    }
  }));
  const issues: string[] = [];
  validateSourceTopology(sources, rootKeys, (_index, _field, message) => issues.push(message));
  if (issues.length > 0) throw new Error(issues[0]);
}

const commonExcludes = [
  ".git",
  ".svn",
  ".hg",
  ".cursor",
  ".codex",
  ".agents",
  ".claude",
  ".windsurf",
  ".continue",
  ".aider",
  ".cline",
  ".roo",
  ".gemini",
  ".openai",
  ".github",
  ".gitlab",
  ".vscode",
  ".idea",
  ".vs",
  ".devcontainer",
  ".obsidian",
  ".aws",
  ".azure",
  ".gcloud",
  ".kube",
  ".ssh",
  ".gnupg",
  ".docker",
  "node_modules",
  "dist",
  "build",
  "temp",
  "tmp",
  "__macosx",
  ".env",
  ".env.*",
  ".npmrc",
  ".pypirc",
  ".netrc",
  "credentials.json",
  "credentials.yaml",
  "credentials.yml",
  ".credentials.json",
  "credential.json",
  "secrets.json",
  "secrets.yaml",
  "secrets.yml",
  ".secrets.json",
  "token.json",
  "token.yaml",
  "token.yml",
  "tokens.json",
  ".token.json",
  "*-credentials.json",
  "*-secrets.json",
  "*.token.json",
  "client_secret*.json",
  "client-secret*.json",
  "service-account*.json",
  "service_account*.json",
  "application_default_credentials.json",
  "config.local.*",
  "config.private.*",
  "config.secret.*",
  "settings.local.*",
  "local.settings.*",
  "id_rsa",
  "id_dsa",
  "id_ecdsa",
  "id_ed25519",
  "authorized_keys",
  "private.key",
  "private.pem",
  "server.key",
  "client.key",
];

function withDefaultExcludes(values: readonly string[]): string[] {
  const result = [...values];
  const seen = new Set(values.map((value) => value.trim().toLowerCase()).filter(Boolean));
  for (const value of commonExcludes) {
    if (seen.has(value.toLowerCase())) continue;
    result.push(value);
    seen.add(value.toLowerCase());
  }
  return result;
}

export function normalizeSourceRootPath(rootPath: string): string {
  const value = rootPath.trim();
  if (!path.isAbsolute(value)) {
    throw new Error(`资料源目录必须使用绝对路径：${rootPath}`);
  }
  return path.resolve(value);
}

export async function validateSourceRootPath(rootPath: string): Promise<string> {
  const normalized = normalizeSourceRootPath(rootPath);
  let info;
  try {
    info = await stat(normalized);
  } catch (error) {
    const code = error instanceof Error && "code" in error ? String(error.code) : "";
    if (code === "ENOENT") return normalized;
    if (code === "ENOTDIR") throw new Error(`资料源路径的上级不是目录：${normalized}`);
    // Offline, disconnected or permission-restricted absolute roots retain the
    // existing pending/unavailable retry semantics. Index discovery reports the
    // concrete failure when the source is scanned.
    return normalized;
  }
  if (!info.isDirectory()) throw new Error(`资料源路径已存在但不是目录：${normalized}`);
  return normalized;
}

export function createSourceConfig(input: {
  id: string;
  label: string;
  kind: KnowledgeSourceConfig["kind"];
  rootPath: string;
  enabled?: boolean;
}): KnowledgeSourceConfig {
  if (input.kind === "design") {
    return {
      id: input.id,
      label: input.label,
      kind: "design",
      rootPath: normalizeSourceRootPath(input.rootPath),
      enabled: input.enabled ?? true,
      includeExtensions: [
        ".docx",
        ".xlsx",
        ".xlsm",
        ".xls",
        ".pdf",
        ".xmind",
        ".md",
        ".markdown",
        ".txt",
        ".html",
        ".json",
        ".yaml",
        ".yml",
      ],
      excludeDirectoryNames: [...commonExcludes],
      maxFileBytes: 128 * 1024 * 1024,
    };
  }
  return {
    id: input.id,
    label: input.label,
    kind: "table",
    rootPath: normalizeSourceRootPath(input.rootPath),
    enabled: input.enabled ?? true,
    includeExtensions: [".xlsx", ".xlsm", ".xls", ".csv", ".pdf"],
    excludeDirectoryNames: [...commonExcludes],
    maxFileBytes: 128 * 1024 * 1024,
  };
}

export function createDefaultConfig(): AppConfig {
  return {
    schemaVersion: 1,
    sources: [],
    search: {
      defaultSort: "newest",
      defaultLimit: 12,
      maxEvidenceChars: 24_000,
      synonymExpansion: true,
      embedding: {
        enabled: false,
        provider: "ollama",
        endpoint: "http://127.0.0.1:11434/api/embed",
        model: "embeddinggemma",
        timeoutMs: 30_000,
      },
    },
    indexing: {
      automaticScan: true,
      scanIntervalMinutes: 10,
      concurrency: 16,
    },
    codex: {
      codexPath: null,
      model: null,
      reasoningEffort: null,
    },
  };
}

export class ConfigStore {
  readonly configDir: string;
  readonly dataDir: string;
  readonly configPath: string;

  constructor(options: { configDir?: string; dataDir?: string } = {}) {
    this.configDir = options.configDir ?? resolveConfigDir();
    this.dataDir = options.dataDir ?? resolveDataDir();
    this.configPath = path.join(this.configDir, "config.json");
  }

  private async ensureDirectories(): Promise<void> {
    await Promise.all([
      mkdir(this.configDir, { recursive: true }),
      mkdir(this.dataDir, { recursive: true }),
    ]);
  }

  private async readStableFile(): Promise<{ raw: Buffer; fingerprint: ConfigFileFingerprint }> {
    for (let attempt = 0; attempt < 3; attempt += 1) {
      const before = await stat(this.configPath);
      const raw = await readFile(this.configPath);
      const after = await stat(this.configPath);
      if (before.size === after.size && Math.abs(before.mtimeMs - after.mtimeMs) < 1) {
        return {
          raw,
          fingerprint: {
            mtimeMs: after.mtimeMs,
            sizeBytes: raw.byteLength,
            sha256: configHash(raw),
          },
        };
      }
    }
    throw new Error("配置文件在读取期间持续变化，请稍后重试");
  }

  async load(): Promise<AppConfig> {
    return (await this.loadSnapshot()).config;
  }

  async loadSnapshot(): Promise<ConfigSnapshot> {
    await this.ensureDirectories();
    try {
      const { raw, fingerprint } = await this.readStableFile();
      const parsed = appConfigSchema.parse(JSON.parse(raw.toString("utf8")));
      return { config: await this.validate(parsed), fingerprint };
    } catch (error) {
      const isMissing = error instanceof Error && "code" in error && error.code === "ENOENT";
      if (!isMissing) throw error;
      const initial = createDefaultConfig();
      return this.saveSnapshot(initial);
    }
  }

  async save(config: AppConfig): Promise<AppConfig> {
    return (await this.saveSnapshot(config)).config;
  }

  async saveSnapshot(config: AppConfig): Promise<ConfigSnapshot> {
    const parsed = await this.validate(config);
    await mkdir(this.configDir, { recursive: true });
    const serialized = `${JSON.stringify(parsed, null, 2)}\n`;
    await writeFileAtomic(this.configPath, serialized, {
      encoding: "utf8",
      mode: 0o600,
    });
    const fileStat = await stat(this.configPath);
    return {
      config: parsed,
      fingerprint: {
        mtimeMs: fileStat.mtimeMs,
        sizeBytes: Buffer.byteLength(serialized),
        sha256: configHash(serialized),
      },
    };
  }

  async fingerprint(): Promise<ConfigFileFingerprint> {
    await this.ensureDirectories();
    return (await this.readStableFile()).fingerprint;
  }

  async validate(config: AppConfig): Promise<AppConfig> {
    const parsed = appConfigSchema.parse(config);
    const normalized: AppConfig = {
      ...parsed,
      sources: await Promise.all(parsed.sources.map(async (source) => ({
        ...source,
        rootPath: await validateSourceRootPath(source.rootPath),
        excludeDirectoryNames: withDefaultExcludes(source.excludeDirectoryNames),
      }))),
    };
    await validateRealSourceTopology(normalized.sources);
    return normalized;
  }
}
