import { existsSync } from "node:fs";
import { createHash } from "node:crypto";
import { homedir } from "node:os";
import path from "node:path";

export function resolveConfigDir(): string {
  const override = process.env.DESIGN_RAG_CONFIG_DIR?.trim();
  if (override) return path.resolve(override);
  if (process.platform === "darwin") {
    const current = path.join(homedir(), "Library", "Application Support", "DesignRag", "config");
    const previous = path.join(homedir(), "Library", "Application Support", "design-rag", "config");
    return existsSync(current) || !existsSync(previous) ? current : previous;
  }
  const base = process.env.APPDATA?.trim() || path.join(homedir(), ".config");
  const current = path.join(base, "DesignRag");
  const previous = path.join(base, "design-rag");
  return existsSync(current) || !existsSync(previous) ? current : previous;
}

export function resolveDataDir(): string {
  const override = process.env.DESIGN_RAG_DATA_DIR?.trim();
  if (override) return path.resolve(override);
  if (process.platform === "darwin") {
    const current = path.join(homedir(), "Library", "Application Support", "DesignRag", "data");
    const previous = path.join(homedir(), "Library", "Application Support", "design-rag", "data");
    return existsSync(current) || !existsSync(previous) ? current : previous;
  }
  const base = process.env.LOCALAPPDATA?.trim() || path.join(homedir(), ".local", "share");
  const current = path.join(base, "DesignRag");
  const previous = path.join(base, "design-rag");
  return existsSync(current) || !existsSync(previous) ? current : previous;
}

export function canonicalPathKey(value: string): string {
  let resolved = path.resolve(value).normalize("NFC");
  if (process.platform === "win32") {
    resolved = resolved.replace(/^([A-Z]):/, (_, drive: string) => `${drive.toLowerCase()}:`);
    return resolved.toLowerCase();
  }
  return resolved;
}

export function isPathInside(rootPath: string, candidatePath: string): boolean {
  const root = canonicalPathKey(rootPath);
  const candidate = canonicalPathKey(candidatePath);
  const relative = path.relative(root, candidate);
  return relative === "" || (!relative.startsWith("..") && !path.isAbsolute(relative));
}

export function sourceIndexIdentity(source: { kind: string; rootPath: string }): string {
  return createHash("sha256")
    .update(`v1\0${source.kind}\0${canonicalPathKey(source.rootPath)}`)
    .digest("hex");
}

export function sanitizeFilename(value: string): string {
  return value.replace(/[<>:"/\\|?*\u0000-\u001f]/g, "_").slice(0, 120);
}
