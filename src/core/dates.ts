import path from "node:path";
import type { DateEvidence, DateResolution, ExtractedBlock } from "./types.js";

const MIN_DATE = Date.UTC(2000, 0, 1);

function validDateMs(value: number): number | null {
  const maxDate = Date.now() + 366 * 24 * 60 * 60 * 1_000;
  return Number.isFinite(value) && value >= MIN_DATE && value <= maxDate ? value : null;
}

function validCalendarDateMs(year: number, month: number, day: number): number | null {
  const value = Date.UTC(year, month - 1, day);
  const date = new Date(value);
  if (date.getUTCFullYear() !== year || date.getUTCMonth() !== month - 1 || date.getUTCDate() !== day) return null;
  return validDateMs(value);
}

export function findDates(value: string): number[] {
  const found = new Set<number>();
  for (const match of value.matchAll(/(?<!\d)(20\d{2})[-/.年_](0?[1-9]|1[0-2])(?:[-/.月_](0?[1-9]|[12]\d|3[01])日?)?(?!\d)/g)) {
    const year = Number(match[1]);
    const month = Number(match[2]);
    const day = Number(match[3] ?? 1);
    const date = validCalendarDateMs(year, month, day);
    if (date !== null) found.add(date);
  }
  for (const match of value.matchAll(/(?<!\d)(20\d{2})(0[1-9]|1[0-2])([0-2]\d|3[01])(?!\d)/g)) {
    const date = validCalendarDateMs(Number(match[1]), Number(match[2]), Number(match[3]));
    if (date !== null) found.add(date);
  }
  return [...found].sort((a, b) => a - b);
}

function latest(values: number[]): number | null {
  return values.length > 0 ? Math.max(...values) : null;
}

export function findShortYearDates(value: string): number[] {
  const found = new Set<number>();
  for (const match of value.matchAll(/(?<!\d)(0?[1-9]|1[0-2])\/(0?[1-9]|[12]\d|3[01])\/(\d{2})(?!\d)/g)) {
    const date = validCalendarDateMs(2_000 + Number(match[3]), Number(match[1]), Number(match[2]));
    if (date !== null) found.add(date);
  }
  return [...found].sort((a, b) => a - b);
}

export function excelSerialDateMs(serial: number): number | null {
  if (!Number.isInteger(serial) || serial <= 0) return null;
  const epoch = Date.UTC(1899, 11, 30);
  const value = epoch + serial * 24 * 60 * 60 * 1_000;
  const date = new Date(value);
  return validCalendarDateMs(date.getUTCFullYear(), date.getUTCMonth() + 1, date.getUTCDate());
}

const revisionDateField = /^(?:(?:修订|版本|修改|更新|变更)\s*)?(?:日期|时间)$/i;
const leadingVersionDate = /^\s*(?:[#>*-]+\s*)?(?:版本|version)\]?\s*(?:[:：=]\s*)?(?:20\d{6}|20\d{2}[-/.年_](?:0?[1-9]|1[0-2])(?:[-/.月_](?:0?[1-9]|[12]\d|3[01])日?)?)(?!\d)/i;

function pushEvidence(target: DateEvidence[], values: number[], evidence: Omit<DateEvidence, "timestampMs">): void {
  for (const timestampMs of values) {
    if (!target.some((item) => item.timestampMs === timestampMs && item.kind === evidence.kind && item.locator === evidence.locator)) {
      target.push({ timestampMs, ...evidence });
    }
  }
}

export function collectBlockDateEvidence(blocks: readonly ExtractedBlock[]): DateEvidence[] {
  const evidence: DateEvidence[] = [];
  for (const block of blocks) {
    const lines = block.text.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
    const cells = lines.map((line) => line.split("|").map((cell) => cell.trim()));
    for (let row = 0; row < cells.length; row += 1) {
      const header = cells[row] ?? [];
      const dateColumn = header.findIndex((cell) => revisionDateField.test(cell.replace(/^[A-Z]+\d*(?:\[[^\]]*\])?=/, "").trim()));
      if (dateColumn < 0) continue;
      for (const dataRow of cells.slice(row + 1)) {
        const value = dataRow[dateColumn] ?? "";
        pushEvidence(evidence, findDates(value), {
          strength: "strong",
          kind: "revision_table",
          locator: block.locator,
        });
      }
      break;
    }
    for (const line of lines) {
      const prefix = line.match(leadingVersionDate)?.[0];
      if (!prefix) continue;
      pushEvidence(evidence, findDates(prefix), {
        strength: "strong",
        kind: "leading_version",
        locator: block.locator,
      });
    }
  }
  return evidence;
}

function findVersionLogDates(text: string): { strong: number | null; weak: number | null } {
  const strongDates: number[] = [];
  const weakDates: number[] = [];
  const dateHeader = /(版本|修订|修改|更新|变更)\s*(日期|时间)|\b(version|revision)\s*(date|time)\b/i;
  const versionField = /(版本|version)\]?\s*[:：=]\s*20\d{2}/i;
  const versionAction = /(初版|首版|修改|更新|复用|迭代|变更|revision|changelog)/i;
  const versionLeadingDate = /^\s*(?:[#>*-]+\s*)?(?:版本|version)\]?\s*(?:[:：=]\s*)?(?:20\d{6}|20\d{2}[-/.年_](?:0?[1-9]|1[0-2])(?:[-/.月_](?:0?[1-9]|[12]\d|3[01])日?)?)(?!\d)/i;
  const versionCover = /(版本|version)\s*([:：]|\s+)\s*v?\d+(\.\d+){0,3}\s+20\d{2}/i;
  let inDateTable = false;
  let relevantLines = 0;
  for (const line of text.split(/\r?\n/)) {
    if (relevantLines >= 2_000) break;
    if (dateHeader.test(line)) {
      inDateTable = true;
      strongDates.push(...findDates(line));
      // SheetJS renders Excel date cells as M/D/YY in some legacy workbooks.
      // Accept that format only on an explicit version/revision date row.
      strongDates.push(...findShortYearDates(line));
      relevantLines += 1;
      continue;
    }
    if (inDateTable || versionField.test(line) || versionAction.test(line)) {
      strongDates.push(...findDates(line));
      relevantLines += 1;
      continue;
    }
    const leadingVersionDate = line.match(versionLeadingDate)?.[0];
    if (leadingVersionDate) {
      // Only the date immediately following the marker is authoritative; do
      // not let a later campaign date rescue an invalid leading date.
      strongDates.push(...findDates(leadingVersionDate));
      relevantLines += 1;
      continue;
    }
    if (versionCover.test(line)) {
      weakDates.push(...findDates(line));
      relevantLines += 1;
    }
  }
  return { strong: latest(strongDates), weak: latest(weakDates) };
}

export function resolveEffectiveDate(input: {
  absolutePath: string;
  contentSample: string;
  embeddedModifiedAt: string | null;
  filesystemMtimeMs: number;
  dateEvidence?: readonly DateEvidence[];
}): DateResolution {
  const basename = path.basename(input.absolutePath, path.extname(input.absolutePath));
  const filenameDateMs = latest(findDates(basename));
  const structuredEvidence = input.dateEvidence;
  const versionDates = structuredEvidence === undefined
    ? findVersionLogDates(input.contentSample)
    : {
        strong: latest(structuredEvidence.filter((item) => item.strength === "strong").map((item) => item.timestampMs)),
        weak: latest(structuredEvidence.filter((item) => item.strength === "weak").map((item) => item.timestampMs)),
      };
  const versionLogDateMs = versionDates.strong ?? versionDates.weak;
  const pathDateMs = latest(findDates(path.dirname(input.absolutePath)));
  const embeddedModifiedAtMs = input.embeddedModifiedAt
    ? validDateMs(Date.parse(input.embeddedModifiedAt))
    : null;

  if (filenameDateMs !== null) {
    return { effectiveUpdatedAtMs: filenameDateMs, dateSource: "filename", filenameDateMs, versionLogDateMs, pathDateMs, embeddedModifiedAtMs };
  }
  if (versionDates.strong !== null) {
    return { effectiveUpdatedAtMs: versionDates.strong, dateSource: "version_log", filenameDateMs, versionLogDateMs, pathDateMs, embeddedModifiedAtMs };
  }
  if (pathDateMs !== null) {
    return { effectiveUpdatedAtMs: pathDateMs, dateSource: "path", filenameDateMs, versionLogDateMs, pathDateMs, embeddedModifiedAtMs };
  }
  if (versionDates.weak !== null) {
    return { effectiveUpdatedAtMs: versionDates.weak, dateSource: "version_log", filenameDateMs, versionLogDateMs, pathDateMs, embeddedModifiedAtMs };
  }
  if (embeddedModifiedAtMs !== null) {
    return { effectiveUpdatedAtMs: embeddedModifiedAtMs, dateSource: "embedded_modified", filenameDateMs, versionLogDateMs, pathDateMs, embeddedModifiedAtMs };
  }
  return {
    effectiveUpdatedAtMs: input.filesystemMtimeMs,
    dateSource: "filesystem_mtime",
    filenameDateMs,
    versionLogDateMs,
    pathDateMs,
    embeddedModifiedAtMs,
  };
}

export function toIsoDateTime(value: number): string {
  return new Date(value).toISOString();
}
