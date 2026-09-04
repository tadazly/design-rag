package core

import "time"

const (
	ProtocolVersion = 3
	BackendVersion  = "0.3.2"
)

type Source struct {
	ID                    string   `json:"id"`
	Label                 string   `json:"label"`
	Kind                  string   `json:"kind"`
	IndexIdentity         string   `json:"indexIdentity,omitempty"`
	RootPath              string   `json:"rootPath"`
	Enabled               bool     `json:"enabled"`
	IncludeExtensions     []string `json:"includeExtensions"`
	ExcludeDirectoryNames []string `json:"excludeDirectoryNames"`
	MaxFileBytes          int64    `json:"maxFileBytes"`
}

type IndexingConfig struct {
	AutomaticScan       bool `json:"automaticScan"`
	ScanIntervalMinutes int  `json:"scanIntervalMinutes"`
	Concurrency         int  `json:"concurrency"`
}

type EmbeddingConfig struct {
	Enabled   bool   `json:"enabled"`
	Provider  string `json:"provider"`
	Endpoint  string `json:"endpoint"`
	Model     string `json:"model"`
	TimeoutMS int    `json:"timeoutMs"`
}

type SearchConfig struct {
	DefaultSort      string          `json:"defaultSort"`
	DefaultLimit     int             `json:"defaultLimit"`
	MaxEvidenceChars int             `json:"maxEvidenceChars"`
	SynonymExpansion bool            `json:"synonymExpansion"`
	Embedding        EmbeddingConfig `json:"embedding"`
}

type CodexConfig struct {
	CodexPath       *string `json:"codexPath"`
	Model           *string `json:"model"`
	ReasoningEffort *string `json:"reasoningEffort"`
}

type AppConfig struct {
	SchemaVersion int            `json:"schemaVersion"`
	Sources       []Source       `json:"sources"`
	Search        SearchConfig   `json:"search"`
	Indexing      IndexingConfig `json:"indexing"`
	Codex         CodexConfig    `json:"codex"`
}

type IndexOptions struct {
	Full      bool     `json:"full"`
	SourceIDs []string `json:"sourceIds,omitempty"`
}

type IndexRequest struct {
	DatabasePath string       `json:"databasePath"`
	Config       AppConfig    `json:"config"`
	Options      IndexOptions `json:"options"`
}

type Candidate struct {
	SourceID          string `json:"sourceId"`
	SourceLabel       string `json:"sourceLabel"`
	SourceKind        string `json:"sourceKind"`
	SourceIdentity    string `json:"sourceIdentity"`
	RootPath          string `json:"rootPath"`
	AbsolutePath      string `json:"absolutePath"`
	ReadPath          string `json:"readPath,omitempty"`
	RelativePath      string `json:"relativePath"`
	Extension         string `json:"extension"`
	SizeBytes         int64  `json:"sizeBytes"`
	FilesystemMtimeMS int64  `json:"filesystemMtimeMs"`
}

type Block struct {
	Ordinal     int      `json:"ordinal"`
	Text        string   `json:"text"`
	HeadingPath []string `json:"headingPath"`
	SectionType string   `json:"sectionType"`
	Locator     string   `json:"locator"`
}

type DateEvidence struct {
	TimestampMS int64
	Strength    string
	Kind        string
	Locator     string
}

type ExtractedDocument struct {
	Title              string
	Blocks             []Block
	EmbeddedModifiedAt *time.Time
	Warnings           []string
	NeedsOCR           bool
	BytesRead          int64
	// Nil keeps the legacy text fallback; a non-nil empty slice is authoritative.
	DateEvidence []DateEvidence
}

type Chunk struct {
	Ordinal     int      `json:"ordinal"`
	Text        string   `json:"text"`
	HeadingPath []string `json:"headingPath"`
	SectionType string   `json:"sectionType"`
	Locator     string   `json:"locator"`
	ContentHash string   `json:"contentHash"`
	SearchTerms string   `json:"searchTerms,omitempty"`
}

type DateResolution struct {
	EffectiveUpdatedAtMS int64  `json:"effectiveUpdatedAtMs"`
	DateSource           string `json:"dateSource"`
}

type Draft struct {
	ID               string         `json:"id"`
	Candidate        Candidate      `json:"candidate"`
	Title            string         `json:"title"`
	FamilyKey        string         `json:"familyKey"`
	FamilyConfidence float64        `json:"familyConfidence"`
	ContentHash      string         `json:"contentHash"`
	Date             DateResolution `json:"date"`
	Chunks           []Chunk        `json:"chunks"`
	Warnings         []string       `json:"warnings"`
	NeedsOCR         bool           `json:"needsOcr"`
}

type RunSummary struct {
	RunID       string  `json:"runId"`
	Phase       string  `json:"phase"`
	StartedAt   string  `json:"startedAt"`
	FinishedAt  *string `json:"finishedAt"`
	Discovered  int     `json:"discovered"`
	Indexed     int     `json:"indexed"`
	Unchanged   int     `json:"unchanged"`
	Skipped     int     `json:"skipped"`
	Failed      int     `json:"failed"`
	Deleted     int     `json:"deleted"`
	CurrentPath *string `json:"currentPath"`
	Error       *string `json:"error"`
}

type Metrics struct {
	Backend             string  `json:"backend"`
	BackendVersion      string  `json:"backendVersion"`
	ProtocolVersion     int     `json:"protocolVersion"`
	WallClockMS         int64   `json:"wallClockMs"`
	DiscoverMS          int64   `json:"discoverMs"`
	ExtractAndIndexMS   int64   `json:"extractAndIndexMs"`
	FinalizeMS          int64   `json:"finalizeMs"`
	BytesRead           int64   `json:"bytesRead"`
	PeakHeapAllocBytes  uint64  `json:"peakHeapAllocBytes"`
	PeakHeapSystemBytes uint64  `json:"peakHeapSystemBytes"`
	PeakGoroutines      int     `json:"peakGoroutines"`
	WorkerCount         int     `json:"workerCount"`
	DocumentsPerSecond  float64 `json:"documentsPerSecond"`
	ChunksWritten       int64   `json:"chunksWritten"`
	FallbackDocuments   int     `json:"fallbackDocuments"`
	WorkerTaskMSTotal   int64   `json:"workerTaskMsTotal"`
	MaxTaskMS           int64   `json:"maxTaskMs"`
	FallbackTaskMSTotal int64   `json:"fallbackTaskMsTotal"`
	SQLiteWriteMS       int64   `json:"sqliteWriteMs"`
	PeakWorkingSetBytes uint64  `json:"peakWorkingSetBytes"`
	CPUTimeMS           int64   `json:"cpuTimeMs"`
	PeakCPUPercent      float64 `json:"peakCpuPercent"`
}

type IndexBackendHello struct {
	ProtocolVersion int      `json:"protocolVersion"`
	BackendVersion  string   `json:"backendVersion"`
	Platform        string   `json:"platform"`
	Arch            string   `json:"arch"`
	Capabilities    []string `json:"capabilities"`
}

type IndexBackendRun struct {
	RunID   string            `json:"runId"`
	Hello   IndexBackendHello `json:"hello"`
	Metrics *Metrics          `json:"metrics"`
}

type ExistingDocument struct {
	ID                string
	ContentHash       string
	SizeBytes         int64
	FilesystemMtimeMS int64
	Stale             bool
	Deleted           bool
	SourceIdentity    string
}

type Issue struct {
	SourceID string
	Path     string
	Code     string
	Message  string
}

type TaskResult struct {
	Candidate Candidate
	Existing  *ExistingDocument
	Draft     *Draft
	Unchanged bool
	Issue     *Issue
	BytesRead int64
	Fallback  bool
	ElapsedMS int64
}
