import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { lstat, mkdir, mkdtemp, realpath, stat, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import fg from "fast-glob";

export const SOURCE_INVENTORY_ALGORITHM = "sha256-source-config-canonical-path-size-mtime-ms-v1";
export const GO_COLD_INDEX_OBJECTIVE_MAX_MS = 60_000;
export const GO_COLD_INDEX_RECOMMENDED_MAX_MS = 90_000;

function canonicalPathKey(value) {
  const normalized = path.resolve(value).normalize("NFC").replaceAll("\\", "/");
  return process.platform === "win32" ? normalized.toLowerCase() : normalized;
}

function isPathInside(rootPath, candidatePath) {
  const relative = path.relative(rootPath, candidatePath);
  return relative !== "" && relative !== ".." && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative);
}

function isTemporaryFile(filePath) {
  const name = path.basename(filePath);
  return name.startsWith("~$") || /^~WRL.*\.tmp$/i.test(name) || /\.(?:tmp|bak)$/i.test(name);
}

function sourceIgnorePatterns(source) {
  return source.excludeDirectoryNames.flatMap((name) => [...new Set([name, name.toLowerCase(), name.toUpperCase()])]
    .flatMap((variant) => [`**/${variant}`, `**/${variant}/**`]));
}

function stableStrings(values) {
  return [...values].map((value) => String(value).normalize("NFC")).sort();
}

export function fingerprintSourceInventory(sourceDescriptors, fileEntries) {
  const hash = createHash("sha256");
  const descriptors = [...sourceDescriptors].sort((left, right) => {
    const leftKey = `${left.sourceId}\0${left.sourceKind}\0${left.canonicalRoot}`;
    const rightKey = `${right.sourceId}\0${right.sourceKind}\0${right.canonicalRoot}`;
    return leftKey < rightKey ? -1 : leftKey > rightKey ? 1 : 0;
  });
  for (const source of descriptors) {
    hash.update([
      "source",
      source.sourceId.normalize("NFC"),
      source.sourceKind,
      source.canonicalRoot,
      stableStrings(source.includeExtensions).join(","),
      stableStrings(source.excludeDirectoryNames).join(","),
      String(source.maxFileBytes),
    ].join("\0"));
    hash.update("\n");
  }
  const entries = [...fileEntries].sort((left, right) => {
    const leftKey = `${left.sourceId}\0${left.sourceKind}\0${left.canonicalPath}`;
    const rightKey = `${right.sourceId}\0${right.sourceKind}\0${right.canonicalPath}`;
    return leftKey < rightKey ? -1 : leftKey > rightKey ? 1 : 0;
  });
  for (const entry of entries) {
    hash.update([
      "file",
      entry.sourceId.normalize("NFC"),
      entry.sourceKind,
      entry.canonicalPath,
      String(entry.sizeBytes),
      String(entry.mtimeMs),
    ].join("\0"));
    hash.update("\n");
  }
  return hash.digest("hex");
}

async function captureSource(source) {
  const rootStat = await stat(source.rootPath);
  if (!rootStat.isDirectory()) throw new Error(`资料源不是目录：${source.rootPath}`);
  const lexicalRoot = path.resolve(source.rootPath);
  const canonicalRoot = canonicalPathKey(await realpath(lexicalRoot));
  const extensionSet = new Set(source.includeExtensions.map((extension) => extension.toLowerCase()));
  const discovered = await fg("**/*", {
    cwd: lexicalRoot,
    absolute: true,
    onlyFiles: true,
    dot: true,
    followSymbolicLinks: false,
    suppressErrors: false,
    ignore: sourceIgnorePatterns(source),
  });
  const fileEntries = [];
  for (let offset = 0; offset < discovered.length; offset += 128) {
    const batch = await Promise.all(discovered.slice(offset, offset + 128).map(async (discoveredPath) => {
      const absolutePath = path.resolve(discoveredPath);
      if (!isPathInside(lexicalRoot, absolutePath) || isTemporaryFile(absolutePath)) return null;
      if (!extensionSet.has(path.extname(absolutePath).toLowerCase())) return null;
      const fileStat = await lstat(absolutePath);
      if (!fileStat.isFile() || fileStat.isSymbolicLink() || fileStat.size > source.maxFileBytes) return null;
      const relativePath = path.relative(lexicalRoot, absolutePath);
      return {
        sourceId: source.id,
        sourceKind: source.kind,
        canonicalPath: canonicalPathKey(path.resolve(canonicalRoot, relativePath)),
        sizeBytes: fileStat.size,
        mtimeMs: Math.trunc(fileStat.mtimeMs),
      };
    }));
    fileEntries.push(...batch.filter(Boolean));
  }
  const descriptor = {
    sourceId: source.id,
    sourceKind: source.kind,
    canonicalRoot,
    includeExtensions: source.includeExtensions,
    excludeDirectoryNames: source.excludeDirectoryNames,
    maxFileBytes: source.maxFileBytes,
  };
  return {
    descriptor,
    fileEntries,
    summary: {
      sourceId: source.id,
      sourceKind: source.kind,
      canonicalRoot,
      fileCount: fileEntries.length,
      totalBytes: fileEntries.reduce((total, entry) => total + entry.sizeBytes, 0),
    },
  };
}

export async function captureSourceInventory(sources) {
  const capturedAt = new Date().toISOString();
  const captures = [];
  for (const source of sources) captures.push(await captureSource(source));
  const descriptors = captures.map((capture) => capture.descriptor);
  const entries = captures.flatMap((capture) => capture.fileEntries);
  return {
    algorithm: SOURCE_INVENTORY_ALGORITHM,
    fingerprint: fingerprintSourceInventory(descriptors, entries),
    fileCount: entries.length,
    sourceCount: descriptors.length,
    totalBytes: entries.reduce((total, entry) => total + entry.sizeBytes, 0),
    capturedAt,
    sources: captures.map((capture) => capture.summary),
  };
}

export function sameSourceInventory(left, right) {
  return left.algorithm === right.algorithm
    && left.fingerprint === right.fingerprint
    && left.fileCount === right.fileCount
    && left.sourceCount === right.sourceCount;
}

export function goColdIndexWithinRecommendation(wallMs) {
  return Number.isFinite(wallMs) && wallMs >= 0 && wallMs <= GO_COLD_INDEX_RECOMMENDED_MAX_MS;
}

export function goColdIndexUnderOneMinute(wallMs) {
  return Number.isFinite(wallMs) && wallMs >= 0 && wallMs < GO_COLD_INDEX_OBJECTIVE_MAX_MS;
}

async function selfTest() {
  const descriptors = [{
    sourceId: "plans",
    sourceKind: "design",
    canonicalRoot: "c:/资料",
    includeExtensions: [".md", ".docx"],
    excludeDirectoryNames: [".codex", ".cursor"],
    maxFileBytes: 1024,
  }];
  const entries = [
    { sourceId: "plans", sourceKind: "design", canonicalPath: "c:/资料/b.md", sizeBytes: 2, mtimeMs: 20 },
    { sourceId: "plans", sourceKind: "design", canonicalPath: "c:/资料/a.md", sizeBytes: 1, mtimeMs: 10 },
  ];
  const first = fingerprintSourceInventory(descriptors, entries);
  assert.equal(first, fingerprintSourceInventory(descriptors, [...entries].reverse()));
  assert.notEqual(first, fingerprintSourceInventory(descriptors, [{ ...entries[0], mtimeMs: 21 }, entries[1]]));
  assert.equal(goColdIndexWithinRecommendation(89_999.9), true);
  assert.equal(goColdIndexWithinRecommendation(90_000), true);
  assert.equal(goColdIndexWithinRecommendation(90_000.1), false);
  assert.equal(goColdIndexUnderOneMinute(59_999.9), true);
  assert.equal(goColdIndexUnderOneMinute(60_000), false);
  const fixtureRoot = await mkdtemp(path.join(os.tmpdir(), "drag-source-inventory-"));
  await Promise.all([
    mkdir(path.join(fixtureRoot, ".draft"), { recursive: true }),
    mkdir(path.join(fixtureRoot, ".codex"), { recursive: true }),
  ]);
  await Promise.all([
    writeFile(path.join(fixtureRoot, "normal.md"), "normal", "utf8"),
    writeFile(path.join(fixtureRoot, ".draft", "business.md"), "business", "utf8"),
    writeFile(path.join(fixtureRoot, ".codex", "instructions.md"), "tool", "utf8"),
    writeFile(path.join(fixtureRoot, "config.local.json"), "{}", "utf8"),
    writeFile(path.join(fixtureRoot, "ignored.bin"), "binary", "utf8"),
  ]);
  const captured = await captureSourceInventory([{
    id: "fixture",
    label: "fixture",
    kind: "design",
    rootPath: fixtureRoot,
    enabled: true,
    includeExtensions: [".md", ".json"],
    excludeDirectoryNames: [".codex", "config.local.*"],
    maxFileBytes: 1024,
  }]);
  assert.equal(captured.fileCount, 2);
  assert.equal(captured.sources[0]?.fileCount, 2);
  process.stdout.write(`${JSON.stringify({ status: "PASS", algorithm: SOURCE_INVENTORY_ALGORITHM, goColdIndexObjectiveMaxMs: GO_COLD_INDEX_OBJECTIVE_MAX_MS, goColdIndexRecommendedMaxMs: GO_COLD_INDEX_RECOMMENDED_MAX_MS, fixtureFileCount: captured.fileCount, fixtureRoot })}\n`);
}

const isMain = process.argv[1] && pathToFileURL(path.resolve(process.argv[1])).href === import.meta.url;
if (isMain && process.argv.includes("--self-test")) await selfTest();
