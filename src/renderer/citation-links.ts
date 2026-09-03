import type { ChatCitation } from "../shared/contracts.js";

const CITATION_SCHEME = "drag-citation:";

function escapeMarkdownLabel(value: string): string {
  return value.replaceAll("\\", "\\\\").replaceAll("[", "\\[").replaceAll("]", "\\]");
}

function normalizeLocalPath(value: string): string {
  let candidate = value.trim();
  try {
    if (/^file:/i.test(candidate)) candidate = new URL(candidate).pathname;
    candidate = decodeURIComponent(candidate);
  } catch {
    // Keep the original value when it is not a valid URL or percent-encoded path.
  }
  return candidate
    .replace(/^\/([a-z]:\/)/i, "$1")
    .replaceAll("\\", "/")
    .normalize("NFKC")
    .toLowerCase();
}

export function citationMarkdown(text: string, lookup: ReadonlyMap<string, ChatCitation>): string {
  return text.replace(/\[\[(DRAG:[^\]]+)\]\]/g, (_full, citationId: string) => {
    const citation = lookup.get(citationId);
    if (!citation) return "[未验证引用]";
    return `[${escapeMarkdownLabel(citation.label)}](${CITATION_SCHEME}${citationId})`;
  });
}

function sourceLinkMarkdown(citation: ChatCitation): string {
  const fileName = citation.absolutePath.split(/[\\/]/).at(-1) ?? citation.title;
  const linkTarget = citation.absolutePath.replaceAll("\\", "/").replaceAll(">", "%3E");
  const locator = citation.locator.replaceAll("`", "'");
  return `[${escapeMarkdownLabel(fileName)}](<${linkTarget}>) · \`${locator}\``;
}

export function bindCitationLinks(text: string, lookup: ReadonlyMap<string, ChatCitation>): string {
  let bound = citationMarkdown(text, lookup);
  for (const citation of lookup.values()) {
    const fileName = citation.absolutePath.split(/[\\/]/).at(-1) ?? citation.title;
    const replacement = `[${escapeMarkdownLabel(fileName)}](${CITATION_SCHEME}${citation.citationId}) · \`${citation.locator.replaceAll("`", "'")}\``;
    bound = bound.replaceAll(sourceLinkMarkdown(citation), replacement);
  }
  return bound;
}

export function resolveMessageUrl(url: string, lookup: ReadonlyMap<string, ChatCitation>): string {
  if (url.startsWith(CITATION_SCHEME)) {
    return lookup.has(url.slice(CITATION_SCHEME.length)) ? url : "";
  }
  if (/^(?:https?:|mailto:)/i.test(url)) return url;

  const normalized = normalizeLocalPath(url);
  const matches = [...lookup.values()].filter((citation) => normalizeLocalPath(citation.absolutePath) === normalized);
  return matches.length === 1 ? `${CITATION_SCHEME}${matches[0]?.citationId}` : "";
}
