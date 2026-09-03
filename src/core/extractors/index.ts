import { open } from "node:fs/promises";
import path from "node:path";
import type { FileCandidate, ExtractedDocument } from "../types.js";

export class UnsupportedFormatError extends Error {
  readonly code = "unsupported_format";
}

async function readMagic(filePath: string): Promise<Buffer> {
  const handle = await open(filePath, "r");
  try {
    const buffer = Buffer.alloc(8);
    const { bytesRead } = await handle.read(buffer, 0, buffer.length, 0);
    return buffer.subarray(0, bytesRead);
  } finally {
    await handle.close();
  }
}

export async function extractDocument(candidate: FileCandidate, inputBuffer?: Buffer): Promise<ExtractedDocument> {
  const extension = candidate.extension.toLowerCase();
  if (path.basename(candidate.absolutePath).startsWith("~$")) {
    throw new UnsupportedFormatError("Office 锁文件不进入索引");
  }
  const magic = await readMagic(candidate.absolutePath);
  const isZip = magic[0] === 0x50 && magic[1] === 0x4b;

  if (extension === ".docx") {
    const { extractDocx } = await import("./docx.js");
    return extractDocx(candidate.absolutePath, inputBuffer);
  }
  if ([".xlsx", ".xlsm", ".xls", ".csv"].includes(extension) || (extension === ".xls" && isZip)) {
    const { extractSpreadsheet } = await import("./spreadsheet.js");
    return extractSpreadsheet(candidate.absolutePath, inputBuffer);
  }
  if (extension === ".pdf") {
    const { extractPdf } = await import("./pdf.js");
    return extractPdf(candidate.absolutePath, inputBuffer);
  }
  if (extension === ".xmind") {
    const { extractXmind } = await import("./xmind.js");
    return extractXmind(candidate.absolutePath, inputBuffer);
  }
  if ([".md", ".markdown", ".txt", ".html", ".htm", ".json", ".yaml", ".yml"].includes(extension)) {
    const { extractTextDocument } = await import("./text.js");
    return extractTextDocument(candidate.absolutePath, inputBuffer);
  }
  if (extension === ".doc") {
    throw new UnsupportedFormatError("旧版 OLE .doc 尚不支持；请转换为 DOCX 或配置 LibreOffice 转换器");
  }
  throw new UnsupportedFormatError(`不支持的文档格式：${extension || "无扩展名"}`);
}
