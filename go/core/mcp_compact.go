package core

import (
	"bytes"
	"encoding/json"
	"strings"
)

// MCP 结果面向模型阅读：只保留判断、回读和引用所需字段，避免同一正文和元数据在一次结果中重复出现。
// CLI 与桌面协议仍返回完整结构；这里的投影只用于 MCP 工具结果。

type compactExcerpt struct {
	CitationID string `json:"citationId"`
	Locator    string `json:"locator"`
	Section    string `json:"section,omitempty"`
	Heading    string `json:"heading,omitempty"`
	Hash       string `json:"hash"`
	Text       string `json:"text"`
}

type compactSearchHit struct {
	DocumentID string           `json:"documentId"`
	Title      string           `json:"title"`
	SourceKind string           `json:"sourceKind"`
	Date       string           `json:"date"`
	DateSource string           `json:"dateSource"`
	Path       string           `json:"path"`
	FamilyKey  string           `json:"familyKey,omitempty"`
	Relevance  float64          `json:"relevance"`
	Stale      bool             `json:"stale,omitempty"`
	Excerpts   []compactExcerpt `json:"excerpts"`
}

type compactSearchResponse struct {
	Query           string             `json:"query"`
	Sort            string             `json:"sort"`
	Mode            string             `json:"mode"`
	IndexRevision   int64              `json:"indexRevision"`
	TotalCandidates int                `json:"totalCandidates"`
	Warnings        []string           `json:"warnings,omitempty"`
	Hits            []compactSearchHit `json:"hits"`
}

type compactEvidenceChunk struct {
	CitationID string `json:"citationId"`
	Locator    string `json:"locator"`
	Section    string `json:"section,omitempty"`
	Heading    string `json:"heading,omitempty"`
	Hash       string `json:"hash"`
	Link       string `json:"link"`
	Content    string `json:"content"`
}

type compactEvidenceDocument struct {
	DocumentID string                 `json:"documentId"`
	Title      string                 `json:"title"`
	SourceKind string                 `json:"sourceKind"`
	Date       string                 `json:"date"`
	DateSource string                 `json:"dateSource"`
	Path       string                 `json:"path"`
	FamilyKey  string                 `json:"familyKey,omitempty"`
	Stale      bool                   `json:"stale,omitempty"`
	Chunks     []compactEvidenceChunk `json:"chunks"`
}

type compactCandidate struct {
	DocumentID string `json:"documentId"`
	Title      string `json:"title"`
	SourceKind string `json:"sourceKind"`
	Date       string `json:"date"`
}

type compactRetrievalBundle struct {
	Kind            string                    `json:"kind"`
	Trust           string                    `json:"trust"`
	Query           string                    `json:"query"`
	Mode            string                    `json:"mode"`
	IndexRevision   int64                     `json:"indexRevision"`
	Truncated       bool                      `json:"truncated"`
	CharacterCount  int                       `json:"characterCount"`
	Warnings        []string                  `json:"warnings,omitempty"`
	Documents       []compactEvidenceDocument `json:"documents"`
	OtherCandidates []compactCandidate        `json:"otherCandidates,omitempty"`
}

type compactCitationRead struct {
	CitationID           string `json:"citationId"`
	SourceKind           string `json:"sourceKind"`
	DocumentID           string `json:"documentId"`
	Path                 string `json:"path"`
	Locator              string `json:"locator"`
	Heading              string `json:"heading,omitempty"`
	Hash                 string `json:"hash"`
	Link                 string `json:"link"`
	IndexRevision        int64  `json:"indexRevision"`
	CurrentIndexRevision int64  `json:"currentIndexRevision"`
	Changed              bool   `json:"changed"`
	Stale                bool   `json:"stale,omitempty"`
	Content              string `json:"content"`
}

type compactVersion struct {
	DocumentID string `json:"documentId"`
	Title      string `json:"title"`
	SourceKind string `json:"sourceKind"`
	Date       string `json:"date"`
	DateSource string `json:"dateSource"`
	Path       string `json:"path"`
	Canonical  bool   `json:"canonical"`
	Stale      bool   `json:"stale,omitempty"`
}

type compactVersions struct {
	FamilyKey string           `json:"familyKey"`
	Versions  []compactVersion `json:"versions"`
}

// compactDate 去掉整日时间的零时刻后缀；带具体时刻的日期保留到秒。
func compactDate(value string) string {
	if strings.HasSuffix(value, "T00:00:00.000Z") {
		return strings.TrimSuffix(value, "T00:00:00.000Z")
	}
	if index := strings.Index(value, "."); index > 0 && strings.HasSuffix(value, "Z") {
		return value[:index] + "Z"
	}
	return value
}

func roundedRelevance(value float64) float64 {
	return float64(int(value*100+0.5)) / 100
}

func joinedHeading(headings []string) string {
	return strings.Join(headings, " > ")
}

func compactSearchResult(response SearchResponse) compactSearchResponse {
	result := compactSearchResponse{Query: response.Query, Sort: response.Sort, Mode: response.ActualMode, IndexRevision: response.IndexRevision, TotalCandidates: response.TotalCandidates, Warnings: response.Warnings, Hits: make([]compactSearchHit, 0, len(response.Hits))}
	for _, hit := range response.Hits {
		excerpts := make([]compactExcerpt, 0, len(hit.Excerpts))
		for _, excerpt := range hit.Excerpts {
			excerpts = append(excerpts, compactExcerpt{CitationID: excerpt.Citation.CitationID, Locator: excerpt.Locator, Section: excerpt.SectionType, Heading: joinedHeading(excerpt.HeadingPath), Hash: excerpt.Citation.IndexedContentHash, Text: excerpt.Text})
		}
		result.Hits = append(result.Hits, compactSearchHit{DocumentID: hit.DocumentID, Title: hit.Title, SourceKind: hit.SourceKind, Date: compactDate(hit.EffectiveUpdatedAt), DateSource: hit.DateSource, Path: hit.AbsolutePath, FamilyKey: hit.FamilyKey, Relevance: roundedRelevance(hit.Relevance), Stale: hit.Stale, Excerpts: excerpts})
	}
	return result
}

func compactRetrievalResult(bundle RetrievalBundle) compactRetrievalBundle {
	result := compactRetrievalBundle{Kind: "drag_retrieval_bundle_v2", Trust: bundle.Trust, Query: bundle.Query, Mode: bundle.ActualMode, IndexRevision: bundle.IndexRevision, Truncated: bundle.Truncated, CharacterCount: bundle.CharacterCount, Warnings: bundle.Search.Warnings, Documents: []compactEvidenceDocument{}}
	hits := map[string]SearchHit{}
	for _, hit := range bundle.Search.Hits {
		hits[hit.DocumentID] = hit
	}
	documentIndex := map[string]int{}
	chunkDocuments := map[string]string{}
	chunkHeadings := map[string]string{}
	for _, hit := range bundle.Search.Hits {
		for _, excerpt := range hit.Excerpts {
			chunkDocuments[excerpt.Citation.CitationID] = hit.DocumentID
			chunkHeadings[excerpt.Citation.CitationID] = joinedHeading(excerpt.HeadingPath)
		}
	}
	for _, evidence := range bundle.Evidence {
		documentID := chunkDocuments[evidence.CitationID]
		key := documentID
		if key == "" {
			key = evidence.AbsolutePath
		}
		index, ok := documentIndex[key]
		if !ok {
			hit := hits[documentID]
			index = len(result.Documents)
			documentIndex[key] = index
			result.Documents = append(result.Documents, compactEvidenceDocument{DocumentID: documentID, Title: evidence.Title, SourceKind: hit.SourceKind, Date: compactDate(evidence.EffectiveUpdatedAt), DateSource: evidence.DateSource, Path: evidence.AbsolutePath, FamilyKey: hit.FamilyKey, Stale: hit.Stale, Chunks: []compactEvidenceChunk{}})
		}
		result.Documents[index].Chunks = append(result.Documents[index].Chunks, compactEvidenceChunk{CitationID: evidence.CitationID, Locator: evidence.Locator, Section: evidence.SectionType, Heading: evidenceHeading(chunkHeadings[evidence.CitationID], evidence.Locator), Hash: evidence.IndexedContentHash, Link: evidence.SourceLink.Markdown, Content: evidence.Content})
	}
	for _, hit := range bundle.Search.Hits {
		if _, ok := documentIndex[hit.DocumentID]; ok {
			continue
		}
		result.OtherCandidates = append(result.OtherCandidates, compactCandidate{DocumentID: hit.DocumentID, Title: hit.Title, SourceKind: hit.SourceKind, Date: compactDate(hit.EffectiveUpdatedAt)})
	}
	return result
}

// evidenceHeading 返回片段所在的章节标题；表格 locator 已以 sheet 名开头时不再重复。
// docx、Markdown 等正文片段的 locator 只有行号或段落号，章节标题是理解片段的必要上下文。
func evidenceHeading(heading, locator string) string {
	if heading == "" || strings.HasPrefix(locator, heading+"!") {
		return ""
	}
	return heading
}

func compactCitationResult(read CitationReadResult) compactCitationRead {
	citation := read.Citation
	return compactCitationRead{CitationID: citation.CitationID, SourceKind: citation.SourceKind, DocumentID: citation.DocumentID, Path: citation.AbsolutePath, Locator: citation.Locator, Heading: joinedHeading(citation.HeadingPath), Hash: citation.IndexedContentHash, Link: citation.SourceLink.Markdown, IndexRevision: citation.IndexRevision, CurrentIndexRevision: read.CurrentIndexRevision, Changed: read.Changed, Stale: citation.Stale, Content: read.Content}
}

func compactVersionsResult(familyKey string, entries []VersionEntry) compactVersions {
	result := compactVersions{FamilyKey: familyKey, Versions: make([]compactVersion, 0, len(entries))}
	for _, entry := range entries {
		if result.FamilyKey == "" {
			result.FamilyKey = entry.FamilyKey
		}
		result.Versions = append(result.Versions, compactVersion{DocumentID: entry.DocumentID, Title: entry.Title, SourceKind: entry.SourceKind, Date: compactDate(entry.EffectiveUpdatedAt), DateSource: entry.DateSource, Path: entry.RelativePath, Canonical: entry.Canonical, Stale: entry.Stale})
	}
	return result
}

// marshalToolText 不转义 HTML 字符，保持 Markdown 链接 `<...>` 原样可读。
func marshalToolText(value any) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		raw, _ := json.Marshal(value)
		return string(raw)
	}
	return strings.TrimSuffix(buffer.String(), "\n")
}
