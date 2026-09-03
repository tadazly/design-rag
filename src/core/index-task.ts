import { stat, readFile } from "node:fs/promises";
import type { Stats } from "node:fs";
import { chunkBlocks } from "./chunker.js";
import { makeFamilyKey } from "./classifier.js";
import { resolveEffectiveDate } from "./dates.js";
import { extractDocument, UnsupportedFormatError } from "./extractors/index.js";
import { canonicalPathKey } from "./paths.js";
import { sha256 } from "./text.js";
import type { DocumentDraft, FileCandidate } from "./types.js";

export interface IndexTaskInput {
  candidate: FileCandidate;
  existingContentHash: string | null;
  full: boolean;
}

export type IndexTaskResult =
  | { kind: "unchanged"; contentHash: string }
  | { kind: "draft"; draft: DocumentDraft };

export interface IndexWorkerRequest {
  id: number;
  input: IndexTaskInput;
}

export type IndexWorkerResponse =
  | { id: number; ok: true; result: IndexTaskResult }
  | { id: number; ok: false; code: string; message: string; stack?: string };

function assertStableFile(before: Stats, after: Stats, action: string): void {
  if (before.size !== after.size || Math.abs(before.mtimeMs - after.mtimeMs) >= 1) {
    throw new Error(`文件在${action}期间发生变化，请在下次增量扫描重试`);
  }
}

export function indexTaskErrorCode(error: unknown): string {
  return error instanceof UnsupportedFormatError ? error.code : "extract_failed";
}

/**
 * 文件读取、解析与分块都在 SQLite 事务之外完成。该函数既可由 worker
 * 调用，也可在 worker 不可用时作为可测试的独立执行单元调用。
 */
export async function processIndexTask(input: IndexTaskInput): Promise<IndexTaskResult> {
  const { candidate } = input;
  const before = await stat(candidate.absolutePath);
  const rawBuffer = await readFile(candidate.absolutePath);
  const rawHash = sha256(rawBuffer);

  if (!input.full && input.existingContentHash === rawHash) {
    const after = await stat(candidate.absolutePath);
    assertStableFile(before, after, "读取");
    return { kind: "unchanged", contentHash: rawHash };
  }

  const extracted = await extractDocument(candidate, rawBuffer);
  const after = await stat(candidate.absolutePath);
  assertStableFile(before, after, "提取");
  const chunks = chunkBlocks(extracted.title, candidate.relativePath, extracted.blocks);
  if (chunks.length === 0 && !extracted.needsOcr) {
    throw new Error("文档未提取到可索引内容");
  }
  const date = resolveEffectiveDate({
    absolutePath: candidate.absolutePath,
    contentSample: extracted.blocks
      .filter((block) => block.sectionType === "version_history" && (
        candidate.sourceKind !== "table"
        || block.headingPath.some((heading) => /^(?:版本修改记录|版本记录|修改记录|changelog|revision history)$/i.test(heading.trim()))
      ))
      .slice(0, 300)
      .map((block) => `${block.headingPath.join(" / ")} ${block.text}`)
      .join("\n"),
    embeddedModifiedAt: extracted.embeddedModifiedAt,
    filesystemMtimeMs: candidate.filesystemMtimeMs,
    ...(extracted.dateEvidence !== undefined ? { dateEvidence: extracted.dateEvidence } : {}),
  });
  const family = makeFamilyKey(extracted.title);
  const documentId = `doc_${sha256(canonicalPathKey(candidate.absolutePath)).slice(0, 24)}`;
  return {
    kind: "draft",
    draft: {
      id: documentId,
      candidate,
      title: extracted.title,
      familyKey: family.key,
      familyConfidence: family.confidence,
      contentHash: rawHash,
      date,
      chunks,
      warnings: extracted.warnings,
      needsOcr: extracted.needsOcr,
    },
  };
}
