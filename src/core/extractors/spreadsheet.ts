import path from "node:path";
import * as fs from "node:fs";
import { readFile } from "node:fs/promises";
import JSZip from "jszip";
import * as XLSX from "xlsx";
import * as cptable from "xlsx/dist/cpexcel.full.mjs";
import { classifySection } from "../classifier.js";
import { excelSerialDateMs, findDates, findShortYearDates } from "../dates.js";
import type { DateEvidence, ExtractedBlock, ExtractedDocument } from "../types.js";

XLSX.set_cptable(cptable);
XLSX.set_fs(fs);

interface CellEntry {
  address: string;
  row: number;
  column: number;
  text: string;
  value: unknown;
  formulaOnly: boolean;
}

const structuredDateField = /^(?:修订|版本|修改|更新|变更)\s*(?:日期|时间)$|^(?:版本|version|招募版本|复用版本)$/i;
const datedSheet = /(版本|修订|更新|迭代|测试内容)/i;

function cellDateValues(cell: CellEntry, allowSerial: boolean): number[] {
  const values = new Set<number>([...findDates(cell.text), ...findShortYearDates(cell.text)]);
  if (cell.value instanceof Date && Number.isFinite(cell.value.getTime())) {
    values.add(Date.UTC(cell.value.getUTCFullYear(), cell.value.getUTCMonth(), cell.value.getUTCDate()));
  }
  if (allowSerial) {
    const numeric = typeof cell.value === "number" ? cell.value : /^\d{5}$/.test(cell.text) ? Number(cell.text) : NaN;
    const serialDate = excelSerialDateMs(numeric);
    if (serialDate !== null) values.add(serialDate);
  }
  return [...values];
}

function collectSheetDateEvidence(sheetName: string, rows: Array<[number, CellEntry[]]>): DateEvidence[] {
  const evidence: DateEvidence[] = [];
  const push = (timestampMs: number, kind: DateEvidence["kind"], locator: string) => {
    if (!evidence.some((item) => item.timestampMs === timestampMs && item.kind === kind && item.locator === locator)) {
      evidence.push({ timestampMs, strength: "strong", kind, locator });
    }
  };
  if (datedSheet.test(sheetName)) {
    for (const timestampMs of findDates(sheetName)) push(timestampMs, "dated_sheet", sheetName);
  }

  const flat = rows.flatMap(([row, cells]) => cells.map((cell) => ({ row, cell })));
  for (const [headerRow, cells] of rows) {
    const semantic = cells.filter((cell) => structuredDateField.test(cell.text.trim()));
    for (const header of semantic) {
      for (const item of flat) {
        if (item.row <= headerRow || item.cell.column !== header.column) continue;
        for (const timestampMs of cellDateValues(item.cell, true)) {
          push(timestampMs, "version_field", `${sheetName}!${item.cell.address}`);
        }
      }
    }

    if (semantic.length === 0) continue;
    const axisCandidates = cells.flatMap((cell) => cellDateValues(cell, true).map((timestampMs) => ({ cell, timestampMs })));
    if (axisCandidates.length < 2) continue;
    for (const candidate of axisCandidates) {
      const hasBusinessContent = flat.some((item) => item.row > headerRow
        && item.cell.column === candidate.cell.column
        && !item.cell.formulaOnly
        && item.cell.text.trim() !== "");
      if (hasBusinessContent) push(candidate.timestampMs, "version_axis", `${sheetName}!${candidate.cell.address}`);
    }
  }
  return evidence;
}

function reconcileWorkbookDateEvidence(evidence: DateEvidence[]): DateEvidence[] {
  const axisSheets = new Set<string>();
  const axisSheetsByDate = new Map<number, Set<string>>();
  for (const item of evidence) {
    if (item.kind !== "version_axis") continue;
    const separator = item.locator.lastIndexOf("!");
    if (separator <= 0) continue;
    const sheetName = item.locator.slice(0, separator);
    axisSheets.add(sheetName);
    const sheets = axisSheetsByDate.get(item.timestampMs) ?? new Set<string>();
    sheets.add(sheetName);
    axisSheetsByDate.set(item.timestampMs, sheets);
  }
  if (axisSheets.size < 2) return evidence;
  const corroborated = new Set([...axisSheetsByDate.entries()]
    .filter(([, sheets]) => sheets.size >= 2)
    .map(([timestampMs]) => timestampMs));
  if (corroborated.size === 0) return evidence;
  return evidence.filter((item) => item.kind !== "version_axis" || corroborated.has(item.timestampMs));
}

function cellText(cell: XLSX.CellObject): string {
  const formatted = typeof cell.w === "string" && cell.w.trim() ? cell.w : cell.v === undefined ? "" : String(cell.v);
  if (cell.f) return formatted ? `${formatted} [公式: ${cell.f}]` : `[公式: ${cell.f}]`;
  return formatted;
}

function rowText(cells: CellEntry[], headers: Map<number, string>): string {
  return cells
    .map((cell) => {
      const header = headers.get(cell.column);
      return header && header !== cell.text ? `${header}=${cell.text}` : `${cell.address}=${cell.text}`;
    })
    .join(" | ");
}

function extractSheetBlocks(sheetName: string, sheet: XLSX.WorkSheet, startOrdinal: number): { blocks: ExtractedBlock[]; dateEvidence: DateEvidence[] } {
  const entries: CellEntry[] = [];
  for (const key of Object.keys(sheet)) {
    if (key.startsWith("!")) continue;
    let decoded: XLSX.CellAddress;
    try {
      decoded = XLSX.utils.decode_cell(key);
    } catch {
      continue;
    }
    const cell = sheet[key] as XLSX.CellObject;
    const text = cellText(cell).replace(/\s+/g, " ").trim();
    if (!text) continue;
    entries.push({
      address: key,
      row: decoded.r,
      column: decoded.c,
      text,
      value: cell.v,
      formulaOnly: Boolean(cell.f) && (cell.v === undefined || cell.v === null || cell.v === ""),
    });
    if (entries.length >= 1_000_000) break;
  }
  entries.sort((left, right) => left.row - right.row || left.column - right.column);
  const byRow = new Map<number, CellEntry[]>();
  for (const entry of entries) {
    const row = byRow.get(entry.row) ?? [];
    row.push(entry);
    byRow.set(entry.row, row);
  }
  const rows = [...byRow.entries()].sort(([left], [right]) => left - right);
  const headers = new Map<number, string>();
  const firstSubstantial = rows.find(([, cells]) => cells.length >= 2);
  if (firstSubstantial) {
    for (const cell of firstSubstantial[1]) {
      if (cell.text.length <= 80) headers.set(cell.column, cell.text);
    }
  }

  const blocks: ExtractedBlock[] = [];
  const groupSize = sheetName.includes("属性") ? 18 : 24;
  for (let index = 0; index < rows.length; index += groupSize) {
    const group = rows.slice(index, index + groupSize);
    if (group.length === 0) continue;
    const firstRow = group[0]?.[0] ?? 0;
    const lastRow = group[group.length - 1]?.[0] ?? firstRow;
    const content = group.map(([, cells]) => rowText(cells, headers)).filter(Boolean).join("\n");
    const allCells = group.flatMap(([, cells]) => cells);
    const minColumn = Math.min(...allCells.map((cell) => cell.column));
    const maxColumn = Math.max(...allCells.map((cell) => cell.column));
    const range = `${XLSX.utils.encode_col(minColumn)}${firstRow + 1}:${XLSX.utils.encode_col(maxColumn)}${lastRow + 1}`;
    blocks.push({
      ordinal: startOrdinal + blocks.length,
      text: content,
      headingPath: [sheetName],
      sectionType: classifySection([sheetName], content),
      locator: `${sheetName}!${range}`,
      metadata: { sheet: sheetName, range, actualCellCount: allCells.length },
    });
  }
  return { blocks, dateEvidence: collectSheetDateEvidence(sheetName, rows) };
}

async function inspectWorkbookZip(filePath: string, inputBuffer?: Buffer): Promise<{ macro: boolean; modified: string | null }> {
  try {
    const buffer = inputBuffer ?? await readFile(filePath);
    if (buffer[0] !== 0x50 || buffer[1] !== 0x4b) return { macro: false, modified: null };
    const zip = await JSZip.loadAsync(buffer);
    const macro = Object.keys(zip.files).some((name) => /vbaProject\.bin$/i.test(name));
    const core = zip.file("docProps/core.xml");
    if (!core) return { macro, modified: null };
    const xml = await core.async("string");
    const match = /<(?:dcterms:)?modified[^>]*>([^<]+)</i.exec(xml);
    return { macro, modified: match?.[1] ?? null };
  } catch {
    return { macro: false, modified: null };
  }
}

export async function extractSpreadsheet(filePath: string, inputBuffer?: Buffer): Promise<ExtractedDocument> {
  const workbook = inputBuffer ? XLSX.read(inputBuffer, {
    type: "buffer",
    cellDates: true,
    cellFormula: true,
    cellText: true,
    cellStyles: false,
    bookVBA: false,
  }) : XLSX.readFile(filePath, {
    cellDates: true,
    cellFormula: true,
    cellText: true,
    cellStyles: false,
    bookVBA: false,
  });
  const blocks: ExtractedBlock[] = [];
  const dateEvidence: DateEvidence[] = [];
  for (const sheetName of workbook.SheetNames) {
    const sheet = workbook.Sheets[sheetName];
    if (!sheet) continue;
    const extracted = extractSheetBlocks(sheetName, sheet, blocks.length);
    blocks.push(...extracted.blocks);
    dateEvidence.push(...extracted.dateEvidence);
  }
  const inspection = await inspectWorkbookZip(filePath, inputBuffer);
  const warnings: string[] = [];
  if (inspection.macro) warnings.push("工作簿包含 VBA 项目；索引器仅只读解析，不执行宏");
  if (blocks.length === 0) warnings.push("工作簿未发现实际非空单元格");
  return {
    title: path.basename(filePath, path.extname(filePath)),
    blocks,
    embeddedModifiedAt: inspection.modified,
    warnings,
    needsOcr: false,
    dateEvidence: reconcileWorkbookDateEvidence(dateEvidence),
  };
}
