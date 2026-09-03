import { readFile } from "node:fs/promises";
import path from "node:path";
import { PDFParse } from "pdf-parse";
import { classifySection } from "../classifier.js";
import type { ExtractedDocument } from "../types.js";

export async function extractPdf(filePath: string, inputBuffer?: Buffer): Promise<ExtractedDocument> {
  const parser = new PDFParse({ data: new Uint8Array(inputBuffer ?? await readFile(filePath)) });
  try {
    const [textResult, infoResult] = await Promise.all([
      parser.getText(),
      parser.getInfo().catch(() => null),
    ]);
    const blocks = textResult.pages
      .map((page, index) => ({ page, index }))
      .filter(({ page }) => page.text.trim().length > 0)
      .map(({ page, index }) => ({
        ordinal: index,
        text: page.text.trim(),
        headingPath: [`第 ${page.num} 页`],
        sectionType: classifySection([], page.text),
        locator: `第 ${page.num} 页`,
        metadata: { page: page.num },
      }));
    const dateNode = infoResult?.getDateNode();
    const modified = dateNode?.ModDate;
    const embeddedModifiedAt = modified instanceof Date ? modified.toISOString() : modified ? String(modified) : null;
    return {
      title: infoResult?.info?.Title || path.basename(filePath, path.extname(filePath)),
      blocks,
      embeddedModifiedAt,
      warnings: blocks.length === 0 ? ["PDF 无文本层，需要 OCR"] : [],
      needsOcr: blocks.length === 0,
    };
  } finally {
    await parser.destroy();
  }
}
