package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

type CitationSourceLink struct {
	FileName     string `json:"fileName"`
	Label        string `json:"label"`
	AbsolutePath string `json:"absolutePath"`
	Locator      string `json:"locator"`
	Markdown     string `json:"markdown"`
}

type Citation struct {
	CitationID         string             `json:"citationId"`
	Display            string             `json:"display"`
	SourceID           string             `json:"sourceId"`
	SourceLabel        string             `json:"sourceLabel"`
	SourceKind         string             `json:"sourceKind"`
	AbsolutePath       string             `json:"absolutePath"`
	RelativePath       string             `json:"relativePath"`
	DocumentID         string             `json:"documentId"`
	ChunkID            string             `json:"chunkId"`
	Locator            string             `json:"locator"`
	HeadingPath        []string           `json:"headingPath"`
	IndexedContentHash string             `json:"indexedContentHash"`
	IndexRevision      int64              `json:"indexRevision"`
	Stale              bool               `json:"stale"`
	SourceLink         CitationSourceLink `json:"sourceLink"`
}

type SearchExcerpt struct {
	ChunkID         string   `json:"chunkId"`
	SectionType     string   `json:"sectionType"`
	HeadingPath     []string `json:"headingPath"`
	Locator         string   `json:"locator"`
	Text            string   `json:"text"`
	HighlightedText string   `json:"highlightedText"`
	Score           float64  `json:"score"`
	Citation        Citation `json:"citation"`
}

type SearchHit struct {
	DocumentID           string          `json:"documentId"`
	SourceID             string          `json:"sourceId"`
	SourceLabel          string          `json:"sourceLabel"`
	SourceKind           string          `json:"sourceKind"`
	Title                string          `json:"title"`
	AbsolutePath         string          `json:"absolutePath"`
	RelativePath         string          `json:"relativePath"`
	Extension            string          `json:"extension"`
	EffectiveUpdatedAt   string          `json:"effectiveUpdatedAt"`
	DateSource           string          `json:"dateSource"`
	FilesystemModifiedAt string          `json:"filesystemModifiedAt"`
	Relevance            float64         `json:"relevance"`
	FamilyKey            string          `json:"familyKey"`
	FamilyConfidence     float64         `json:"familyConfidence"`
	Stale                bool            `json:"stale"`
	SectionTypes         []string        `json:"sectionTypes"`
	Excerpts             []SearchExcerpt `json:"excerpts"`
}

type SearchRequest struct {
	Query           string   `json:"query"`
	SourceIDs       []string `json:"sourceIds,omitempty"`
	SourceKinds     []string `json:"sourceKinds,omitempty"`
	SectionTypes    []string `json:"sectionTypes,omitempty"`
	Extensions      []string `json:"extensions,omitempty"`
	UpdatedAfter    string   `json:"updatedAfter,omitempty"`
	UpdatedBefore   string   `json:"updatedBefore,omitempty"`
	RetrievalMode   string   `json:"retrievalMode,omitempty"`
	Sort            string   `json:"sort,omitempty"`
	LatestPerFamily bool     `json:"latestPerFamily,omitempty"`
	Limit           int      `json:"limit,omitempty"`
}

type SearchResponse struct {
	Query            string      `json:"query"`
	ExpandedTerms    []string    `json:"expandedTerms"`
	RequestedMode    string      `json:"requestedMode"`
	ActualMode       string      `json:"actualMode"`
	SemanticUsed     bool        `json:"semanticUsed"`
	SemanticCoverage float64     `json:"semanticCoverage"`
	Sort             string      `json:"sort"`
	IndexRevision    int64       `json:"indexRevision"`
	TotalCandidates  int         `json:"totalCandidates"`
	TookMS           float64     `json:"tookMs"`
	Hits             []SearchHit `json:"hits"`
	Warnings         []string    `json:"warnings"`
}

type RetrievalRequest struct {
	SearchRequest
	DocumentIDs          []string `json:"documentIds,omitempty"`
	MaxDocuments         int      `json:"maxDocuments,omitempty"`
	MaxChunksPerDocument int      `json:"maxChunksPerDocument,omitempty"`
	MaxChars             int      `json:"maxChars,omitempty"`
}

func (request *RetrievalRequest) UnmarshalJSON(data []byte) error {
	type wire struct {
		Query                string   `json:"query"`
		SourceIDs            []string `json:"sourceIds"`
		SourceKinds          []string `json:"sourceKinds"`
		SectionTypes         []string `json:"sectionTypes"`
		Extensions           []string `json:"extensions"`
		UpdatedAfter         string   `json:"updatedAfter"`
		UpdatedBefore        string   `json:"updatedBefore"`
		RetrievalMode        string   `json:"retrievalMode"`
		Sort                 string   `json:"sort"`
		LatestPerFamily      bool     `json:"latestPerFamily"`
		Limit                int      `json:"limit"`
		DocumentIDs          []string `json:"documentIds"`
		MaxDocuments         int      `json:"maxDocuments"`
		MaxChunksPerDocument int      `json:"maxChunksPerDocument"`
		MaxChars             int      `json:"maxChars"`
	}
	var value wire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("retrieve 参数含尾随 JSON 内容")
	}
	request.SearchRequest = SearchRequest{Query: value.Query, SourceIDs: value.SourceIDs, SourceKinds: value.SourceKinds, SectionTypes: value.SectionTypes, Extensions: value.Extensions, UpdatedAfter: value.UpdatedAfter, UpdatedBefore: value.UpdatedBefore, RetrievalMode: value.RetrievalMode, Sort: value.Sort, LatestPerFamily: value.LatestPerFamily, Limit: value.Limit}
	request.DocumentIDs = value.DocumentIDs
	request.MaxDocuments = value.MaxDocuments
	request.MaxChunksPerDocument = value.MaxChunksPerDocument
	request.MaxChars = value.MaxChars
	return nil
}

type RetrievalEvidence struct {
	CitationID         string             `json:"citationId"`
	Title              string             `json:"title"`
	EffectiveUpdatedAt string             `json:"effectiveUpdatedAt"`
	DateSource         string             `json:"dateSource"`
	SectionType        string             `json:"sectionType"`
	Locator            string             `json:"locator"`
	RelativePath       string             `json:"relativePath"`
	AbsolutePath       string             `json:"absolutePath"`
	SourceLink         CitationSourceLink `json:"sourceLink"`
	Content            string             `json:"content"`
	IndexedContentHash string             `json:"indexedContentHash"`
}

type RetrievalBundle struct {
	Kind           string              `json:"kind"`
	Trust          string              `json:"trust"`
	Query          string              `json:"query"`
	IndexRevision  int64               `json:"indexRevision"`
	ActualMode     string              `json:"actualMode"`
	GeneratedAt    string              `json:"generatedAt"`
	Truncated      bool                `json:"truncated"`
	CharacterCount int                 `json:"characterCount"`
	Evidence       []RetrievalEvidence `json:"evidence"`
	Search         SearchResponse      `json:"search"`
}

type CitationReadResult struct {
	Citation             Citation `json:"citation"`
	Content              string   `json:"content"`
	Changed              bool     `json:"changed"`
	CurrentIndexRevision int64    `json:"currentIndexRevision"`
}

type VersionEntry struct {
	DocumentID         string  `json:"documentId"`
	SourceID           string  `json:"sourceId"`
	SourceLabel        string  `json:"sourceLabel"`
	SourceKind         string  `json:"sourceKind"`
	Title              string  `json:"title"`
	EffectiveUpdatedAt string  `json:"effectiveUpdatedAt"`
	DateSource         string  `json:"dateSource"`
	RelativePath       string  `json:"relativePath"`
	FamilyKey          string  `json:"familyKey"`
	FamilyConfidence   float64 `json:"familyConfidence"`
	Canonical          bool    `json:"canonical"`
	Stale              bool    `json:"stale"`
}
