import { readFile } from "node:fs/promises";
import path from "node:path";
import chardet from "chardet";
import * as cheerio from "cheerio";
import iconv from "iconv-lite";
import { classifySection } from "../classifier.js";
import { stripControlCharacters } from "../text.js";
import type { ExtractedBlock, ExtractedDocument } from "../types.js";
import { blocksFromHtml } from "./html.js";

function decodeBuffer(buffer: Buffer): { text: string; encoding: string } {
  const detected = chardet.detect(buffer);
  const candidates = [detected, "utf8", "gb18030", "gbk"].filter((value): value is string => Boolean(value));
  for (const encoding of candidates) {
    if (!iconv.encodingExists(encoding)) continue;
    const text = stripControlCharacters(iconv.decode(buffer, encoding));
    if (!text.includes("�")) return { text, encoding };
  }
  return { text: stripControlCharacters(buffer.toString("utf8")), encoding: "utf8-fallback" };
}

function lineBlocks(text: string, markdown: boolean): ExtractedBlock[] {
  const lines = text.replace(/\r\n/g, "\n").split("\n");
  const blocks: ExtractedBlock[] = [];
  const headingPath: string[] = [];
  let ordinal = 0;
  let buffer: string[] = [];
  let startLine = 1;

  const flush = (endLine: number) => {
    const content = buffer.join("\n").trim();
    if (content) {
      blocks.push({
        ordinal,
        text: content,
        headingPath: [...headingPath],
        sectionType: classifySection(headingPath, content),
        locator: `行 ${startLine}-${endLine}`,
        metadata: { lineStart: startLine, lineEnd: endLine },
      });
      ordinal += 1;
    }
    buffer = [];
  };

  lines.forEach((line, index) => {
    const lineNumber = index + 1;
    const heading = markdown ? /^(#{1,6})\s+(.+?)\s*$/.exec(line) : null;
    if (heading) {
      flush(lineNumber - 1);
      const level = heading[1]?.length ?? 1;
      headingPath.splice(level - 1);
      headingPath[level - 1] = heading[2] ?? "";
      startLine = lineNumber + 1;
      return;
    }
    if (!line.trim()) {
      flush(lineNumber - 1);
      startLine = lineNumber + 1;
      return;
    }
    if (buffer.length === 0) startLine = lineNumber;
    buffer.push(line);
  });
  flush(lines.length);
  return blocks;
}

export async function extractTextDocument(filePath: string, inputBuffer?: Buffer): Promise<ExtractedDocument> {
  const buffer = inputBuffer ?? await readFile(filePath);
  const { text, encoding } = decodeBuffer(buffer);
  const extension = path.extname(filePath).toLowerCase();
  let blocks: ExtractedBlock[];
  if (extension === ".html" || extension === ".htm") {
    const $ = cheerio.load(text);
    $("script,style,noscript").remove();
    blocks = blocksFromHtml($.html(), "HTML 段落");
  } else {
    blocks = lineBlocks(text, extension === ".md" || extension === ".markdown");
  }
  return {
    title: path.basename(filePath, extension),
    blocks,
    embeddedModifiedAt: null,
    warnings: encoding.toLowerCase().startsWith("utf") ? [] : [`文本编码检测为 ${encoding}`],
    needsOcr: false,
  };
}
