import { createHash } from "node:crypto";

const ZERO_WIDTH = /[\u200b-\u200d\ufeff]/g;
const WHITESPACE = /\s+/g;
const CJK_RUN = /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}]+/gu;
const ASCII_TOKEN = /[a-z0-9_./:-]+/g;

export function normalizeText(value: string): string {
  return value.normalize("NFKC").replace(ZERO_WIDTH, "").replace(WHITESPACE, " ").trim().toLowerCase();
}

export function sha256(value: string | Uint8Array): string {
  return createHash("sha256").update(value).digest("hex");
}

export function cjkSearchTerms(value: string): string[] {
  const normalized = normalizeText(value);
  const tokens = new Set<string>();
  for (const ascii of normalized.match(ASCII_TOKEN) ?? []) {
    if (ascii.length > 1) tokens.add(ascii);
  }
  for (const run of normalized.match(CJK_RUN) ?? []) {
    const chars = Array.from(run);
    for (const char of chars) tokens.add(char);
    for (let index = 0; index < chars.length - 1; index += 1) {
      tokens.add(`${chars[index]}${chars[index + 1]}`);
    }
    if (chars.length <= 8) tokens.add(run);
  }
  return [...tokens];
}

export function buildSearchTerms(...values: string[]): string {
  const tokens = new Set<string>();
  for (const value of values) {
    for (const token of cjkSearchTerms(value)) {
      if (Array.from(token).length >= 2) tokens.add(token);
    }
  }
  return [...tokens].join(" ");
}

export function stripControlCharacters(value: string): string {
  return value.replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, "");
}

export function escapeFtsToken(value: string): string {
  return `"${value.replace(/"/g, '""')}"`;
}

export function summarizeText(value: string, maxLength = 280): string {
  const normalized = value.replace(WHITESPACE, " ").trim();
  if (normalized.length <= maxLength) return normalized;
  return `${normalized.slice(0, Math.max(0, maxLength - 1)).trimEnd()}…`;
}

export function highlightTerms(value: string, terms: string[]): string {
  let output = value;
  const uniqueTerms = [...new Set(terms.map(normalizeText).filter((term) => term.length >= 2))]
    .sort((a, b) => b.length - a.length)
    .slice(0, 12);
  for (const term of uniqueTerms) {
    const escaped = term.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    output = output.replace(new RegExp(escaped, "giu"), (match) => `**${match}**`);
  }
  return output;
}
