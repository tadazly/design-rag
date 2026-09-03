export interface SpreadsheetLocation {
  sheetName: string;
  range: string;
}

export function parseSpreadsheetLocation(locator: string): SpreadsheetLocation | null {
  const separator = locator.lastIndexOf("!");
  if (separator <= 0 || separator >= locator.length - 1) return null;
  const sheetName = locator.slice(0, separator).trim().replace(/^'(.*)'$/, "$1").replaceAll("''", "'");
  const range = locator.slice(separator + 1).trim();
  if (!sheetName || !/^(?:[A-Z]{1,3}\d+)(?::[A-Z]{1,3}\d+)?$/i.test(range)) return null;
  return { sheetName, range: range.toUpperCase() };
}

export function parsePdfPage(locator: string): number | null {
  const match = locator.match(/(?:^|\b)(?:page|页)\s*[:#]?\s*(\d+)\b/i);
  if (!match?.[1]) return null;
  const page = Number(match[1]);
  return Number.isSafeInteger(page) && page > 0 ? page : null;
}
