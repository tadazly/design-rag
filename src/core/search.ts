import type {
  Citation,
  DateSource,
  RetrievalBundle,
  RetrievalEvidence,
  RetrievalRequest,
  SearchExcerpt,
  SearchHit,
  SearchRequest,
  SearchResponse,
  SearchSort,
  SectionType,
} from "../shared/contracts.js";
import { createHash } from "node:crypto";
import path from "node:path";
import { expandQueryTerms, queryConceptGroups } from "./classifier.js";
import { IndexDatabase, type LexicalCandidateRow } from "./database.js";
import { cosineSimilarity, OllamaEmbeddingProvider } from "./embeddings.js";
import { sourceIndexIdentity } from "./paths.js";
import { cjkSearchTerms, escapeFtsToken, highlightTerms, normalizeText, summarizeText } from "./text.js";
import type { AppConfig } from "../shared/contracts.js";

interface ScoredCandidate {
  row: LexicalCandidateRow;
  score: number;
  semanticScore: number;
  matchedTerms: string[];
}

function safeHeadingPath(value: string): string[] {
  try {
    const parsed = JSON.parse(value) as unknown;
    return Array.isArray(parsed) ? parsed.map(String) : [];
  } catch {
    return [];
  }
}

function matchesFilters(row: LexicalCandidateRow, request: SearchRequest): boolean {
  if (request.sourceIds?.length && !request.sourceIds.includes(row.source_id)) return false;
  if (request.sourceKinds?.length && !request.sourceKinds.includes(row.source_kind)) return false;
  if (request.sectionTypes?.length && !request.sectionTypes.includes(row.section_type)) return false;
  if (request.extensions?.length && !request.extensions.map((value) => value.toLowerCase()).includes(row.extension.toLowerCase())) return false;
  if (request.updatedAfter && row.effective_updated_at_ms < Date.parse(request.updatedAfter)) return false;
  if (request.updatedBefore && row.effective_updated_at_ms > Date.parse(request.updatedBefore)) return false;
  return true;
}

function matchesConfiguredSource(
  row: Pick<LexicalCandidateRow, "source_id" | "source_identity">,
  sourcesById: ReadonlyMap<string, AppConfig["sources"][number]>,
): boolean {
  const source = sourcesById.get(row.source_id);
  return Boolean(source && source.enabled && row.source_identity === sourceIndexIdentity(source));
}

function scoreCandidate(row: LexicalCandidateRow, terms: string[], semanticScore: number): ScoredCandidate {
  const title = normalizeText(row.title);
  const heading = normalizeText(safeHeadingPath(row.heading_path_json).join(" "));
  const relativePath = normalizeText(row.relative_path);
  const text = normalizeText(row.text);
  const matchedTerms = terms.filter((term) => [title, heading, relativePath, text].some((value) => value.includes(term)));
  const coverage = matchedTerms.length / Math.max(1, terms.length);
  let score = 0.18 + coverage * 0.3;
  for (const term of matchedTerms) {
    if (title === term) score += 0.24;
    else if (title.includes(term)) score += 0.16;
    if (heading.includes(term)) score += 0.1;
    if (relativePath.includes(term)) score += 0.08;
    if (text.includes(term)) score += 0.04;
  }
  score += 1 / (1 + Math.max(0, Math.abs(row.lexical_rank))) * 0.16;
  if (/【\s*复用\s*】/.test(row.title) || /复用/.test(row.relative_path)) score += 0.08;
  if (semanticScore > 0) score = score * 0.72 + semanticScore * 0.28;
  return { row, score: Math.min(1, score), semanticScore, matchedTerms };
}

interface ExcerptSlice {
  start: number;
  end: number;
}

interface SpreadsheetCitationScope {
  version: 1;
  locator: string;
  columns: string[];
  headerSlice?: ExcerptSlice;
  rowSlice?: ExcerptSlice;
}

interface ExcerptProjection {
  text: string;
  locator: string;
  scope?: SpreadsheetCitationScope;
}

interface SearchCandidateScope {
  documentIds: readonly string[];
  chunksPerDocument: number;
}

const spreadsheetLocator = /^(.*)!([A-Z]+)(\d+):([A-Z]+)(\d+)$/;
const spreadsheetRowLine = /^行\s+(\d+)\s*\|\s*(.*)$/;
const citationIdPrefix = "DRAG:";

function columnNumber(value: string): number {
  let result = 0;
  for (const character of value) result = result * 26 + character.charCodeAt(0) - 64;
  return result;
}

function columnName(value: number): string {
  if (!Number.isSafeInteger(value) || value < 1) throw new Error("引用范围无效或已损坏：列编号非法");
  let remaining = value;
  let result = "";
  while (remaining > 0) {
    remaining -= 1;
    result = String.fromCharCode(65 + (remaining % 26)) + result;
    remaining = Math.floor(remaining / 26);
  }
  return result;
}

function spreadsheetSegmentColumn(segment: string): string | null {
  return /^([A-Z]+)(?:\d+)?(?:\[[^\]]*\])?=/.exec(segment.trim())?.[1] ?? null;
}

function uniqueNormalizedTerms(terms: readonly string[]): string[] {
  return [...new Set(terms.map(normalizeText).filter((term) => term.length >= 2))]
    .map((term, index) => ({ term, index }))
    .sort((left, right) => {
      const leftNumeric = /^\d+$/.test(left.term);
      const rightNumeric = /^\d+$/.test(right.term);
      if (leftNumeric !== rightNumeric) return leftNumeric ? 1 : -1;
      return Array.from(right.term).length - Array.from(left.term).length || left.index - right.index;
    })
    .map(({ term }) => term);
}

function scalarSafeUtf16Slice(value: string, requestedStart: number, requestedEnd: number): string {
  let start = requestedStart;
  let end = requestedEnd;
  const isHigh = (unit: number): boolean => unit >= 0xD800 && unit <= 0xDBFF;
  const isLow = (unit: number): boolean => unit >= 0xDC00 && unit <= 0xDFFF;
  if (start > 0 && start < value.length && isLow(value.charCodeAt(start)) && isHigh(value.charCodeAt(start - 1))) {
    start += 1;
  }
  if (end > start && end < value.length && isHigh(value.charCodeAt(end - 1)) && isLow(value.charCodeAt(end))) {
    end -= 1;
  }
  if (end < start) return "";
  return value.slice(start, end);
}

function renderExcerptSlice(value: string, slice: ExcerptSlice): string {
  if (!Number.isInteger(slice.start) || !Number.isInteger(slice.end)
    || slice.start < 0 || slice.end < slice.start || slice.end > value.length) {
    throw new Error("引用范围无效或已损坏：文本切片越界");
  }
  const prefix = slice.start > 0 ? "…" : "";
  const suffix = slice.end < value.length ? "…" : "";
  return `${prefix}${scalarSafeUtf16Slice(value, slice.start, slice.end).trim()}${suffix}`;
}

function excerptAroundTermsWithSlice(
  value: string,
  terms: readonly string[],
  maxLength: number,
): { text: string; slice: ExcerptSlice } {
  const limit = Math.max(1, Math.trunc(maxLength));
  if (value.length <= limit) return { text: value, slice: { start: 0, end: value.length } };
  const lower = value.toLowerCase();
  let position = -1;
  for (const term of terms) {
    const found = lower.indexOf(term.toLowerCase());
    if (found >= 0) {
      position = found;
      break;
    }
  }
  if (position < 0) {
    const slice = { start: 0, end: Math.min(value.length, Math.max(0, limit - 1)) };
    return { text: renderExcerptSlice(value, slice), slice };
  }
  const bodyBudget = Math.max(1, limit - 2);
  const start = Math.max(0, Math.min(value.length - bodyBudget, position - Math.floor(bodyBudget * 0.3)));
  const suffixBudget = start + bodyBudget < value.length ? 1 : 0;
  const available = Math.max(1, limit - (start > 0 ? 1 : 0) - suffixBudget);
  const end = Math.min(value.length, start + available);
  const slice = { start, end };
  return { text: scalarSafeUtf16Slice(renderExcerptSlice(value, slice), 0, limit), slice };
}

function excerptAroundTerms(value: string, terms: readonly string[], maxLength: number): string {
  return excerptAroundTermsWithSlice(value, terms, maxLength).text;
}

export function makeExcerpt(text: string, locator: string, terms: string[], maxLength = 520): ExcerptProjection {
  const limit = Math.max(1, Math.trunc(maxLength));
  const locatorMatch = spreadsheetLocator.exec(locator);
  const lines = text.replace(/\r\n/g, "\n").split("\n").map((line) => line.trim()).filter(Boolean);
  const header = lines.find((line) => line.startsWith("字段 |"));
  const parsedRows = lines.flatMap((line) => {
    const match = spreadsheetRowLine.exec(line);
    return match ? [{ number: Number(match[1]), cells: match[2] ?? "", line }] : [];
  });
  if (locatorMatch && header && parsedRows.length > 0) {
    const baseStartColumn = columnNumber(locatorMatch[2] as string);
    const baseEndColumn = columnNumber(locatorMatch[4] as string);
    const baseStartRow = Number(locatorMatch[3]);
    const baseEndRow = Number(locatorMatch[5]);
    const rows = parsedRows.filter((row) => row.number >= baseStartRow && row.number <= baseEndRow);
    if (rows.length === 0) return { text: excerptAroundTerms(text, uniqueNormalizedTerms(terms), limit), locator };
    const normalizedTerms = uniqueNormalizedTerms(terms);
    let center = -1;
    for (const term of normalizedTerms) {
      center = rows.findIndex((row) => normalizeText(row.line).includes(term));
      if (center >= 0) break;
    }
    if (center < 0) center = 0;
    let selectedRows = rows.slice(Math.max(0, center - 2), Math.min(rows.length, center + 3));
    const headerSegments = header.slice("字段 |".length).split("|").map((segment) => segment.trim()).filter(Boolean);
    const rowSegments = selectedRows.map((row) => row.cells.split("|").map((segment) => segment.trim()).filter(Boolean));
    const headerByColumn = new Map(headerSegments.flatMap((segment) => {
      const column = spreadsheetSegmentColumn(segment);
      return column ? [[column, segment] as const] : [];
    }));
    const relevantColumns = new Set<string>();
    for (const segment of headerSegments) {
      if (normalizedTerms.some((term) => normalizeText(segment).includes(term))) {
        const column = spreadsheetSegmentColumn(segment);
        if (column) relevantColumns.add(column);
      }
    }
    for (const segments of rowSegments) {
      for (const segment of segments) {
        if (normalizedTerms.some((term) => normalizeText(segment).includes(term))) {
          const column = spreadsheetSegmentColumn(segment);
          if (column) relevantColumns.add(column);
        }
      }
    }
    const orderedColumns = [...new Set([
      ...headerSegments.map(spreadsheetSegmentColumn),
      ...rowSegments.flatMap((segments) => segments.map(spreadsheetSegmentColumn)),
    ].filter((value): value is string => Boolean(value)))]
      .filter((column) => {
        const value = columnNumber(column);
        return value >= baseStartColumn && value <= baseEndColumn;
      })
      .sort((left, right) => columnNumber(left) - columnNumber(right));
    if (orderedColumns.length === 0) return { text: excerptAroundTerms(text, normalizedTerms, limit), locator };
    for (const column of orderedColumns.slice(0, 2)) relevantColumns.add(column);
    for (const column of [...relevantColumns]) {
      const position = orderedColumns.indexOf(column);
      if (position > 0) relevantColumns.add(orderedColumns[position - 1] as string);
      if (position >= 0 && position + 1 < orderedColumns.length) relevantColumns.add(orderedColumns[position + 1] as string);
    }
    const selectedColumns = (relevantColumns.size > 0 ? orderedColumns.filter((column) => relevantColumns.has(column)) : orderedColumns)
      .slice(0, 12);
    const selectedColumnSet = new Set(selectedColumns);
    const filterSegments = (segments: string[]) => segments.filter((segment) => {
      const column = spreadsheetSegmentColumn(segment);
      return !column || selectedColumnSet.has(column);
    });
    const projectedHeader = selectedColumns.map((column) => headerByColumn.get(column) ?? `${column}=未命名字段`);
    const headerLine = `字段映射（投影） | ${projectedHeader.join(" | ")}`;
    const projectedRows = selectedRows.map((row, index) => `行 ${row.number} | ${filterSegments(rowSegments[index] ?? []).join(" | ")}`);
    let projectedText = [headerLine, ...projectedRows].join("\n");
    let headerSlice: ExcerptSlice | undefined;
    let rowSlice: ExcerptSlice | undefined;
    if (projectedText.length > limit) {
      const centerRow = rows[center] as (typeof rows)[number];
      selectedRows = [centerRow];
      const centerSegments = centerRow.cells.split("|").map((segment) => segment.trim()).filter(Boolean);
      const centerLine = `行 ${centerRow.number} | ${filterSegments(centerSegments).join(" | ")}`;
      const headerBudget = Math.max(1, Math.min(Math.floor(limit * 0.35), limit - 2));
      const clippedHeader = excerptAroundTermsWithSlice(headerLine, normalizedTerms, headerBudget);
      const rowBudget = Math.max(1, limit - clippedHeader.text.length - 1);
      const clippedRow = excerptAroundTermsWithSlice(centerLine, normalizedTerms, rowBudget);
      headerSlice = clippedHeader.slice;
      rowSlice = clippedRow.slice;
      projectedText = `${clippedHeader.text}\n${clippedRow.text}`.slice(0, limit);
    }
    const startColumn = selectedColumns[0] ?? locatorMatch[2];
    const endColumn = selectedColumns.at(-1) ?? locatorMatch[4];
    const firstRow = Math.min(...selectedRows.map((row) => row.number));
    const lastRow = Math.max(...selectedRows.map((row) => row.number));
    const projectedLocator = `${locatorMatch[1]}!${startColumn}${firstRow}:${endColumn}${lastRow}`;
    return {
      text: projectedText,
      locator: projectedLocator,
      ...(selectedColumns.length > 0 ? {
        scope: {
          version: 1 as const,
          locator: projectedLocator,
          columns: selectedColumns,
          ...(headerSlice ? { headerSlice } : {}),
          ...(rowSlice ? { rowSlice } : {}),
        },
      } : {}),
    };
  }
  return { text: excerptAroundTerms(text, uniqueNormalizedTerms(terms), limit), locator };
}

function makeSourceLink(absolutePath: string, locator: string) {
  const fileName = path.basename(absolutePath);
  const escapedLabel = fileName.replaceAll("\\", "\\\\").replaceAll("[", "\\[").replaceAll("]", "\\]");
  const linkTarget = absolutePath.replaceAll("\\", "/").replaceAll(">", "%3E");
  const escapedLocator = locator.replaceAll("`", "'");
  return {
    fileName,
    label: `${fileName} · ${locator}`,
    absolutePath,
    locator,
    markdown: `[${escapedLabel}](<${linkTarget}>) · \`${escapedLocator}\``,
  };
}

function renderSpreadsheetCitationScope(
  text: string,
  baseLocator: string,
  scope: SpreadsheetCitationScope,
): string {
  const base = spreadsheetLocator.exec(baseLocator);
  const projected = spreadsheetLocator.exec(scope.locator);
  if (!base || !projected || base[1] !== projected[1]) {
    throw new Error("引用范围无效或已损坏：sheet 不匹配");
  }
  const baseStartColumn = columnNumber(base[2] as string);
  const baseEndColumn = columnNumber(base[4] as string);
  const projectedStartColumn = columnNumber(projected[2] as string);
  const projectedEndColumn = columnNumber(projected[4] as string);
  const baseStartRow = Number(base[3]);
  const baseEndRow = Number(base[5]);
  const projectedStartRow = Number(projected[3]);
  const projectedEndRow = Number(projected[5]);
  if (projectedStartColumn < baseStartColumn || projectedEndColumn > baseEndColumn
    || projectedStartColumn > projectedEndColumn
    || projectedStartRow < baseStartRow || projectedEndRow > baseEndRow
    || projectedStartRow > projectedEndRow
    || scope.columns.length === 0 || scope.columns.length > 12) {
    throw new Error("引用范围无效或已损坏：投影超出索引 chunk");
  }
  const columns = [...new Set(scope.columns)];
  if (columns.length !== scope.columns.length
    || columns.some((column) => !/^[A-Z]+$/.test(column))
    || columns.some((column) => {
      const value = columnNumber(column);
      return value < projectedStartColumn || value > projectedEndColumn;
    })
    || columns.some((column, index) => index > 0 && columnNumber(column) <= columnNumber(columns[index - 1] as string))
    || columns[0] !== projected[2] || columns.at(-1) !== projected[4]) {
    throw new Error("引用范围无效或已损坏：投影列非法");
  }
  if (Boolean(scope.headerSlice) !== Boolean(scope.rowSlice)) {
    throw new Error("引用范围无效或已损坏：文本切片不完整");
  }

  const lines = text.replace(/\r\n/g, "\n").split("\n").map((line) => line.trim()).filter(Boolean);
  const header = lines.find((line) => line.startsWith("字段 |"));
  const rows = lines.flatMap((line) => {
    const match = spreadsheetRowLine.exec(line);
    const number = Number(match?.[1]);
    return match && number >= projectedStartRow && number <= projectedEndRow
      ? [{ number, cells: match[2] ?? "" }]
      : [];
  });
  if (!header || rows.length === 0) throw new Error("引用范围无效或已损坏：投影原文不存在");
  const headerSegments = header.slice("字段 |".length).split("|").map((segment) => segment.trim()).filter(Boolean);
  const headerByColumn = new Map(headerSegments.flatMap((segment) => {
    const column = spreadsheetSegmentColumn(segment);
    return column ? [[column, segment] as const] : [];
  }));
  const columnSet = new Set(columns);
  const filterSegments = (segments: string[]) => segments.filter((segment) => {
    const column = spreadsheetSegmentColumn(segment);
    return !column || columnSet.has(column);
  });
  const headerLine = `字段映射（投影） | ${columns.map((column) => headerByColumn.get(column) ?? `${column}=未命名字段`).join(" | ")}`;
  const rowLines = rows.map((row) => `行 ${row.number} | ${filterSegments(row.cells.split("|").map((segment) => segment.trim()).filter(Boolean)).join(" | ")}`);
  if (scope.headerSlice && scope.rowSlice) {
    if (rowLines.length !== 1) throw new Error("引用范围无效或已损坏：切片必须定位单行");
    return `${renderExcerptSlice(headerLine, scope.headerSlice)}\n${renderExcerptSlice(rowLines[0] as string, scope.rowSlice)}`;
  }
  return [headerLine, ...rowLines].join("\n");
}

function serializeSpreadsheetCitationScopeV1(scope: SpreadsheetCitationScope): string {
  const payload = {
    v: scope.version,
    l: scope.locator,
    c: scope.columns,
    ...(scope.headerSlice ? { h: [scope.headerSlice.start, scope.headerSlice.end] } : {}),
    ...(scope.rowSlice ? { r: [scope.rowSlice.start, scope.rowSlice.end] } : {}),
  };
  return Buffer.from(JSON.stringify(payload), "utf8").toString("base64url");
}

function scopedCitationDigestV1(row: LexicalCandidateRow, payload: string): string {
  return createHash("sha256")
    .update("drag-scoped-citation-v1\0")
    .update(row.chunk_id)
    .update("\0")
    .update(row.content_hash)
    .update("\0")
    .update(payload)
    .digest("base64url")
    .slice(0, 22);
}

function scopedCitationIdV1(row: LexicalCandidateRow, scope: SpreadsheetCitationScope): string {
  const payload = serializeSpreadsheetCitationScopeV1(scope);
  return `${citationIdPrefix}${row.chunk_id}~${payload}.${scopedCitationDigestV1(row, payload)}`;
}

interface CompactSpreadsheetCitationScopeV2 {
  rowStartDelta: number;
  rowSpan: number;
  columnDeltas: number[];
  headerSlice?: ExcerptSlice;
  rowSlice?: ExcerptSlice;
}

function appendUnsignedVarint(target: number[], value: number): void {
  if (!Number.isSafeInteger(value) || value < 0 || value > 2_147_483_647) {
    throw new Error("引用范围无效或已损坏：短引用整数越界");
  }
  let remaining = value;
  do {
    const byte = remaining % 128;
    remaining = Math.floor(remaining / 128);
    target.push(byte | (remaining > 0 ? 0x80 : 0));
  } while (remaining > 0);
}

function readUnsignedVarint(payload: Buffer, cursor: { offset: number }): number {
  let result = 0;
  let multiplier = 1;
  for (let index = 0; index < 5; index += 1) {
    const byte = payload[cursor.offset];
    if (byte === undefined) throw new Error("引用范围无效或已损坏：短引用被截断");
    cursor.offset += 1;
    result += (byte & 0x7f) * multiplier;
    if ((byte & 0x80) === 0) {
      if (!Number.isSafeInteger(result) || result > 2_147_483_647) {
        throw new Error("引用范围无效或已损坏：短引用整数越界");
      }
      return result;
    }
    multiplier *= 128;
  }
  throw new Error("引用范围无效或已损坏：短引用整数过长");
}

function encodeSpreadsheetCitationScopeV2(
  row: LexicalCandidateRow,
  scope: SpreadsheetCitationScope,
): Buffer | null {
  const chunk = /^chunk_([0-9a-f]{16})_([0-9a-f]{8})_(\d+)$/i.exec(row.chunk_id);
  const base = spreadsheetLocator.exec(row.locator);
  const projected = spreadsheetLocator.exec(scope.locator);
  if (!chunk || !base || !projected || base[1] !== projected[1]) return null;
  const columns = [...new Set(scope.columns)];
  if (columns.length !== scope.columns.length) {
    throw new Error("引用范围无效或已损坏：生成器拒绝重复投影列");
  }
  const hasSlices = Boolean(scope.headerSlice || scope.rowSlice);
  if (Boolean(scope.headerSlice) !== Boolean(scope.rowSlice)) {
    throw new Error("引用范围无效或已损坏：生成器拒绝不完整切片");
  }
  const baseStartRow = Number(base[3]);
  const projectedStartRow = Number(projected[3]);
  const projectedEndRow = Number(projected[5]);
  const baseStartColumn = columnNumber(base[2] as string);
  const projectedColumns = columns.map(columnNumber);
  if (projectedColumns.length === 0
    || projectedColumns.some((value, index) => index > 0 && value <= (projectedColumns[index - 1] as number))) {
    throw new Error("引用范围无效或已损坏：生成器拒绝无序投影列");
  }

  const bytes = [
    ...Buffer.from(chunk[1] as string, "hex"),
    ...Buffer.from(chunk[2] as string, "hex"),
  ];
  appendUnsignedVarint(bytes, Number(chunk[3]));
  bytes.push(hasSlices ? 1 : 0);
  appendUnsignedVarint(bytes, projectedStartRow - baseStartRow);
  appendUnsignedVarint(bytes, projectedEndRow - projectedStartRow);
  appendUnsignedVarint(bytes, projectedColumns.length);
  projectedColumns.forEach((value, index) => {
    appendUnsignedVarint(bytes, index === 0
      ? value - baseStartColumn
      : value - (projectedColumns[index - 1] as number));
  });
  if (scope.headerSlice && scope.rowSlice) {
    appendUnsignedVarint(bytes, scope.headerSlice.start);
    appendUnsignedVarint(bytes, scope.headerSlice.end - scope.headerSlice.start);
    appendUnsignedVarint(bytes, scope.rowSlice.start);
    appendUnsignedVarint(bytes, scope.rowSlice.end - scope.rowSlice.start);
  }
  return Buffer.from(bytes);
}

function scopedCitationDigestV2(row: LexicalCandidateRow, payload: Buffer): string {
  return createHash("sha256")
    .update("drag-scoped-citation-v2\0")
    .update(row.content_hash)
    .update("\0")
    .update(payload)
    .digest("base64url")
    .slice(0, 22);
}

function scopedCitationId(row: LexicalCandidateRow, scope: SpreadsheetCitationScope): string {
  const compact = encodeSpreadsheetCitationScopeV2(row, scope);
  if (!compact) return scopedCitationIdV1(row, scope);
  const payload = compact.toString("base64url");
  return `${citationIdPrefix}2.${payload}.${scopedCitationDigestV2(row, compact)}`;
}

function parseScopedCitationValue(value: unknown): SpreadsheetCitationScope {
  if (!value || typeof value !== "object") throw new Error("引用范围无效或已损坏：scope 格式错误");
  const record = value as Record<string, unknown>;
  const parseSlice = (input: unknown): ExcerptSlice | undefined => {
    if (input === undefined) return undefined;
    if (!Array.isArray(input) || input.length !== 2
      || !input.every((item) => typeof item === "number" && Number.isInteger(item))) {
      throw new Error("引用范围无效或已损坏：scope 切片错误");
    }
    return { start: input[0] as number, end: input[1] as number };
  };
  if (record.v !== 1 || typeof record.l !== "string"
    || !Array.isArray(record.c) || !record.c.every((item) => typeof item === "string")) {
    throw new Error("引用范围无效或已损坏：scope 字段错误");
  }
  return {
    version: 1,
    locator: record.l,
    columns: record.c as string[],
    ...(record.h !== undefined ? { headerSlice: parseSlice(record.h) as ExcerptSlice } : {}),
    ...(record.r !== undefined ? { rowSlice: parseSlice(record.r) as ExcerptSlice } : {}),
  };
}

function parseCitationReference(citationId: string): {
  chunkId: string;
  scopeV1?: SpreadsheetCitationScope;
  payloadV1?: string;
  scopeV2?: CompactSpreadsheetCitationScopeV2;
  payloadV2?: Buffer;
  digest?: string;
} {
  const value = citationId.startsWith(citationIdPrefix) ? citationId.slice(citationIdPrefix.length) : citationId;
  if (value.startsWith("2.")) {
    const parts = value.split(".");
    if (parts.length !== 3 || !parts[1] || !parts[2] || parts[1].length > 512) {
      throw new Error("引用范围无效或已损坏：短 scoped citation 格式错误");
    }
    const payload = Buffer.from(parts[1], "base64url");
    if (payload.length < 17 || payload.toString("base64url") !== parts[1]) {
      throw new Error("引用范围无效或已损坏：短 scoped citation 无法解码");
    }
    const cursor = { offset: 12 };
    const chunkHash = payload.subarray(0, 8).toString("hex");
    const documentSuffix = payload.subarray(8, 12).toString("hex");
    const ordinal = readUnsignedVarint(payload, cursor);
    const flags = payload[cursor.offset];
    if (flags === undefined || (flags & ~1) !== 0) throw new Error("引用范围无效或已损坏：短引用 flags 非法");
    cursor.offset += 1;
    const rowStartDelta = readUnsignedVarint(payload, cursor);
    const rowSpan = readUnsignedVarint(payload, cursor);
    const columnCount = readUnsignedVarint(payload, cursor);
    if (columnCount < 1 || columnCount > 12) throw new Error("引用范围无效或已损坏：短引用列数非法");
    const columnDeltas = Array.from({ length: columnCount }, (_unused, index) => {
      const delta = readUnsignedVarint(payload, cursor);
      if (index > 0 && delta < 1) throw new Error("引用范围无效或已损坏：短引用投影列重复或无序");
      return delta;
    });
    let headerSlice: ExcerptSlice | undefined;
    let rowSlice: ExcerptSlice | undefined;
    if ((flags & 1) !== 0) {
      const headerStart = readUnsignedVarint(payload, cursor);
      const headerLength = readUnsignedVarint(payload, cursor);
      const rowStart = readUnsignedVarint(payload, cursor);
      const rowLength = readUnsignedVarint(payload, cursor);
      headerSlice = { start: headerStart, end: headerStart + headerLength };
      rowSlice = { start: rowStart, end: rowStart + rowLength };
    }
    if (cursor.offset !== payload.length) throw new Error("引用范围无效或已损坏：短引用存在尾随数据");
    return {
      chunkId: `chunk_${chunkHash}_${documentSuffix}_${ordinal}`,
      scopeV2: {
        rowStartDelta,
        rowSpan,
        columnDeltas,
        ...(headerSlice ? { headerSlice } : {}),
        ...(rowSlice ? { rowSlice } : {}),
      },
      payloadV2: payload,
      digest: parts[2],
    };
  }
  const separator = value.indexOf("~");
  if (separator < 0) return { chunkId: value };
  const chunkId = value.slice(0, separator);
  const token = value.slice(separator + 1);
  const digestSeparator = token.lastIndexOf(".");
  if (!chunkId || digestSeparator <= 0 || token.length > 4_096) {
    throw new Error("引用范围无效或已损坏：scoped citation 格式错误");
  }
  const payload = token.slice(0, digestSeparator);
  const digest = token.slice(digestSeparator + 1);
  try {
    const parsed = JSON.parse(Buffer.from(payload, "base64url").toString("utf8")) as unknown;
    return { chunkId, scopeV1: parseScopedCitationValue(parsed), payloadV1: payload, digest };
  } catch (error) {
    if (error instanceof Error && error.message.startsWith("引用范围")) throw error;
    throw new Error("引用范围无效或已损坏：scoped citation 无法解码");
  }
}

function decodeSpreadsheetCitationScopeV2(
  baseLocator: string,
  compact: CompactSpreadsheetCitationScopeV2,
): SpreadsheetCitationScope {
  const base = spreadsheetLocator.exec(baseLocator);
  if (!base) throw new Error("引用范围无效或已损坏：短引用底层 locator 非表格范围");
  const baseStartColumn = columnNumber(base[2] as string);
  let currentColumn = baseStartColumn;
  const columns = compact.columnDeltas.map((delta, index) => {
    currentColumn = index === 0 ? baseStartColumn + delta : currentColumn + delta;
    return columnName(currentColumn);
  });
  const startRow = Number(base[3]) + compact.rowStartDelta;
  const endRow = startRow + compact.rowSpan;
  const locator = `${base[1]}!${columns[0]}${startRow}:${columns.at(-1)}${endRow}`;
  return {
    version: 1,
    locator,
    columns,
    ...(compact.headerSlice ? { headerSlice: compact.headerSlice } : {}),
    ...(compact.rowSlice ? { rowSlice: compact.rowSlice } : {}),
  };
}

function makeCitation(
  row: LexicalCandidateRow,
  revision: number,
  projection?: ExcerptProjection,
  citationIdOverride?: string,
): Citation {
  const locator = projection?.locator ?? row.locator;
  const headingPath = safeHeadingPath(row.heading_path_json);
  return {
    citationId: citationIdOverride ?? (projection?.scope ? scopedCitationId(row, projection.scope) : `${citationIdPrefix}${row.chunk_id}`),
    display: `${row.title} · ${headingPath.at(-1) ?? row.section_type} · ${locator}`,
    sourceId: row.source_id,
    sourceLabel: row.source_label,
    sourceKind: row.source_kind,
    absolutePath: row.absolute_path,
    relativePath: row.relative_path,
    documentId: row.id,
    chunkId: row.chunk_id,
    locator,
    headingPath,
    indexedContentHash: row.content_hash,
    indexRevision: revision,
    stale: Boolean(row.stale),
    sourceLink: makeSourceLink(row.absolute_path, locator),
  };
}

interface QueryAnchorSignals {
  explicitAnchors: string[];
  documentAnchors: string[];
  identityGroups: DocumentIdentityGroup[];
  latestIntent: boolean;
}

interface DocumentIdentityGroup {
  phrase: string;
  terms: string[];
}

interface DocumentRankContext {
  query: string;
  terms: string[];
  signals: QueryAnchorSignals;
}

const activityEntityLeadWords = [
  "帮我", "给我", "我要", "我想", "想要", "需要", "找到", "找出", "查找", "查询", "看看",
  "最新", "最近", "复用", "沿用", "套用", "一个", "这个", "那个", "关于", "分析", "说明", "请", "的",
].sort((left, right) => right.length - left.length);
const genericActivityEntityNames = new Set(["活动", "玩法", "配置", "配表", "表格", "任务", "奖励", "版本", "最新", "最近"]);
const activityAuxiliaryRoles = [
  { title: /(累充|充值)/i, query: /(累充|充值)/i },
  { title: /(玩法内容|玩法设计|表格设计)/i, query: /(玩法内容|玩法设计|表格设计|配表)/i },
  { title: /(数组特效|特效设计)/i, query: /(数组特效|特效设计)/i },
  { title: /(商业数值|数值模型)/i, query: /(商业数值|数值模型)/i },
  { title: /复用/i, query: /(复用|沿用|套用)/i },
] as const;

function compactIdentity(value: string): string {
  return normalizeText(value).replace(/[^\p{L}\p{N}]+/gu, "");
}

function trimActivityEntityLead(value: string): string {
  let result = normalizeText(value).replace(/^[\s·•・:_-]+|[\s·•・:_-]+$/g, "");
  let changed = true;
  while (changed && result) {
    changed = false;
    for (const word of activityEntityLeadWords) {
      if (!result.startsWith(word)) continue;
      result = result.slice(word.length).replace(/^[\s·•・:_-]+/, "");
      changed = true;
      break;
    }
  }
  return compactIdentity(result);
}

function extractDocumentIdentityGroups(query: string): DocumentIdentityGroup[] {
  const groups = new Map<string, DocumentIdentityGroup>();
  for (const match of query.matchAll(/([\p{Script=Han}·•・:_-]{2,24})[\s·•・:_-]*(\d{2,})/gu)) {
    const entity = trimActivityEntityLead(match[1] ?? "");
    const numeric = normalizeText(match[2] ?? "");
    if (Array.from(entity).length < 2 || genericActivityEntityNames.has(entity) || !numeric) continue;
    const terms = [entity, numeric];
    const phrase = terms.join("");
    groups.set(phrase, { phrase, terms });
  }
  return [...groups.values()];
}

function identityGroupScore(value: string, group: DocumentIdentityGroup): number {
  const identity = compactIdentity(value);
  if (!identity) return 0;
  if (identity.includes(group.phrase)) return 4;
  let cursor = 0;
  let first = -1;
  let last = -1;
  for (const term of group.terms) {
    const position = identity.indexOf(term, cursor);
    if (position < 0) return 0;
    if (first < 0) first = position;
    last = position + term.length;
    cursor = last;
  }
  const termLength = group.terms.reduce((total, term) => total + term.length, 0);
  const gap = Math.max(0, last - first - termLength);
  return Math.max(2, 3.5 - Math.min(1.5, gap / 8));
}

function namedDocumentIdentityScore(
  title: string,
  relativePath: string,
  identityGroups: readonly DocumentIdentityGroup[],
): number {
  return Math.max(0, ...identityGroups.map((group) => {
    const titleScore = identityGroupScore(title, group);
    return Math.max(titleScore > 0 ? titleScore + 0.25 : 0, identityGroupScore(relativePath, group));
  }));
}

export function queryAnchorSignals(query: string): QueryAnchorSignals {
  const raw = query.match(/[a-z0-9_./:-]{2,}/gi) ?? [];
  const latestIntent = /最新|最近|\blatest\b/i.test(query);
  const contentAnchors = raw.filter((anchor) => !(latestIntent && normalizeText(anchor) === "latest"));
  return {
    explicitAnchors: [...new Set(contentAnchors.map(normalizeText))],
    documentAnchors: [...new Set(contentAnchors
      .filter((anchor) => /[A-Z]/.test(anchor) || /\d|[_./:-]/.test(anchor) || anchor.length >= 8)
      .map(normalizeText))],
    identityGroups: extractDocumentIdentityGroups(query),
    latestIntent,
  };
}

function matchesDocumentIdentity(
  title: string,
  relativePath: string,
  documentAnchors: readonly string[],
): boolean {
  const identity = normalizeText(`${title}\n${relativePath}`);
  return documentAnchors.some((anchor) => identity.includes(anchor));
}

function matchesIdentitySignals(
  title: string,
  relativePath: string,
  signals: QueryAnchorSignals,
): boolean {
  if (signals.identityGroups.length > 0) {
    return namedDocumentIdentityScore(title, relativePath, signals.identityGroups) > 0;
  }
  return matchesDocumentIdentity(title, relativePath, signals.documentAnchors);
}

function shouldRankDocumentIdentity(signals: QueryAnchorSignals): boolean {
  return signals.identityGroups.length > 0 || (signals.latestIntent && signals.documentAnchors.length > 0);
}

function documentIdentityRoleScore(hit: SearchHit, context: DocumentRankContext): number {
  if (!shouldRankDocumentIdentity(context.signals)) return 0;
  const normalizedTitle = normalizeText(hit.title);
  const normalizedPath = normalizeText(hit.relativePath);
  const normalizedIdentity = `${normalizedTitle}\n${normalizedPath}`;
  const normalizedQuery = normalizeText(context.query);
  let score = namedDocumentIdentityScore(hit.title, hit.relativePath, context.signals.identityGroups) * 4;
  for (const anchor of context.signals.documentAnchors) {
    if (normalizedTitle.includes(anchor)) score += 1;
    else if (normalizedPath.includes(anchor)) score += 0.25;
  }
  for (const term of context.terms) {
    if (!normalizedIdentity.includes(term)) continue;
    score += Math.min(10, Array.from(term).length) * 0.08;
  }
  if ((context.signals.identityGroups.length > 0 || normalizedQuery.includes("活动")) && normalizedTitle.includes("活动")) {
    score += 1;
  }
  for (const role of activityAuxiliaryRoles) {
    if (!role.title.test(normalizedTitle)) continue;
    score += role.query.test(normalizedQuery) ? 0.6 : -0.45;
  }
  return score;
}

function candidateHaystack(row: LexicalCandidateRow): string {
  return normalizeText([
    row.title,
    row.relative_path,
    safeHeadingPath(row.heading_path_json).join(" / "),
    row.text,
  ].join("\n"));
}

function hitHaystack(hit: SearchHit): string {
  return normalizeText([
    hit.title,
    hit.relativePath,
    ...hit.excerpts.flatMap((excerpt) => [excerpt.headingPath.join(" / "), excerpt.text]),
  ].join("\n"));
}

function matchesQuerySignals(
  row: LexicalCandidateRow,
  primaryConcept: string[] | undefined,
  explicitAnchors: string[],
  documentAnchors: string[],
): boolean {
  if (!primaryConcept && explicitAnchors.length === 0) return true;
  const haystack = candidateHaystack(row);
  const matchesDocumentAnchor = documentAnchors.some((anchor) => haystack.includes(anchor));
  const remainingAnchors = explicitAnchors.filter((anchor) => !documentAnchors.includes(anchor));
  const matchesConcept = matchesDocumentAnchor || !primaryConcept || primaryConcept.some((term) => haystack.includes(term));
  return matchesConcept
    && (documentAnchors.length === 0 || matchesDocumentAnchor)
    && remainingAnchors.every((anchor) => haystack.includes(anchor));
}

export class SearchEngine {
  constructor(
    private readonly database: IndexDatabase,
    private readonly getConfig: () => AppConfig,
  ) {}

  async search(request: SearchRequest, candidateScope?: SearchCandidateScope): Promise<SearchResponse> {
    const startedAt = performance.now();
    const config = this.getConfig();
    const query = request.query.trim();
    if (!query) throw new Error("query 不能为空");
    const requestedMode = request.retrievalMode ?? "auto";
    const sort = request.sort ?? config.search.defaultSort;
    const limit = Math.min(100, Math.max(1, request.limit ?? config.search.defaultLimit));
    const expandedTerms = expandQueryTerms(query, config.search.synonymExpansion);
    const candidateDocumentIds = [...new Set((candidateScope?.documentIds ?? [])
      .map((value) => value.trim())
      .filter(Boolean))].slice(0, 50);
    const documentRestricted = candidateScope !== undefined;
    const requestedSourceIds = request.sourceIds?.length ? new Set(request.sourceIds) : null;
    const requestedSourceKinds = request.sourceKinds?.length ? new Set(request.sourceKinds) : null;
    const eligibleSources = config.sources
      .filter((source) => source.enabled)
      .filter((source) => !requestedSourceIds || requestedSourceIds.has(source.id))
      .filter((source) => !requestedSourceKinds || requestedSourceKinds.has(source.kind));
    const eligibleSourceIds = eligibleSources.map((source) => source.id);
    const eligibleSourcesById = new Map(eligibleSources.map((source) => [source.id, source]));
    const effectiveRequest: SearchRequest = { ...request, sourceIds: eligibleSourceIds };
    if (eligibleSourceIds.length === 0) {
      return {
        query,
        expandedTerms,
        requestedMode,
        actualMode: "lexical",
        semanticUsed: false,
        semanticCoverage: 0,
        sort,
        indexRevision: this.database.getRevision(),
        totalCandidates: 0,
        tookMs: Math.round((performance.now() - startedAt) * 10) / 10,
        hits: [],
        warnings: ["当前没有符合筛选条件的已启用资料源"],
      };
    }
    const lexicalTokens = [...new Set(expandedTerms.flatMap(cjkSearchTerms).filter((term) => term.length >= 2))].slice(0, 80);
    const lexicalQuery = lexicalTokens.map(escapeFtsToken).join(" OR ");
    const trigramTerms = expandedTerms.filter((term) => Array.from(term).length >= 3).slice(0, 24);
    const trigramQuery = trigramTerms.map(escapeFtsToken).join(" OR ");
    const primaryConcept = queryConceptGroups(query)[0];
    const signals = queryAnchorSignals(query);
    const { explicitAnchors, documentAnchors, identityGroups, latestIntent } = signals;
    const tableIntent = /(配表|配置表|哪些表|表格|字段|参数|前端模块|后台模块)/i.test(query);
    const designOnly = eligibleSources.length > 0 && eligibleSources.every((source) => source.kind === "design");
    const indexedCandidateLimit = tableIntent ? 1_200 : Math.min(1_200, Math.max(320, limit * 40));
    const sourceFilter = {
      sourceIds: eligibleSourceIds,
      sourceScopes: eligibleSources.map((source) => ({
        sourceId: source.id,
        sourceIdentity: sourceIndexIdentity(source),
      })),
      ...(effectiveRequest.sourceKinds ? { sourceKinds: effectiveRequest.sourceKinds } : {}),
    };

    const merged = new Map<string, LexicalCandidateRow>();
    const exactCanonicalRepresentatives = new Map<string, string>();
    const addRows = (rows: LexicalCandidateRow[], exact = false) => {
      rows.forEach((row) => {
        const canonicalGroup = row.canonical_id || row.id;
        if (exact) {
          const representative = exactCanonicalRepresentatives.get(canonicalGroup);
          if (representative && representative !== row.id) return;
          exactCanonicalRepresentatives.set(canonicalGroup, row.id);
        } else {
          const representative = exactCanonicalRepresentatives.get(canonicalGroup);
          if (representative && representative !== row.id) return;
        }
        const existing = merged.get(row.chunk_id);
        if (!existing || row.lexical_rank < existing.lexical_rank) merged.set(row.chunk_id, row);
      });
    };
    const identityLookupAnchors = identityGroups.flatMap((group) => group.terms.filter((term) => !/^\d+$/.test(term)));
    const exactLookupAnchors = [...new Set([...documentAnchors, ...identityLookupAnchors])];
    let exactRows: LexicalCandidateRow[] = [];
    if (documentRestricted) {
      addRows(this.database.documentCandidates(
        candidateDocumentIds,
        expandedTerms,
        candidateScope?.chunksPerDocument ?? 12,
        sourceFilter,
      ));
    } else {
      if (exactLookupAnchors.length > 0) {
        exactRows = this.database.documentExactCandidates(exactLookupAnchors, Math.min(240, indexedCandidateLimit), sourceFilter)
          .filter((row) => matchesFilters(row, effectiveRequest));
        addRows(exactRows, true);
      }
      try {
        addRows(this.database.lexicalCandidates(lexicalQuery, indexedCandidateLimit, sourceFilter));
      } catch {
        // Malformed/unsupported FTS queries still fall back to literal LIKE below.
      }
      try {
        addRows(this.database.trigramCandidates(trigramQuery, indexedCandidateLimit, sourceFilter));
      } catch {
        // Trigram is a recall enhancer, never the only path.
      }
      const indexedSignalCount = [...merged.values()]
        .filter((row) => matchesFilters(row, effectiveRequest))
        .filter((row) => matchesQuerySignals(row, primaryConcept, explicitAnchors, documentAnchors))
        .length;
      // LIKE scans chunk text and is intentionally a fallback. On a complete
      // million-chunk index, FTS/trigram usually provide ample anchored rows;
      // literal scanning remains available for rare terms and degraded SQLite.
      const fallbackFloor = Math.max(24, limit * 3);
      const exactCoverage = new Set(exactRows
        .filter((row) => matchesQuerySignals(row, primaryConcept, explicitAnchors, documentAnchors))
        .flatMap((row) => row.exact_anchors ?? []));
      const allDocumentAnchorsCovered = documentAnchors.length > 0
        && documentAnchors.every((anchor) => exactCoverage.has(anchor));
      if (!allDocumentAnchorsCovered && indexedSignalCount < fallbackFloor && merged.size < fallbackFloor * 4) {
        addRows(this.database.likeCandidates(expandedTerms, 800, sourceFilter));
      }
    }
    let rows = [...merged.values()]
      .filter((row) => matchesFilters(row, effectiveRequest))
      .filter((row) => matchesConfiguredSource(row, eligibleSourcesById));
    const identityDocumentIds = new Set<string>();
    if (!documentRestricted) {
      const identityPriority = identityGroups.length > 0 && (!tableIntent || designOnly);
      for (const row of rows) {
        if (namedDocumentIdentityScore(row.title, row.relative_path, identityGroups) > 0) identityDocumentIds.add(row.id);
      }
      if (identityPriority && identityDocumentIds.size > 0) {
        rows = rows.filter((row) => identityDocumentIds.has(row.id));
      } else if (latestIntent && !tableIntent && documentAnchors.length > 0) {
        const latestIdentityDocumentIds = new Set(rows
          .filter((row) => matchesDocumentIdentity(row.title, row.relative_path, documentAnchors))
          .map((row) => row.id));
        if (latestIdentityDocumentIds.size > 0) {
          rows = rows.filter((row) => latestIdentityDocumentIds.has(row.id));
        }
      }
    }

    const warnings: string[] = [];
    if (!this.database.fts5Available) warnings.push("当前 SQLite 不支持 FTS5，已降级为字面子串检索");
    if (!this.database.trigramAvailable) warnings.push("当前 SQLite 不支持 trigram，短语子串召回已降级");

    let semanticUsed = false;
    let semanticCoverage = 0;
    const semanticScores = new Map<string, number>();
    const wantsSemantic = requestedMode === "semantic" || requestedMode === "hybrid" || (requestedMode === "auto" && config.search.embedding.enabled);
    if (wantsSemantic && config.search.embedding.enabled && rows.length > 0) {
      try {
        const provider = new OllamaEmbeddingProvider(config.search.embedding);
        const semanticRows = rows.slice(0, 80);
        const vectors = await provider.embed([query, ...semanticRows.map((row) => `${row.title}\n${row.text.slice(0, 2_000)}`)]);
        const queryVector = vectors[0] ?? [];
        semanticRows.forEach((row, index) => {
          semanticScores.set(row.chunk_id, Math.max(0, cosineSimilarity(queryVector, vectors[index + 1] ?? [])));
        });
        semanticUsed = semanticScores.size > 0;
        semanticCoverage = semanticScores.size / rows.length;
      } catch (error) {
        warnings.push(`本地语义检索不可用，已使用词法结果：${error instanceof Error ? error.message : String(error)}`);
      }
    } else if (requestedMode === "semantic" && !config.search.embedding.enabled) {
      warnings.push("语义检索未启用，已降级为词法检索");
    }

    const normalizedTerms = [...new Set(expandedTerms.map(normalizeText).filter((term) => term.length >= 2))];
    const projectionTerms = uniqueNormalizedTerms([
      ...identityGroups.flatMap((group) => [group.phrase, ...group.terms]),
      normalizeText(query),
      ...normalizedTerms,
      ...documentAnchors,
      ...explicitAnchors,
    ]);
    const requiredExactDocumentIds = [...new Set(documentAnchors.flatMap((anchor) => {
      const representative = exactRows.find((row) => row.exact_anchors?.includes(anchor));
      return representative ? [representative.id] : [];
    }))];
    const requiredExactDocumentIdSet = new Set(requiredExactDocumentIds);
    const scored = rows.map((row) => scoreCandidate(row, normalizedTerms, semanticScores.get(row.chunk_id) ?? 0));
    const byDocument = new Map<string, ScoredCandidate[]>();
    for (const candidate of scored) {
      const list = byDocument.get(candidate.row.id) ?? [];
      list.push(candidate);
      byDocument.set(candidate.row.id, list);
    }
    const revision = this.database.getRevision();
    let hits: SearchHit[] = [...byDocument.values()].map((candidates) => {
      candidates.sort((left, right) => right.score - left.score);
      const best = candidates[0] as ScoredCandidate;
      const excerpts: SearchExcerpt[] = candidates.slice(0, 3).map((candidate) => {
        const projection = makeExcerpt(candidate.row.text, candidate.row.locator, projectionTerms);
        const excerptText = projection.text;
        return {
          chunkId: candidate.row.chunk_id,
          sectionType: candidate.row.section_type,
          headingPath: safeHeadingPath(candidate.row.heading_path_json),
          locator: projection.locator,
          text: excerptText,
          highlightedText: highlightTerms(excerptText, candidate.matchedTerms),
          score: candidate.score,
          citation: makeCitation(candidate.row, revision, projection),
        };
      });
      const sectionTypes = [...new Set(candidates.map((candidate) => candidate.row.section_type))] as SectionType[];
      return {
        documentId: best.row.id,
        sourceId: best.row.source_id,
        sourceLabel: best.row.source_label,
        sourceKind: best.row.source_kind,
        title: best.row.title,
        absolutePath: best.row.absolute_path,
        relativePath: best.row.relative_path,
        extension: best.row.extension,
        effectiveUpdatedAt: best.row.effective_updated_at,
        dateSource: best.row.date_source as DateSource,
        filesystemModifiedAt: best.row.filesystem_modified_at,
        relevance: Math.max(...candidates.map((candidate) => candidate.score)),
        familyKey: best.row.family_key,
        familyConfidence: best.row.family_confidence,
        stale: Boolean(best.row.stale),
        sectionTypes,
        excerpts,
      };
    });

    if (!documentRestricted && (primaryConcept || explicitAnchors.length > 0)) {
      hits = hits.filter((hit) => {
        const haystack = hitHaystack(hit);
        const matchesDocumentAnchor = documentAnchors.some((anchor) => haystack.includes(anchor));
        const remainingAnchors = explicitAnchors.filter((anchor) => !documentAnchors.includes(anchor));
        const matchesConcept = matchesDocumentAnchor || !primaryConcept || primaryConcept.some((term) => haystack.includes(term));
        return matchesConcept
          && (documentAnchors.length === 0 || matchesDocumentAnchor)
          && remainingAnchors.every((anchor) => haystack.includes(anchor));
      });
    }
    const rankContext: DocumentRankContext = { query, terms: normalizedTerms, signals };
    if (!documentRestricted && hits.length > 0) {
      const bestRelevance = Math.max(...hits.map((hit) => hit.relevance));
      const qualityFloor = Math.min(bestRelevance, Math.max(0.3, bestRelevance * 0.7));
      hits = hits.filter((hit) => requiredExactDocumentIdSet.has(hit.documentId)
        || identityDocumentIds.has(hit.documentId)
        || documentIdentityRoleScore(hit, rankContext) >= 0.9
        || hit.relevance >= qualityFloor);
    }
    hits = this.sortHits(hits, sort, rankContext);
    if (request.latestPerFamily) {
      const seen = new Set<string>();
      hits = hits.filter((hit) => {
        if (seen.has(hit.familyKey)) return false;
        seen.add(hit.familyKey);
        return true;
      });
    }
    const selected = new Map<string, SearchHit>();
    for (const documentId of requiredExactDocumentIds.slice(0, limit)) {
      const hit = hits.find((candidate) => candidate.documentId === documentId);
      if (hit) selected.set(hit.documentId, hit);
    }
    for (const hit of hits) {
      if (selected.size >= limit) break;
      selected.set(hit.documentId, hit);
    }
    hits = this.sortHits([...selected.values()], sort, rankContext).slice(0, limit);
    rows = [];
    return {
      query,
      expandedTerms,
      requestedMode,
      actualMode: semanticUsed ? "hybrid" : "lexical",
      semanticUsed,
      semanticCoverage,
      sort,
      indexRevision: revision,
      totalCandidates: merged.size,
      tookMs: Math.round((performance.now() - startedAt) * 10) / 10,
      hits,
      warnings,
    };
  }

  async retrieve(request: RetrievalRequest): Promise<RetrievalBundle> {
    const config = this.getConfig();
    const maxDocuments = Math.min(50, Math.max(1, request.maxDocuments ?? 8));
    const maxChunks = Math.min(10, Math.max(1, request.maxChunksPerDocument ?? 4));
    const maxChars = Math.min(60_000, Math.max(2_000, request.maxChars ?? config.search.maxEvidenceChars));
    const selectedDocumentIds = [...new Set((request.documentIds ?? [])
      .map((value) => value.trim())
      .filter(Boolean))].slice(0, maxDocuments);
    const candidateScope: SearchCandidateScope | undefined = selectedDocumentIds.length > 0
      ? { documentIds: selectedDocumentIds, chunksPerDocument: Math.max(12, maxChunks * 3) }
      : undefined;
    let search: SearchResponse;
    if (!request.sourceIds?.length && !request.sourceKinds?.length) {
      const perSourceLimit = Math.max(maxDocuments, request.limit ?? 0);
      const [designSearch, tableSearch] = await Promise.all([
        this.search({ ...request, sourceKinds: ["design"], limit: perSourceLimit }, candidateScope),
        this.search({ ...request, sourceKinds: ["table"], limit: perSourceLimit }, candidateScope),
      ]);
      const tableFirst = /(配表|配置表|哪些表|表格|字段|参数|前端模块|后台模块)/i.test(request.query);
      const retrievalSignals = queryAnchorSignals(request.query);
      const retrievalRankContext: DocumentRankContext = {
        query: request.query,
        terms: uniqueNormalizedTerms(expandQueryTerms(request.query, config.search.synonymExpansion)),
        signals: retrievalSignals,
      };
      let primary = tableFirst ? tableSearch.hits : designSearch.hits;
      let secondary = tableFirst ? designSearch.hits : tableSearch.hits;
      const restrictToDocumentIdentity = !tableFirst && (
        retrievalSignals.identityGroups.length > 0
        || (retrievalSignals.latestIntent && retrievalSignals.documentAnchors.length > 0)
      );
      if (restrictToDocumentIdentity) {
        const identityDocumentIds = new Set([...primary, ...secondary]
          .filter((hit) => matchesIdentitySignals(hit.title, hit.relativePath, retrievalSignals))
          .map((hit) => hit.documentId));
        if (identityDocumentIds.size > 0) {
          primary = primary.filter((hit) => identityDocumentIds.has(hit.documentId));
          secondary = secondary.filter((hit) => identityDocumentIds.has(hit.documentId));
        }
      }
      const primaryQuota = Math.ceil(maxDocuments * 0.75);
      let selectedPrimary = primary.slice(0, primaryQuota);
      if (tableFirst) {
        const recentQuota = Math.max(1, Math.ceil(primaryQuota * 0.66));
        const selected = new Map(primary.slice(0, recentQuota).map((hit) => [hit.documentId, hit]));
        for (const hit of [...primary].sort((left, right) => right.relevance - left.relevance)) {
          if (selected.size >= primaryQuota) break;
          selected.set(hit.documentId, hit);
        }
        selectedPrimary = this.sortHits([...selected.values()], "newest", retrievalRankContext);
      }
      const quotaHits = [...selectedPrimary, ...secondary.slice(0, maxDocuments - primaryQuota)];
      const allCandidateHits = [...primary, ...secondary];
      const retrievalAnchors = retrievalSignals.documentAnchors;
      const requiredIdentityHits = tableFirst && retrievalSignals.identityGroups.length > 0
        ? designSearch.hits
          .filter((hit) => matchesIdentitySignals(hit.title, hit.relativePath, retrievalSignals))
          .slice(0, 1)
        : [];
      const requiredHits = [...requiredIdentityHits, ...retrievalAnchors.flatMap((anchor) => {
        const hit = allCandidateHits.find((candidate) => restrictToDocumentIdentity
          ? matchesDocumentIdentity(candidate.title, candidate.relativePath, [anchor])
          : hitHaystack(candidate).includes(anchor));
        return hit ? [hit] : [];
      })];
      const selectedHits = new Map<string, SearchHit>();
      for (const hit of requiredHits.slice(0, maxDocuments)) selectedHits.set(hit.documentId, hit);
      for (const hit of [...quotaHits, ...allCandidateHits]) {
        if (selectedHits.size >= maxDocuments) break;
        selectedHits.set(hit.documentId, hit);
      }
      const mergedHits = this.sortHits([...selectedHits.values()], designSearch.sort, retrievalRankContext).slice(0, maxDocuments);
      search = {
        ...designSearch,
        actualMode: designSearch.semanticUsed || tableSearch.semanticUsed ? "hybrid" : "lexical",
        semanticUsed: designSearch.semanticUsed || tableSearch.semanticUsed,
        semanticCoverage: Math.max(designSearch.semanticCoverage, tableSearch.semanticCoverage),
        totalCandidates: designSearch.totalCandidates + tableSearch.totalCandidates,
        tookMs: Math.round((designSearch.tookMs + tableSearch.tookMs) * 10) / 10,
        hits: mergedHits,
        warnings: [...new Set([...designSearch.warnings, ...tableSearch.warnings])],
      };
    } else {
      search = await this.search({ ...request, limit: Math.max(maxDocuments, request.limit ?? 0) }, candidateScope);
    }
    const allowedDocuments = selectedDocumentIds.length > 0 ? new Set(selectedDocumentIds) : null;
    if (allowedDocuments) {
      const availableDocumentIds = new Set(search.hits.map((hit) => hit.documentId));
      const unavailableDocumentIds = selectedDocumentIds.filter((documentId) => !availableDocumentIds.has(documentId));
      search = {
        ...search,
        hits: search.hits.filter((hit) => allowedDocuments.has(hit.documentId)),
        warnings: unavailableDocumentIds.length > 0
          ? [...search.warnings, `以下 documentId 不存在、已禁用或不符合筛选条件：${unavailableDocumentIds.join(", ")}`]
          : search.warnings,
      };
    }
    const evidence: RetrievalEvidence[] = [];
    let characterCount = 0;
    let truncated = false;

    for (const hit of search.hits.filter((item) => !allowedDocuments || allowedDocuments.has(item.documentId)).slice(0, maxDocuments)) {
      for (const excerpt of hit.excerpts.slice(0, maxChunks)) {
        const nextLength = characterCount + excerpt.text.length;
        if (nextLength > maxChars) {
          truncated = true;
          break;
        }
        evidence.push({
          citationId: excerpt.citation.citationId,
          title: hit.title,
          effectiveUpdatedAt: hit.effectiveUpdatedAt,
          dateSource: hit.dateSource,
          sectionType: excerpt.sectionType,
          locator: excerpt.locator,
          relativePath: hit.relativePath,
          absolutePath: hit.absolutePath,
          sourceLink: excerpt.citation.sourceLink,
          content: excerpt.text,
          indexedContentHash: excerpt.citation.indexedContentHash,
        });
        characterCount = nextLength;
      }
      if (truncated) break;
    }
    return {
      kind: "drag_retrieval_bundle_v1",
      trust: "untrusted_reference_data",
      query: search.query,
      indexRevision: search.indexRevision,
      actualMode: search.actualMode,
      generatedAt: new Date().toISOString(),
      truncated,
      characterCount,
      evidence,
      search,
    };
  }

  readCitation(citationId: string, expectedIndexRevision?: number): {
    citation: Citation;
    content: string;
    changed: boolean;
    currentIndexRevision: number;
  } {
    const reference = parseCitationReference(citationId);
    const row = this.database.getChunk(reference.chunkId);
    if (!row) throw new Error(`引用不存在或已删除：${citationId}`);
    const enabledSourcesById = new Map(this.getConfig().sources.filter((source) => source.enabled).map((source) => [source.id, source]));
    if (!matchesConfiguredSource(row, enabledSourcesById)) {
      throw new Error(`引用所属资料源已禁用、不存在或已变更：${citationId}`);
    }
    const revision = this.database.getRevision();
    let projection: ExcerptProjection | undefined;
    let canonicalScopedId: string | undefined;
    if (reference.scopeV1) {
      if (!reference.payloadV1 || !reference.digest
        || scopedCitationDigestV1(row, reference.payloadV1) !== reference.digest) {
        throw new Error("引用范围无效或已损坏：scoped citation 校验失败");
      }
      const content = renderSpreadsheetCitationScope(row.text, row.locator, reference.scopeV1);
      projection = { text: content, locator: reference.scopeV1.locator, scope: reference.scopeV1 };
      canonicalScopedId = scopedCitationIdV1(row, reference.scopeV1);
    } else if (reference.scopeV2) {
      if (!reference.payloadV2 || !reference.digest
        || scopedCitationDigestV2(row, reference.payloadV2) !== reference.digest) {
        throw new Error("引用范围无效或已损坏：短 scoped citation 校验失败");
      }
      const scope = decodeSpreadsheetCitationScopeV2(row.locator, reference.scopeV2);
      const content = renderSpreadsheetCitationScope(row.text, row.locator, scope);
      projection = { text: content, locator: scope.locator, scope };
      canonicalScopedId = scopedCitationId(row, scope);
    }
    if (canonicalScopedId && citationId.startsWith(citationIdPrefix) && canonicalScopedId !== citationId) {
      throw new Error("引用范围无效或已损坏：scoped citation 不一致");
    }
    return {
      citation: makeCitation(row, revision, projection, canonicalScopedId),
      content: projection?.text ?? row.text,
      changed: expectedIndexRevision !== undefined && expectedIndexRevision !== revision,
      currentIndexRevision: revision,
    };
  }

  private sortHits(hits: SearchHit[], sort: SearchSort, rankContext?: DocumentRankContext): SearchHit[] {
    if (sort === "relevance") {
      return hits.sort((left, right) => right.relevance - left.relevance || Date.parse(right.effectiveUpdatedAt) - Date.parse(left.effectiveUpdatedAt));
    }
    if (sort === "hybrid") {
      const dates = hits.map((hit) => Date.parse(hit.effectiveUpdatedAt));
      const min = Math.min(...dates);
      const max = Math.max(...dates);
      return hits.sort((left, right) => {
        const leftDate = max === min ? 1 : (Date.parse(left.effectiveUpdatedAt) - min) / (max - min);
        const rightDate = max === min ? 1 : (Date.parse(right.effectiveUpdatedAt) - min) / (max - min);
        return (right.relevance * 0.68 + rightDate * 0.32) - (left.relevance * 0.68 + leftDate * 0.32);
      });
    }
    return hits.sort((left, right) => Date.parse(right.effectiveUpdatedAt) - Date.parse(left.effectiveUpdatedAt)
      || (rankContext
        ? documentIdentityRoleScore(right, rankContext) - documentIdentityRoleScore(left, rankContext)
        : 0)
      || right.relevance - left.relevance
      || left.relativePath.localeCompare(right.relativePath, "zh-CN"));
  }
}
