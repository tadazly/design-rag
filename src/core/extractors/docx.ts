import path from "node:path";
import JSZip from "jszip";
import mammoth from "mammoth";
import { XMLParser } from "fast-xml-parser";
import { readFile } from "node:fs/promises";
import type { ExtractedDocument } from "../types.js";
import { collectBlockDateEvidence } from "../dates.js";
import { blocksFromHtml } from "./html.js";

interface CoreProperties {
  [key: string]: unknown;
}

function findModifiedDate(value: unknown): string | null {
  if (Array.isArray(value)) {
    for (const item of value) {
      const found = findModifiedDate(item);
      if (found) return found;
    }
  }
  if (value && typeof value === "object") {
    const object = value as CoreProperties;
    for (const [key, item] of Object.entries(object)) {
      if (/modified$/i.test(key)) {
        if (typeof item === "string" && Number.isFinite(Date.parse(item))) return item;
        if (item && typeof item === "object") {
          const text = (item as Record<string, unknown>)["#text"];
          if (typeof text === "string" && Number.isFinite(Date.parse(text))) return text;
        }
      }
    }
    for (const item of Object.values(object)) {
      const found = findModifiedDate(item);
      if (found) return found;
    }
  }
  return null;
}

async function readCoreModified(filePath: string, inputBuffer?: Buffer): Promise<string | null> {
  try {
    const zip = await JSZip.loadAsync(inputBuffer ?? await readFile(filePath));
    const entry = zip.file("docProps/core.xml");
    if (!entry) return null;
    const xml = await entry.async("string");
    const parsed = new XMLParser({ ignoreAttributes: false }).parse(xml) as unknown;
    return findModifiedDate(parsed);
  } catch {
    return null;
  }
}

export async function extractDocx(filePath: string, inputBuffer?: Buffer): Promise<ExtractedDocument> {
  const result = await mammoth.convertToHtml(
    inputBuffer ? { buffer: inputBuffer } : { path: filePath },
    {
      includeDefaultStyleMap: true,
      styleMap: [
        "p[style-name='Title'] => h1:fresh",
        "p[style-name='标题 1'] => h1:fresh",
        "p[style-name='标题 2'] => h2:fresh",
        "p[style-name='标题 3'] => h3:fresh",
      ],
    },
  );
  const blocks = blocksFromHtml(result.value);
  const warnings = result.messages.map((message) => `${message.type}: ${message.message}`);
  if (blocks.length === 0) warnings.push("DOCX 未提取到可索引文本");
  return {
    title: path.basename(filePath, path.extname(filePath)),
    blocks,
    embeddedModifiedAt: await readCoreModified(filePath, inputBuffer),
    warnings,
    needsOcr: blocks.length === 0,
    dateEvidence: collectBlockDateEvidence(blocks),
  };
}
