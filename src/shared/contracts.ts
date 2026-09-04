export const APP_NAME = "DRAG · 游戏策划知识库";
export const APP_ID = "design-rag";
export const APP_VERSION = "0.3.2";

export type SourceKind = "design" | "table";
export type SectionType =
  | "overview"
  | "version_history"
  | "flow"
  | "gameplay"
  | "panel_logic"
  | "config"
  | "reward_value"
  | "statistics"
  | "art_requirement"
  | "animation_requirement"
  | "other";

export type RetrievalMode = "lexical" | "semantic" | "hybrid" | "auto";
export type SearchSort = "newest" | "relevance" | "hybrid";
export type DateSource =
  | "frontmatter"
  | "filename"
  | "version_log"
  | "path"
  | "embedded_modified"
  | "filesystem_mtime";

export interface KnowledgeSourceConfig {
  id: string;
  label: string;
  kind: SourceKind;
  rootPath: string;
  enabled: boolean;
  includeExtensions: string[];
  excludeDirectoryNames: string[];
  maxFileBytes: number;
}

export interface EmbeddingConfig {
  enabled: boolean;
  provider: "ollama";
  endpoint: string;
  model: string;
  timeoutMs: number;
}

export interface AppConfig {
  schemaVersion: 1;
  sources: KnowledgeSourceConfig[];
  search: {
    defaultSort: SearchSort;
    defaultLimit: number;
    maxEvidenceChars: number;
    synonymExpansion: boolean;
    embedding: EmbeddingConfig;
  };
  indexing: {
    automaticScan: boolean;
    scanIntervalMinutes: number;
    concurrency: number;
  };
  codex: {
    codexPath: string | null;
    model: string | null;
    reasoningEffort: string | null;
  };
}

export interface Citation {
  citationId: string;
  display: string;
  sourceId: string;
  sourceLabel: string;
  sourceKind: SourceKind;
  absolutePath: string;
  relativePath: string;
  documentId: string;
  chunkId: string;
  locator: string;
  headingPath: string[];
  indexedContentHash: string;
  indexRevision: number;
  stale: boolean;
  sourceLink: CitationSourceLink;
}

export interface CitationSourceLink {
  fileName: string;
  label: string;
  absolutePath: string;
  locator: string;
  markdown: string;
}

export interface SearchExcerpt {
  chunkId: string;
  sectionType: SectionType;
  headingPath: string[];
  locator: string;
  text: string;
  highlightedText: string;
  score: number;
  citation: Citation;
}

export interface SearchHit {
  documentId: string;
  sourceId: string;
  sourceLabel: string;
  sourceKind: SourceKind;
  title: string;
  absolutePath: string;
  relativePath: string;
  extension: string;
  effectiveUpdatedAt: string;
  dateSource: DateSource;
  filesystemModifiedAt: string;
  relevance: number;
  familyKey: string;
  familyConfidence: number;
  stale: boolean;
  sectionTypes: SectionType[];
  excerpts: SearchExcerpt[];
}

export interface SearchRequest {
  query: string;
  sourceIds?: string[];
  sourceKinds?: SourceKind[];
  sectionTypes?: SectionType[];
  extensions?: string[];
  updatedAfter?: string;
  updatedBefore?: string;
  retrievalMode?: RetrievalMode;
  sort?: SearchSort;
  latestPerFamily?: boolean;
  limit?: number;
}

export interface SearchResponse {
  query: string;
  expandedTerms: string[];
  requestedMode: RetrievalMode;
  actualMode: "lexical" | "hybrid";
  semanticUsed: boolean;
  semanticCoverage: number;
  sort: SearchSort;
  indexRevision: number;
  totalCandidates: number;
  tookMs: number;
  hits: SearchHit[];
  warnings: string[];
}

export interface RetrievalRequest extends SearchRequest {
  documentIds?: string[];
  maxDocuments?: number;
  maxChunksPerDocument?: number;
  maxChars?: number;
}

export interface RetrievalEvidence {
  citationId: string;
  title: string;
  effectiveUpdatedAt: string;
  dateSource: DateSource;
  sectionType: SectionType;
  locator: string;
  relativePath: string;
  absolutePath: string;
  sourceLink: CitationSourceLink;
  content: string;
  indexedContentHash: string;
}

export interface RetrievalBundle {
  kind: "drag_retrieval_bundle_v1";
  trust: "untrusted_reference_data";
  query: string;
  indexRevision: number;
  actualMode: "lexical" | "hybrid";
  generatedAt: string;
  truncated: boolean;
  characterCount: number;
  evidence: RetrievalEvidence[];
  search: SearchResponse;
}

export type IndexPhase = "idle" | "discover" | "extract" | "chunk" | "index" | "pausing" | "paused" | "complete" | "failed";

export interface IndexRunSummary {
  runId: string;
  phase: IndexPhase;
  startedAt: string;
  finishedAt: string | null;
  discovered: number;
  indexed: number;
  unchanged: number;
  skipped: number;
  failed: number;
  deleted: number;
  currentPath: string | null;
  error: string | null;
}

export interface IndexIssue {
  path: string;
  sourceId: string;
  code: string;
  message: string;
  occurredAt: string;
}

export interface IndexBackendMetrics {
  backend: "go";
  backendVersion: string;
  protocolVersion: number;
  wallClockMs: number;
  discoverMs: number;
  extractAndIndexMs: number;
  finalizeMs: number;
  bytesRead: number;
  peakHeapAllocBytes: number;
  peakHeapSystemBytes: number;
  peakGoroutines: number;
  workerCount: number;
  documentsPerSecond: number;
  chunksWritten: number;
  fallbackDocuments: number;
  workerTaskMsTotal: number;
  maxTaskMs: number;
  fallbackTaskMsTotal: number;
  sqliteWriteMs: number;
  peakWorkingSetBytes: number;
  cpuTimeMs: number;
  peakCpuPercent: number;
}

export interface IndexBackendStatus {
  engine: "go";
  binaryPath: string;
  running: boolean;
  pid: number | null;
  protocolVersion: number;
  backendVersion: string | null;
  platform: string | null;
  arch: string | null;
  capabilities: string[];
  lastMetrics: IndexBackendMetrics | null;
}

export interface IndexStatus {
  databasePath: string;
  configPath: string;
  indexRevision: number;
  fts5Available: boolean;
  trigramAvailable: boolean;
  documentCount: number;
  chunkCount: number;
  staleCount: number;
  sourceCounts: Record<string, number>;
  activeRun: IndexRunSummary | null;
  lastRun: IndexRunSummary | null;
  recentIssues: IndexIssue[];
  indexBackend?: IndexBackendStatus;
}

export interface AccountStatus {
  connected: boolean;
  authMode: string | null;
  planType: string | null;
  codexVersion: string | null;
  error: string | null;
}

export interface ModelReasoningOption {
  value: string;
  description: string;
}

export interface ModelOption {
  id: string;
  model: string;
  displayName: string;
  description: string;
  hidden: boolean;
  isDefault: boolean;
  defaultReasoningEffort: string;
  supportedReasoningEfforts: ModelReasoningOption[];
}

export interface RetrievalActivity {
  phase: "idle" | "searching" | "partial" | "complete" | "error";
  query: string | null;
  message: string;
  foundCount: number;
  startedAt: number | null;
}

export interface AppNotice {
  id: string;
  kind: "index-updated" | "info" | "warning";
  title: string;
  message: string;
  createdAt: number;
}

export interface ThreadSummary {
  id: string;
  title: string;
  preview: string;
  createdAt: number;
  updatedAt: number;
  active: boolean;
  archived: boolean;
}

export interface ChatCitation {
  citationId: string;
  label: string;
  title: string;
  relativePath: string;
  absolutePath: string;
  locator: string;
  sourceKind: SourceKind;
}

export interface ChatMessage {
  id: string;
  role: "user" | "assistant" | "system";
  text: string;
  createdAt: number;
  status: "complete" | "streaming" | "error";
  citationIds: string[];
  citations?: ChatCitation[];
}

export interface AppSnapshot {
  config: AppConfig;
  account: AccountStatus;
  index: IndexStatus;
  threads: ThreadSummary[];
  activeThreadId: string | null;
  messages: ChatMessage[];
  evidence: SearchResponse | null;
  retrieval: RetrievalActivity;
  models: ModelOption[];
  activeView: "chat" | "settings";
}

export type AppEvent =
  | { type: "snapshot"; snapshot: AppSnapshot }
  | { type: "index-progress"; run: IndexRunSummary }
  | { type: "account"; account: AccountStatus }
  | { type: "threads"; threads: ThreadSummary[]; activeThreadId: string | null }
  | { type: "messages"; messages: ChatMessage[] }
  | { type: "evidence"; evidence: SearchResponse | null }
  | { type: "retrieval"; retrieval: RetrievalActivity }
  | { type: "notice"; notice: AppNotice }
  | { type: "error"; message: string };

export interface CodexPreferences {
  model: string | null;
  reasoningEffort: string | null;
}

export interface OpenCitationResult {
  opened: boolean;
  method: "excel-range" | "pdf-page" | "default-app";
  absolutePath: string;
  locator: string;
  note: string | null;
}

export interface DragDesktopApi {
  getSnapshot(): Promise<AppSnapshot>;
  setActiveView(view: "chat" | "settings"): Promise<void>;
  saveConfig(config: AppConfig): Promise<AppConfig>;
  chooseSourceDirectory(sourceId: string): Promise<string | null>;
  chooseDirectory(): Promise<string | null>;
  resolveDroppedPath(file: unknown): Promise<string | null>;
  rebuildIndex(full?: boolean): Promise<IndexRunSummary>;
  pauseIndex(): Promise<IndexRunSummary | null>;
  resumeIndex(): Promise<IndexRunSummary | null>;
  clearIndexCache(): Promise<IndexStatus>;
  search(request: SearchRequest): Promise<SearchResponse>;
  createThread(): Promise<ThreadSummary>;
  selectThread(threadId: string): Promise<void>;
  archiveThread(threadId: string): Promise<void>;
  restoreThread(threadId: string): Promise<void>;
  deleteThread(threadId: string): Promise<void>;
  setCodexPreferences(preferences: CodexPreferences): Promise<AppConfig>;
  sendMessage(text: string, citationIds?: string[]): Promise<void>;
  stopTurn(): Promise<void>;
  loginWithChatGPT(): Promise<{ authUrl?: string; verificationUrl?: string; userCode?: string }>;
  openCitation(citationId: string): Promise<OpenCitationResult>;
  subscribe(listener: (event: AppEvent) => void): () => void;
}
