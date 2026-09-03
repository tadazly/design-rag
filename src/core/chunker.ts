import { classifySection } from "./classifier.js";
import { normalizeText, sha256 } from "./text.js";
import type { ChunkDraft, ExtractedBlock } from "./types.js";

const SENTENCE_BOUNDARY = /(?<=[。！？!?；;\n])\s*/u;

function splitLargeText(text: string, targetSize: number, overlap: number): string[] {
  if (text.length <= targetSize) return [text];
  const sentences = text.split(SENTENCE_BOUNDARY).filter(Boolean);
  const chunks: string[] = [];
  let current = "";
  for (const sentence of sentences) {
    if (current.length > 0 && current.length + sentence.length > targetSize) {
      chunks.push(current.trim());
      current = `${current.slice(Math.max(0, current.length - overlap))}${sentence}`;
    } else {
      current += sentence;
    }
    while (current.length > targetSize * 1.5) {
      chunks.push(current.slice(0, targetSize).trim());
      current = current.slice(Math.max(0, targetSize - overlap));
    }
  }
  if (current.trim()) chunks.push(current.trim());
  return chunks;
}

export function chunkBlocks(
  title: string,
  relativePath: string,
  blocks: ExtractedBlock[],
  options: { targetSize?: number; overlap?: number } = {},
): ChunkDraft[] {
  const targetSize = options.targetSize ?? 1_000;
  const overlap = options.overlap ?? 120;
  const drafts: ChunkDraft[] = [];
  let ordinal = 0;

  for (const block of blocks) {
    const clean = block.text.replace(/\r\n/g, "\n").replace(/\n{3,}/g, "\n\n").trim();
    if (!clean) continue;
    const sectionType = block.sectionType ?? classifySection(block.headingPath, clean);
    for (const part of splitLargeText(clean, targetSize, overlap)) {
      if (!normalizeText(part)) continue;
      drafts.push({
        ordinal,
        text: part,
        headingPath: block.headingPath,
        sectionType,
        locator: block.locator,
        contentHash: sha256(part),
      });
      ordinal += 1;
    }
  }
  return drafts;
}
