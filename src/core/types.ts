import type { DateSource, SectionType, SourceKind } from "../shared/contracts.js";

export interface FileCandidate {
  sourceId: string;
  sourceLabel: string;
  sourceKind: SourceKind;
  sourceIdentity: string;
  rootPath: string;
  absolutePath: string;
  relativePath: string;
  extension: string;
  sizeBytes: number;
  filesystemMtimeMs: number;
}

export interface ExtractedBlock {
  ordinal: number;
  text: string;
  headingPath: string[];
  sectionType: SectionType;
  locator: string;
  metadata?: Record<string, string | number | boolean>;
}

export interface DateEvidence {
  timestampMs: number;
  strength: "strong" | "weak";
  kind: "revision_table" | "leading_version" | "dated_sheet" | "version_axis" | "version_field" | "cover_version";
  locator: string;
}

export interface ExtractedDocument {
  title: string;
  blocks: ExtractedBlock[];
  embeddedModifiedAt: string | null;
  warnings: string[];
  needsOcr: boolean;
  /** Undefined keeps the legacy text fallback; an empty array is authoritative. */
  dateEvidence?: DateEvidence[];
}

export interface ChunkDraft {
  ordinal: number;
  text: string;
  headingPath: string[];
  sectionType: SectionType;
  locator: string;
  contentHash: string;
}

export interface DateResolution {
  effectiveUpdatedAtMs: number;
  dateSource: DateSource;
  filenameDateMs: number | null;
  versionLogDateMs: number | null;
  pathDateMs: number | null;
  embeddedModifiedAtMs: number | null;
}

export interface DocumentDraft {
  id: string;
  candidate: FileCandidate;
  title: string;
  familyKey: string;
  familyConfidence: number;
  contentHash: string;
  date: DateResolution;
  chunks: ChunkDraft[];
  warnings: string[];
  needsOcr: boolean;
}

export interface StoredChunkRow {
  rowid: number;
  id: string;
  document_id: string;
  ordinal: number;
  section_type: SectionType;
  heading_path_json: string;
  locator: string;
  text: string;
  normalized_text: string;
  search_terms: string;
  content_hash: string;
}

export interface StoredDocumentRow {
  id: string;
  canonical_id: string;
  source_id: string;
  source_label: string;
  source_kind: SourceKind;
  source_identity: string;
  absolute_path: string;
  relative_path: string;
  extension: string;
  title: string;
  family_key: string;
  family_confidence: number;
  size_bytes: number;
  filesystem_mtime_ms: number;
  filesystem_modified_at: string;
  effective_updated_at_ms: number;
  effective_updated_at: string;
  date_source: DateSource;
  content_hash: string;
  indexed_at: string;
  stale: number;
  deleted: number;
  extraction_error: string | null;
  warnings_json: string;
  needs_ocr: number;
  chunk_count: number;
  scan_generation: string;
}

export interface EmbeddingProvider {
  readonly id: string;
  embed(input: string[], signal?: AbortSignal): Promise<number[][]>;
  isAvailable(signal?: AbortSignal): Promise<boolean>;
}
