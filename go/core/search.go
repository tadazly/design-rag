package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

type scoredCandidate struct {
	row           LexicalCandidateRow
	score         float64
	semanticScore float64
	matchedTerms  []string
}

type normalizedCandidateFields struct {
	title        string
	heading      string
	relativePath string
	text         string
	haystack     string
}

var errIndexChangedDuringRead = errors.New("索引在读取期间发生变化")

func searchConfigSignature(config AppConfig) string {
	raw, _ := json.Marshal(config)
	return string(raw)
}

type searchCandidateScope struct {
	DocumentIDs       []string
	ChunksPerDocument int
}

type documentIdentityGroup struct {
	Phrase string
	Terms  []string
}

type queryAnchorSignalSet struct {
	ExplicitAnchors []string
	DocumentAnchors []string
	IdentityGroups  []documentIdentityGroup
	LatestIntent    bool
}

type documentRankContext struct {
	Query   string
	Terms   []string
	Signals queryAnchorSignalSet
}

type SearchEngine struct {
	database      *IndexDatabase
	getConfig     func() AppConfig
	refreshConfig func() error
}

func (engine *SearchEngine) refresh() error {
	if engine.refreshConfig != nil {
		return engine.refreshConfig()
	}
	return nil
}

func NewSearchEngine(database *IndexDatabase, getConfig func() AppConfig) *SearchEngine {
	return &SearchEngine{database: database, getConfig: getConfig}
}

func safeHeadingPath(value string) []string {
	result := []string{}
	_ = json.Unmarshal([]byte(value), &result)
	return result
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func parseSearchDate(value string) (int64, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UnixMilli(), nil
		}
	}
	return 0, fmt.Errorf("日期筛选格式无效：%s", value)
}

func matchesFilters(row LexicalCandidateRow, request SearchRequest) (bool, error) {
	if len(request.SourceIDs) > 0 && !containsString(request.SourceIDs, row.SourceID) {
		return false, nil
	}
	if len(request.SourceKinds) > 0 && !containsString(request.SourceKinds, row.SourceKind) {
		return false, nil
	}
	if len(request.SectionTypes) > 0 && !containsString(request.SectionTypes, row.SectionType) {
		return false, nil
	}
	if len(request.Extensions) > 0 {
		matched := false
		for _, extension := range request.Extensions {
			if strings.EqualFold(extension, row.Extension) {
				matched = true
				break
			}
		}
		if !matched {
			return false, nil
		}
	}
	if request.UpdatedAfter != "" {
		value, err := parseSearchDate(request.UpdatedAfter)
		if err != nil {
			return false, err
		}
		if row.EffectiveUpdatedAtMS < value {
			return false, nil
		}
	}
	if request.UpdatedBefore != "" {
		value, err := parseSearchDate(request.UpdatedBefore)
		if err != nil {
			return false, err
		}
		if row.EffectiveUpdatedAtMS > value {
			return false, nil
		}
	}
	return true, nil
}

func matchesConfiguredSource(row LexicalCandidateRow, sources map[string]Source) bool {
	source, ok := sources[row.SourceID]
	return ok && source.Enabled && row.SourceIdentity == SourceIndexIdentity(source)
}

func normalizeCandidateFields(row LexicalCandidateRow) normalizedCandidateFields {
	title := NormalizeText(row.Title)
	heading := NormalizeText(strings.Join(safeHeadingPath(row.HeadingPathJSON), " "))
	relativePath := NormalizeText(row.RelativePath)
	text := NormalizeText(row.Text)
	return normalizedCandidateFields{title: title, heading: heading, relativePath: relativePath, text: text, haystack: strings.Join([]string{title, relativePath, heading, text}, "\n")}
}

func scoreSearchCandidate(row LexicalCandidateRow, normalized normalizedCandidateFields, terms []string, semanticScore float64) scoredCandidate {
	matched := []string{}
	for _, term := range terms {
		if strings.Contains(normalized.title, term) || strings.Contains(normalized.heading, term) || strings.Contains(normalized.relativePath, term) || strings.Contains(normalized.text, term) {
			matched = append(matched, term)
		}
	}
	coverage := float64(len(matched)) / float64(max(1, len(terms)))
	score := 0.18 + coverage*0.3
	for _, term := range matched {
		if normalized.title == term {
			score += 0.24
		} else if strings.Contains(normalized.title, term) {
			score += 0.16
		}
		if strings.Contains(normalized.heading, term) {
			score += 0.1
		}
		if strings.Contains(normalized.relativePath, term) {
			score += 0.08
		}
		if strings.Contains(normalized.text, term) {
			score += 0.04
		}
	}
	score += 1 / (1 + math.Max(0, math.Abs(row.LexicalRank))) * 0.16
	if strings.Contains(row.Title, "复用") || strings.Contains(row.RelativePath, "复用") {
		score += 0.08
	}
	if semanticScore > 0 {
		score = score*0.72 + semanticScore*0.28
	}
	return scoredCandidate{row: row, score: math.Min(1, score), semanticScore: semanticScore, matchedTerms: matched}
}

var (
	activityEntityPattern = regexp.MustCompile(`([\p{Han}·•・:_-]{2,24})[\s·•・:_-]*(\d{2,})`)
	asciiAnchorPattern    = regexp.MustCompile(`(?i)[a-z0-9_./:-]{2,}`)
	tableIntentPattern    = regexp.MustCompile(`(?i)(配表|配置表|哪些表|表格|字段|参数|前端模块|后台模块)`)
	latestIntentPattern   = regexp.MustCompile(`(?i)(最新|最近|\blatest\b)`)
	uppercasePattern      = regexp.MustCompile(`[A-Z]`)
	documentMarkPattern   = regexp.MustCompile(`[\d_./:-]`)
	numericPattern        = regexp.MustCompile(`^\d+$`)
)

var activityEntityLeadWords = []string{
	"帮我", "给我", "我要", "我想", "想要", "需要", "找到", "找出", "查找", "查询", "看看",
	"最新", "最近", "复用", "沿用", "套用", "一个", "这个", "那个", "关于", "分析", "说明", "请", "的",
}

var genericActivityEntityNames = map[string]bool{"活动": true, "玩法": true, "配置": true, "配表": true, "表格": true, "任务": true, "奖励": true, "版本": true, "最新": true, "最近": true}

type activityAuxiliaryRole struct{ title, query *regexp.Regexp }

var activityAuxiliaryRoles = []activityAuxiliaryRole{
	{regexp.MustCompile(`(?i)(累充|充值)`), regexp.MustCompile(`(?i)(累充|充值)`)},
	{regexp.MustCompile(`(?i)(玩法内容|玩法设计|表格设计)`), regexp.MustCompile(`(?i)(玩法内容|玩法设计|表格设计|配表)`)},
	{regexp.MustCompile(`(?i)(数组特效|特效设计)`), regexp.MustCompile(`(?i)(数组特效|特效设计)`)},
	{regexp.MustCompile(`(?i)(商业数值|数值模型)`), regexp.MustCompile(`(?i)(商业数值|数值模型)`)},
	{regexp.MustCompile(`(?i)复用`), regexp.MustCompile(`(?i)(复用|沿用|套用)`)},
}

func init() {
	sort.SliceStable(activityEntityLeadWords, func(i, j int) bool {
		return len([]rune(activityEntityLeadWords[i])) > len([]rune(activityEntityLeadWords[j]))
	})
}

func compactIdentity(value string) string {
	var builder strings.Builder
	for _, r := range NormalizeText(value) {
		if isCJK(r) || unicode.IsLetter(r) || unicode.IsNumber(r) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func trimActivityEntityLead(value string) string {
	result := strings.Trim(NormalizeText(value), " \t\r\n·•・:_-")
	for changed := true; changed && result != ""; {
		changed = false
		for _, word := range activityEntityLeadWords {
			if strings.HasPrefix(result, word) {
				result = strings.TrimLeft(strings.TrimPrefix(result, word), " \t\r\n·•・:_-")
				changed = true
				break
			}
		}
	}
	return compactIdentity(result)
}

func extractDocumentIdentityGroups(query string) []documentIdentityGroup {
	seen := map[string]bool{}
	result := []documentIdentityGroup{}
	for _, match := range activityEntityPattern.FindAllStringSubmatch(query, -1) {
		entity := trimActivityEntityLead(match[1])
		numeric := NormalizeText(match[2])
		if len([]rune(entity)) < 2 || genericActivityEntityNames[entity] || numeric == "" {
			continue
		}
		phrase := entity + numeric
		if !seen[phrase] {
			seen[phrase] = true
			result = append(result, documentIdentityGroup{Phrase: phrase, Terms: []string{entity, numeric}})
		}
	}
	return result
}

func identityGroupScore(value string, group documentIdentityGroup) float64 {
	identity := compactIdentity(value)
	if identity == "" {
		return 0
	}
	if strings.Contains(identity, group.Phrase) {
		return 4
	}
	cursor, first, last := 0, -1, -1
	for _, term := range group.Terms {
		position := strings.Index(identity[cursor:], term)
		if position < 0 {
			return 0
		}
		position += cursor
		if first < 0 {
			first = position
		}
		last = position + len(term)
		cursor = last
	}
	termLength := 0
	for _, term := range group.Terms {
		termLength += len(term)
	}
	gap := max(0, last-first-termLength)
	return math.Max(2, 3.5-math.Min(1.5, float64(gap)/8))
}

func namedDocumentIdentityScore(title, relativePath string, groups []documentIdentityGroup) float64 {
	best := 0.0
	for _, group := range groups {
		titleScore := identityGroupScore(title, group)
		pathScore := identityGroupScore(relativePath, group)
		if titleScore > 0 {
			titleScore += 0.25
		}
		best = math.Max(best, math.Max(titleScore, pathScore))
	}
	return best
}

func QueryAnchorSignals(query string) queryAnchorSignalSet {
	raw := asciiAnchorPattern.FindAllString(query, -1)
	latest := latestIntentPattern.MatchString(query)
	explicit := []string{}
	document := []string{}
	for _, anchor := range raw {
		if latest && NormalizeText(anchor) == "latest" {
			continue
		}
		normalized := NormalizeText(anchor)
		explicit = appendUnique(explicit, normalized)
		if uppercasePattern.MatchString(anchor) || documentMarkPattern.MatchString(anchor) || len(anchor) >= 8 {
			document = appendUnique(document, normalized)
		}
	}
	return queryAnchorSignalSet{ExplicitAnchors: explicit, DocumentAnchors: document, IdentityGroups: extractDocumentIdentityGroups(query), LatestIntent: latest}
}

func matchesDocumentIdentity(title, relativePath string, anchors []string) bool {
	identity := NormalizeText(title + "\n" + relativePath)
	for _, anchor := range anchors {
		if strings.Contains(identity, anchor) {
			return true
		}
	}
	return false
}

func matchesIdentitySignals(title, relativePath string, signals queryAnchorSignalSet) bool {
	if len(signals.IdentityGroups) > 0 {
		return namedDocumentIdentityScore(title, relativePath, signals.IdentityGroups) > 0
	}
	return matchesDocumentIdentity(title, relativePath, signals.DocumentAnchors)
}

func documentIdentityRoleScore(hit SearchHit, context documentRankContext) float64 {
	if len(context.Signals.IdentityGroups) == 0 && !(context.Signals.LatestIntent && len(context.Signals.DocumentAnchors) > 0) {
		return 0
	}
	title := NormalizeText(hit.Title)
	path := NormalizeText(hit.RelativePath)
	identity := title + "\n" + path
	query := NormalizeText(context.Query)
	score := namedDocumentIdentityScore(hit.Title, hit.RelativePath, context.Signals.IdentityGroups) * 4
	for _, anchor := range context.Signals.DocumentAnchors {
		if strings.Contains(title, anchor) {
			score++
		} else if strings.Contains(path, anchor) {
			score += 0.25
		}
	}
	for _, term := range context.Terms {
		if strings.Contains(identity, term) {
			score += math.Min(10, float64(len([]rune(term)))) * 0.08
		}
	}
	if (len(context.Signals.IdentityGroups) > 0 || strings.Contains(query, "活动")) && strings.Contains(title, "活动") {
		score++
	}
	for _, role := range activityAuxiliaryRoles {
		if role.title.MatchString(title) {
			if role.query.MatchString(query) {
				score += 0.6
			} else {
				score -= 0.45
			}
		}
	}
	return score
}

func hitHaystack(hit SearchHit) string {
	parts := []string{hit.Title, hit.RelativePath}
	for _, excerpt := range hit.Excerpts {
		parts = append(parts, strings.Join(excerpt.HeadingPath, " / "), excerpt.Text)
	}
	return NormalizeText(strings.Join(parts, "\n"))
}

func matchesQuerySignalsInHaystack(haystack string, primaryConcept, explicitAnchors, documentAnchors []string) bool {
	if len(primaryConcept) == 0 && len(explicitAnchors) == 0 {
		return true
	}
	matchesDocumentAnchor := false
	for _, anchor := range documentAnchors {
		if strings.Contains(haystack, anchor) {
			matchesDocumentAnchor = true
			break
		}
	}
	matchesConcept := matchesDocumentAnchor || len(primaryConcept) == 0
	for _, term := range primaryConcept {
		if strings.Contains(haystack, term) {
			matchesConcept = true
			break
		}
	}
	if !matchesConcept || (len(documentAnchors) > 0 && !matchesDocumentAnchor) {
		return false
	}
	for _, anchor := range explicitAnchors {
		if containsString(documentAnchors, anchor) {
			continue
		}
		if !strings.Contains(haystack, anchor) {
			return false
		}
	}
	return true
}

func cosineSimilarity(left, right []float64) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return 0
	}
	dot, leftNorm, rightNorm := 0.0, 0.0, 0.0
	for index := range left {
		dot += left[index] * right[index]
		leftNorm += left[index] * left[index]
		rightNorm += right[index] * right[index]
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return dot / math.Sqrt(leftNorm*rightNorm)
}

func nonZeroVector(value []float64) bool {
	for _, component := range value {
		if component != 0 {
			return true
		}
	}
	return false
}

func ollamaEmbed(ctx context.Context, config EmbeddingConfig, input []string) ([][]float64, error) {
	parsed, err := url.Parse(config.Endpoint)
	hostname := ""
	if parsed != nil {
		hostname = strings.ToLower(parsed.Hostname())
	}
	loopback := hostname == "localhost"
	if parsedIP := net.ParseIP(hostname); parsedIP != nil {
		loopback = parsedIP.IsLoopback()
	}
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || !loopback {
		return nil, fmt.Errorf("Ollama endpoint 无效")
	}
	raw, _ := json.Marshal(map[string]any{"model": config.Model, "input": input, "truncate": true})
	timeout := time.Duration(config.TimeoutMS) * time.Millisecond
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, config.Endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	request.Header.Set("content-type", "application/json")
	client := &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Ollama embedding 失败：HTTP %d", response.StatusCode)
	}
	var payload struct {
		Embeddings [][]float64 `json:"embeddings"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if len(payload.Embeddings) != len(input) {
		return nil, fmt.Errorf("Ollama embedding 返回数量不匹配")
	}
	dimension := 0
	for vectorIndex, vector := range payload.Embeddings {
		if len(vector) == 0 || (dimension != 0 && len(vector) != dimension) {
			return nil, fmt.Errorf("Ollama embedding 维度无效")
		}
		dimension = len(vector)
		norm := 0.0
		for _, component := range vector {
			if math.IsNaN(component) || math.IsInf(component, 0) {
				return nil, fmt.Errorf("Ollama embedding 包含非有限值")
			}
			norm += component * component
		}
		if norm == 0 && vectorIndex == 0 {
			return nil, fmt.Errorf("Ollama query embedding 是零向量")
		}
	}
	return payload.Embeddings, nil
}

func (engine *SearchEngine) Search(ctx context.Context, request SearchRequest) (SearchResponse, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if err := engine.refresh(); err != nil {
			return SearchResponse{}, err
		}
		configSignature := searchConfigSignature(engine.getConfig())
		response, err := engine.search(ctx, request, nil, 3)
		if err != nil {
			return response, err
		}
		if err := engine.refresh(); err != nil {
			return SearchResponse{}, err
		}
		currentRevision, revisionErr := engine.database.Revision()
		if revisionErr != nil {
			return SearchResponse{}, revisionErr
		}
		if currentRevision == response.IndexRevision && searchConfigSignature(engine.getConfig()) == configSignature {
			return response, nil
		}
	}
	return SearchResponse{}, fmt.Errorf("%w，请重试", errIndexChangedDuringRead)
}

func (engine *SearchEngine) search(ctx context.Context, request SearchRequest, scope *searchCandidateScope, excerptLimit int) (SearchResponse, error) {
	started := time.Now()
	snapshotRevision, err := engine.database.Revision()
	if err != nil {
		return SearchResponse{}, err
	}
	config := engine.getConfig()
	query := strings.TrimSpace(request.Query)
	if query == "" {
		return SearchResponse{}, fmt.Errorf("query 不能为空")
	}
	requestedMode := request.RetrievalMode
	if requestedMode == "" {
		requestedMode = "auto"
	}
	if requestedMode != "auto" && requestedMode != "lexical" && requestedMode != "semantic" && requestedMode != "hybrid" {
		return SearchResponse{}, fmt.Errorf("retrievalMode 无效：%s", requestedMode)
	}
	sortMode := request.Sort
	if sortMode == "" {
		sortMode = config.Search.DefaultSort
	}
	if sortMode != "newest" && sortMode != "relevance" && sortMode != "hybrid" {
		return SearchResponse{}, fmt.Errorf("sort 无效：%s", sortMode)
	}
	limit := request.Limit
	if limit == 0 {
		limit = config.Search.DefaultLimit
	}
	limit = min(100, max(1, limit))
	excerptLimit = min(10, max(1, excerptLimit))
	expandedTerms := ExpandQueryTerms(query, config.Search.SynonymExpansion)
	requestedSourceIDs := map[string]bool{}
	for _, value := range request.SourceIDs {
		requestedSourceIDs[value] = true
	}
	requestedSourceKinds := map[string]bool{}
	for _, value := range request.SourceKinds {
		requestedSourceKinds[value] = true
	}
	eligible := []Source{}
	for _, source := range config.Sources {
		if !source.Enabled || (len(requestedSourceIDs) > 0 && !requestedSourceIDs[source.ID]) || (len(requestedSourceKinds) > 0 && !requestedSourceKinds[source.Kind]) {
			continue
		}
		eligible = append(eligible, source)
	}
	response := SearchResponse{Query: query, ExpandedTerms: expandedTerms, RequestedMode: requestedMode, ActualMode: "lexical", Sort: sortMode, IndexRevision: snapshotRevision, Hits: []SearchHit{}, Warnings: []string{}}
	if len(eligible) == 0 {
		response.Warnings = append(response.Warnings, "当前没有符合筛选条件的已启用资料源")
		response.TookMS = roundedMilliseconds(time.Since(started))
		return response, nil
	}
	eligibleIDs := make([]string, len(eligible))
	eligibleByID := map[string]Source{}
	scopes := make([]SourceIdentityScope, len(eligible))
	designOnly := true
	for index, source := range eligible {
		eligibleIDs[index] = source.ID
		eligibleByID[source.ID] = source
		scopes[index] = SourceIdentityScope{SourceID: source.ID, SourceIdentity: SourceIndexIdentity(source)}
		if source.Kind != "design" {
			designOnly = false
		}
	}
	effectiveRequest := request
	effectiveRequest.SourceIDs = eligibleIDs
	lexicalTokens := []string{}
	for _, term := range expandedTerms {
		for _, token := range CJKSearchTerms(term) {
			if len([]rune(token)) >= 2 {
				lexicalTokens = appendUnique(lexicalTokens, token)
				if len(lexicalTokens) >= 80 {
					break
				}
			}
		}
	}
	lexicalParts := make([]string, len(lexicalTokens))
	for index, token := range lexicalTokens {
		lexicalParts[index] = EscapeFTSToken(token)
	}
	trigramTerms := []string{}
	for _, term := range expandedTerms {
		if len([]rune(term)) >= 3 && len(trigramTerms) < 24 {
			trigramTerms = append(trigramTerms, EscapeFTSToken(term))
		}
	}
	conceptGroups := QueryConceptGroups(query)
	var primaryConcept []string
	if len(conceptGroups) > 0 {
		primaryConcept = conceptGroups[0]
	}
	signals := QueryAnchorSignals(query)
	normalizedCache := map[string]normalizedCandidateFields{}
	normalizedFor := func(row LexicalCandidateRow) normalizedCandidateFields {
		if cached, ok := normalizedCache[row.ChunkID]; ok {
			return cached
		}
		value := normalizeCandidateFields(row)
		normalizedCache[row.ChunkID] = value
		return value
	}
	tableIntent := tableIntentPattern.MatchString(query)
	indexedLimit := min(1200, max(320, limit*40))
	if tableIntent {
		indexedLimit = 1200
	}
	filter := CandidateSourceFilter{SourceIDs: eligibleIDs, SourceKinds: effectiveRequest.SourceKinds, SourceScopes: scopes}
	merged := map[string]LexicalCandidateRow{}
	exactRepresentatives := map[string]string{}
	addRows := func(rows []LexicalCandidateRow, exact bool) {
		for _, row := range rows {
			canonical := row.CanonicalID
			if canonical == "" {
				canonical = row.ID
			}
			if representative := exactRepresentatives[canonical]; representative != "" && representative != row.ID {
				continue
			}
			if exact {
				exactRepresentatives[canonical] = row.ID
			}
			if existing, ok := merged[row.ChunkID]; !ok || row.LexicalRank < existing.LexicalRank {
				merged[row.ChunkID] = row
			}
		}
	}
	documentRestricted := scope != nil
	exactRows := []LexicalCandidateRow{}
	if documentRestricted {
		rows, err := engine.database.DocumentCandidates(ctx, scope.DocumentIDs, expandedTerms, scope.ChunksPerDocument, filter)
		if err != nil {
			return response, err
		}
		addRows(rows, false)
	} else {
		identityAnchors := []string{}
		for _, group := range signals.IdentityGroups {
			for _, term := range group.Terms {
				if !numericPattern.MatchString(term) {
					identityAnchors = append(identityAnchors, term)
				}
			}
		}
		exactLookup := uniqueStrings(append(append([]string{}, signals.DocumentAnchors...), identityAnchors...))
		if len(exactLookup) > 0 {
			rows, err := engine.database.DocumentExactCandidates(ctx, exactLookup, min(240, indexedLimit), filter)
			if err != nil {
				return response, err
			}
			for _, row := range rows {
				matches, err := matchesFilters(row, effectiveRequest)
				if err != nil {
					return response, err
				}
				if matches {
					exactRows = append(exactRows, row)
				}
			}
			addRows(exactRows, true)
		}
		rows, err := engine.database.LexicalCandidates(ctx, strings.Join(lexicalParts, " OR "), indexedLimit, filter)
		if err != nil {
			return response, err
		}
		addRows(rows, false)
		rows, err = engine.database.TrigramCandidates(ctx, strings.Join(trigramTerms, " OR "), indexedLimit, filter)
		if err != nil {
			return response, err
		}
		addRows(rows, false)
		indexedSignalCount := 0
		for _, row := range merged {
			matches, err := matchesFilters(row, effectiveRequest)
			if err != nil {
				return response, err
			}
			if matches && matchesQuerySignalsInHaystack(normalizedFor(row).haystack, primaryConcept, signals.ExplicitAnchors, signals.DocumentAnchors) {
				indexedSignalCount++
			}
		}
		fallbackFloor := max(24, limit*3)
		exactCoverage := map[string]bool{}
		for _, row := range exactRows {
			if matchesQuerySignalsInHaystack(normalizedFor(row).haystack, primaryConcept, signals.ExplicitAnchors, signals.DocumentAnchors) {
				for _, anchor := range row.ExactAnchors {
					exactCoverage[anchor] = true
				}
			}
		}
		allCovered := len(signals.DocumentAnchors) > 0
		for _, anchor := range signals.DocumentAnchors {
			allCovered = allCovered && exactCoverage[anchor]
		}
		if !allCovered && indexedSignalCount < fallbackFloor && len(merged) < fallbackFloor*4 {
			rows, err := engine.database.LikeCandidates(ctx, expandedTerms, 800, filter)
			if err != nil {
				return response, err
			}
			addRows(rows, false)
		}
	}
	rows := []LexicalCandidateRow{}
	for _, row := range merged {
		matches, err := matchesFilters(row, effectiveRequest)
		if err != nil {
			return response, err
		}
		if matches && matchesConfiguredSource(row, eligibleByID) {
			rows = append(rows, row)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].LexicalRank != rows[j].LexicalRank {
			return rows[i].LexicalRank < rows[j].LexicalRank
		}
		if rows[i].EffectiveUpdatedAtMS != rows[j].EffectiveUpdatedAtMS {
			return rows[i].EffectiveUpdatedAtMS > rows[j].EffectiveUpdatedAtMS
		}
		if rows[i].RelativePath != rows[j].RelativePath {
			return rows[i].RelativePath < rows[j].RelativePath
		}
		return rows[i].ChunkID < rows[j].ChunkID
	})
	identityDocumentIDs := map[string]bool{}
	if !documentRestricted {
		identityPriority := len(signals.IdentityGroups) > 0 && (!tableIntent || designOnly)
		for _, row := range rows {
			if namedDocumentIdentityScore(row.Title, row.RelativePath, signals.IdentityGroups) > 0 {
				identityDocumentIDs[row.ID] = true
			}
		}
		if identityPriority && len(identityDocumentIDs) > 0 {
			rows = filterCandidateRows(rows, func(row LexicalCandidateRow) bool { return identityDocumentIDs[row.ID] })
		} else if signals.LatestIntent && !tableIntent && len(signals.DocumentAnchors) > 0 {
			latestIDs := map[string]bool{}
			for _, row := range rows {
				if matchesDocumentIdentity(row.Title, row.RelativePath, signals.DocumentAnchors) {
					latestIDs[row.ID] = true
				}
			}
			if len(latestIDs) > 0 {
				rows = filterCandidateRows(rows, func(row LexicalCandidateRow) bool { return latestIDs[row.ID] })
			}
		}
	}
	if !engine.database.HasTable("chunks_terms") {
		response.Warnings = append(response.Warnings, "当前 SQLite 不支持 FTS5，已降级为字面子串检索")
	}
	if !engine.database.HasTable("chunks_trigram") {
		response.Warnings = append(response.Warnings, "当前 SQLite 不支持 trigram，短语子串召回已降级")
	}
	semanticScores := map[string]float64{}
	wantsSemantic := requestedMode == "semantic" || requestedMode == "hybrid" || (requestedMode == "auto" && config.Search.Embedding.Enabled)
	if wantsSemantic && config.Search.Embedding.Enabled && len(rows) > 0 {
		semanticRows := rows[:min(80, len(rows))]
		inputs := []string{query}
		for _, row := range semanticRows {
			text := row.Text
			if utf16Length(text) > 2000 {
				text, _ = utf16Slice(text, 0, 2000)
			}
			inputs = append(inputs, row.Title+"\n"+text)
		}
		vectors, err := ollamaEmbed(ctx, config.Search.Embedding, inputs)
		if err != nil {
			response.Warnings = append(response.Warnings, "本地语义检索不可用，已使用词法结果："+err.Error())
		} else {
			for index, row := range semanticRows {
				if nonZeroVector(vectors[index+1]) {
					semanticScores[row.ChunkID] = math.Max(0, cosineSimilarity(vectors[0], vectors[index+1]))
				}
			}
			response.SemanticUsed = len(semanticScores) > 0
			response.SemanticCoverage = float64(len(semanticScores)) / float64(len(rows))
			if !response.SemanticUsed {
				response.Warnings = append(response.Warnings, "本地语义检索返回的候选向量全部无效，已使用词法结果")
			}
		}
	} else if requestedMode == "semantic" && !config.Search.Embedding.Enabled {
		response.Warnings = append(response.Warnings, "语义检索未启用，已降级为词法检索")
	}
	if response.SemanticUsed {
		response.ActualMode = "hybrid"
	}
	normalizedTerms := uniqueNormalizedTerms(expandedTerms)
	projectionValues := []string{}
	for _, group := range signals.IdentityGroups {
		projectionValues = append(projectionValues, group.Phrase)
		projectionValues = append(projectionValues, group.Terms...)
	}
	projectionValues = append(projectionValues, NormalizeText(query))
	projectionValues = append(projectionValues, normalizedTerms...)
	projectionValues = append(projectionValues, signals.DocumentAnchors...)
	projectionValues = append(projectionValues, signals.ExplicitAnchors...)
	projectionTerms := uniqueNormalizedTerms(projectionValues)
	requiredExactIDs := []string{}
	for _, anchor := range signals.DocumentAnchors {
		for _, row := range exactRows {
			if containsString(row.ExactAnchors, anchor) {
				requiredExactIDs = appendUnique(requiredExactIDs, row.ID)
				break
			}
		}
	}
	byDocument := map[string][]scoredCandidate{}
	for _, row := range rows {
		candidate := scoreSearchCandidate(row, normalizedFor(row), normalizedTerms, semanticScores[row.ChunkID])
		byDocument[row.ID] = append(byDocument[row.ID], candidate)
	}
	revision := snapshotRevision
	hits := []SearchHit{}
	for _, candidates := range byDocument {
		sort.SliceStable(candidates, func(i, j int) bool {
			return candidates[i].score > candidates[j].score || (candidates[i].score == candidates[j].score && candidates[i].row.Ordinal < candidates[j].row.Ordinal)
		})
		best := candidates[0]
		excerpts := []SearchExcerpt{}
		sectionTypes := []string{}
		for _, candidate := range candidates {
			sectionTypes = appendUnique(sectionTypes, candidate.row.SectionType)
		}
		for _, candidate := range candidates[:min(excerptLimit, len(candidates))] {
			projection, err := MakeExcerpt(candidate.row.Text, candidate.row.Locator, projectionTerms, 520)
			if err != nil {
				return response, err
			}
			citation, err := MakeCitation(candidate.row, revision, &projection, "")
			if err != nil {
				return response, err
			}
			excerpts = append(excerpts, SearchExcerpt{ChunkID: candidate.row.ChunkID, SectionType: candidate.row.SectionType, HeadingPath: safeHeadingPath(candidate.row.HeadingPathJSON), Locator: projection.Locator, Text: projection.Text, HighlightedText: HighlightTerms(projection.Text, candidate.matchedTerms), Score: candidate.score, Citation: citation})
		}
		relevance := 0.0
		for _, candidate := range candidates {
			relevance = math.Max(relevance, candidate.score)
		}
		hits = append(hits, SearchHit{DocumentID: best.row.ID, SourceID: best.row.SourceID, SourceLabel: best.row.SourceLabel, SourceKind: best.row.SourceKind, Title: best.row.Title, AbsolutePath: best.row.AbsolutePath, RelativePath: best.row.RelativePath, Extension: best.row.Extension, EffectiveUpdatedAt: best.row.EffectiveUpdatedAt, DateSource: best.row.DateSource, FilesystemModifiedAt: best.row.FilesystemModifiedAt, Relevance: relevance, FamilyKey: best.row.FamilyKey, FamilyConfidence: best.row.FamilyConfidence, Stale: best.row.Stale, SectionTypes: sectionTypes, Excerpts: excerpts})
	}
	if !documentRestricted && (len(primaryConcept) > 0 || len(signals.ExplicitAnchors) > 0) {
		hits = filterHits(hits, func(hit SearchHit) bool {
			haystack := hitHaystack(hit)
			matchesDocumentAnchor := anyContains(haystack, signals.DocumentAnchors)
			matchesConcept := matchesDocumentAnchor || len(primaryConcept) == 0 || anyContains(haystack, primaryConcept)
			if !matchesConcept || (len(signals.DocumentAnchors) > 0 && !matchesDocumentAnchor) {
				return false
			}
			for _, anchor := range signals.ExplicitAnchors {
				if !containsString(signals.DocumentAnchors, anchor) && !strings.Contains(haystack, anchor) {
					return false
				}
			}
			return true
		})
	}
	rankContext := documentRankContext{Query: query, Terms: normalizedTerms, Signals: signals}
	if !documentRestricted && len(hits) > 0 {
		bestRelevance := 0.0
		for _, hit := range hits {
			bestRelevance = math.Max(bestRelevance, hit.Relevance)
		}
		qualityFloor := math.Min(bestRelevance, math.Max(0.3, bestRelevance*0.7))
		requiredSet := map[string]bool{}
		for _, id := range requiredExactIDs {
			requiredSet[id] = true
		}
		hits = filterHits(hits, func(hit SearchHit) bool {
			return requiredSet[hit.DocumentID] || identityDocumentIDs[hit.DocumentID] || documentIdentityRoleScore(hit, rankContext) >= 0.9 || hit.Relevance >= qualityFloor
		})
	}
	engine.sortHits(hits, sortMode, &rankContext)
	if request.LatestPerFamily {
		seen := map[string]bool{}
		hits = filterHits(hits, func(hit SearchHit) bool {
			if seen[hit.FamilyKey] {
				return false
			}
			seen[hit.FamilyKey] = true
			return true
		})
	}
	selected := map[string]SearchHit{}
	selectedOrder := []string{}
	for _, id := range requiredExactIDs {
		for _, hit := range hits {
			if hit.DocumentID == id && len(selected) < limit {
				selected[id] = hit
				selectedOrder = append(selectedOrder, id)
				break
			}
		}
	}
	for _, hit := range hits {
		if len(selected) >= limit {
			break
		}
		if _, ok := selected[hit.DocumentID]; !ok {
			selected[hit.DocumentID] = hit
			selectedOrder = append(selectedOrder, hit.DocumentID)
		}
	}
	finalHits := make([]SearchHit, 0, len(selectedOrder))
	for _, id := range selectedOrder {
		finalHits = append(finalHits, selected[id])
	}
	engine.sortHits(finalHits, sortMode, &rankContext)
	response.Hits = finalHits[:min(limit, len(finalHits))]
	response.TotalCandidates = len(merged)
	response.IndexRevision = revision
	response.TookMS = roundedMilliseconds(time.Since(started))
	return response, nil
}

func roundedMilliseconds(duration time.Duration) float64 {
	return math.Round(float64(duration.Microseconds())/100) / 10
}

func filterCandidateRows(values []LexicalCandidateRow, keep func(LexicalCandidateRow) bool) []LexicalCandidateRow {
	result := values[:0]
	for _, value := range values {
		if keep(value) {
			result = append(result, value)
		}
	}
	return result
}

func filterHits(values []SearchHit, keep func(SearchHit) bool) []SearchHit {
	result := values[:0]
	for _, value := range values {
		if keep(value) {
			result = append(result, value)
		}
	}
	return result
}

func anyContains(haystack string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

func (engine *SearchEngine) sortHits(hits []SearchHit, sortMode string, rankContext *documentRankContext) {
	parse := func(value string) int64 {
		parsed, _ := time.Parse(time.RFC3339Nano, value)
		return parsed.UnixMilli()
	}
	dates := make(map[string]int64, len(hits))
	roles := make(map[string]float64, len(hits))
	minimum, maximum := int64(math.MaxInt64), int64(math.MinInt64)
	for _, hit := range hits {
		date := parse(hit.EffectiveUpdatedAt)
		dates[hit.DocumentID] = date
		minimum = min(minimum, date)
		maximum = max(maximum, date)
		if rankContext != nil {
			roles[hit.DocumentID] = documentIdentityRoleScore(hit, *rankContext)
		}
	}
	dateScore := func(value int64) float64 {
		if maximum == minimum {
			return 1
		}
		return float64(value-minimum) / float64(maximum-minimum)
	}
	chinese := collate.New(language.Chinese)
	sort.SliceStable(hits, func(i, j int) bool {
		left, right := hits[i], hits[j]
		leftDate, rightDate := dates[left.DocumentID], dates[right.DocumentID]
		if sortMode == "relevance" {
			if left.Relevance != right.Relevance {
				return left.Relevance > right.Relevance
			}
			if leftDate != rightDate {
				return leftDate > rightDate
			}
		} else if sortMode == "hybrid" {
			leftScore := left.Relevance*0.68 + dateScore(leftDate)*0.32
			rightScore := right.Relevance*0.68 + dateScore(rightDate)*0.32
			if leftScore != rightScore {
				return leftScore > rightScore
			}
		} else {
			if leftDate != rightDate {
				return leftDate > rightDate
			}
			if rankContext != nil {
				leftRole, rightRole := roles[left.DocumentID], roles[right.DocumentID]
				if leftRole != rightRole {
					return leftRole > rightRole
				}
			}
			if left.Relevance != right.Relevance {
				return left.Relevance > right.Relevance
			}
		}
		if compared := chinese.CompareString(left.RelativePath, right.RelativePath); compared != 0 {
			return compared < 0
		}
		return left.DocumentID < right.DocumentID
	})
}

func copySearchRequest(request RetrievalRequest) SearchRequest { return request.SearchRequest }

func (engine *SearchEngine) Retrieve(ctx context.Context, request RetrievalRequest) (RetrievalBundle, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if err := engine.refresh(); err != nil {
			return RetrievalBundle{}, err
		}
		configSignature := searchConfigSignature(engine.getConfig())
		bundle, err := engine.retrieve(ctx, request)
		if err != nil && !errors.Is(err, errIndexChangedDuringRead) {
			return bundle, err
		}
		if err == nil {
			if err := engine.refresh(); err != nil {
				return RetrievalBundle{}, err
			}
			currentRevision, revisionErr := engine.database.Revision()
			if revisionErr != nil {
				return RetrievalBundle{}, revisionErr
			}
			if currentRevision == bundle.IndexRevision && searchConfigSignature(engine.getConfig()) == configSignature {
				return bundle, nil
			}
		}
	}
	return RetrievalBundle{}, fmt.Errorf("%w，请重试", errIndexChangedDuringRead)
}

func (engine *SearchEngine) retrieve(ctx context.Context, request RetrievalRequest) (RetrievalBundle, error) {
	config := engine.getConfig()
	maxDocuments := request.MaxDocuments
	if maxDocuments == 0 {
		maxDocuments = 8
	}
	maxDocuments = min(50, max(1, maxDocuments))
	maxChunks := request.MaxChunksPerDocument
	if maxChunks == 0 {
		maxChunks = 4
	}
	maxChunks = min(10, max(1, maxChunks))
	maxChars := request.MaxChars
	if maxChars == 0 {
		maxChars = config.Search.MaxEvidenceChars
	}
	maxChars = min(60_000, max(2_000, maxChars))
	selectedDocumentIDs := uniqueStrings(request.DocumentIDs)
	if len(selectedDocumentIDs) > maxDocuments {
		selectedDocumentIDs = selectedDocumentIDs[:maxDocuments]
	}
	var candidateScope *searchCandidateScope
	if len(selectedDocumentIDs) > 0 {
		candidateScope = &searchCandidateScope{DocumentIDs: selectedDocumentIDs, ChunksPerDocument: max(12, maxChunks*3)}
	}
	baseRequest := copySearchRequest(request)
	var search SearchResponse
	if len(baseRequest.SourceIDs) == 0 && len(baseRequest.SourceKinds) == 0 {
		perSourceLimit := max(maxDocuments, baseRequest.Limit)
		designRequest, tableRequest := baseRequest, baseRequest
		designRequest.SourceKinds, designRequest.Limit = []string{"design"}, perSourceLimit
		tableRequest.SourceKinds, tableRequest.Limit = []string{"table"}, perSourceLimit
		designSearch, err := engine.search(ctx, designRequest, candidateScope, maxChunks)
		if err != nil {
			return RetrievalBundle{}, err
		}
		tableSearch, err := engine.search(ctx, tableRequest, candidateScope, maxChunks)
		if err != nil {
			return RetrievalBundle{}, err
		}
		if designSearch.IndexRevision != tableSearch.IndexRevision {
			return RetrievalBundle{}, errIndexChangedDuringRead
		}
		tableFirst := tableIntentPattern.MatchString(baseRequest.Query)
		signals := QueryAnchorSignals(baseRequest.Query)
		rank := documentRankContext{Query: baseRequest.Query, Terms: uniqueNormalizedTerms(ExpandQueryTerms(baseRequest.Query, config.Search.SynonymExpansion)), Signals: signals}
		primary, secondary := designSearch.Hits, tableSearch.Hits
		if tableFirst {
			primary, secondary = tableSearch.Hits, designSearch.Hits
		}
		restrictIdentity := !tableFirst && (len(signals.IdentityGroups) > 0 || (signals.LatestIntent && len(signals.DocumentAnchors) > 0))
		if restrictIdentity {
			identityIDs := map[string]bool{}
			for _, hit := range append(append([]SearchHit{}, primary...), secondary...) {
				if matchesIdentitySignals(hit.Title, hit.RelativePath, signals) {
					identityIDs[hit.DocumentID] = true
				}
			}
			if len(identityIDs) > 0 {
				primary = filterHits(primary, func(hit SearchHit) bool { return identityIDs[hit.DocumentID] })
				secondary = filterHits(secondary, func(hit SearchHit) bool { return identityIDs[hit.DocumentID] })
			}
		}
		primaryQuota := (maxDocuments*3 + 3) / 4
		selectedPrimary := append([]SearchHit{}, primary[:min(primaryQuota, len(primary))]...)
		if tableFirst {
			recentQuota := max(1, (primaryQuota*66+99)/100)
			selectedMap := map[string]SearchHit{}
			selectedOrder := []string{}
			for _, hit := range primary[:min(recentQuota, len(primary))] {
				selectedMap[hit.DocumentID] = hit
				selectedOrder = append(selectedOrder, hit.DocumentID)
			}
			byRelevance := append([]SearchHit{}, primary...)
			sort.SliceStable(byRelevance, func(i, j int) bool { return byRelevance[i].Relevance > byRelevance[j].Relevance })
			for _, hit := range byRelevance {
				if len(selectedMap) >= primaryQuota {
					break
				}
				if _, ok := selectedMap[hit.DocumentID]; !ok {
					selectedMap[hit.DocumentID] = hit
					selectedOrder = append(selectedOrder, hit.DocumentID)
				}
			}
			selectedPrimary = selectedPrimary[:0]
			for _, id := range selectedOrder {
				selectedPrimary = append(selectedPrimary, selectedMap[id])
			}
			engine.sortHits(selectedPrimary, "newest", &rank)
		}
		quotaHits := append(append([]SearchHit{}, selectedPrimary...), secondary[:min(max(0, maxDocuments-primaryQuota), len(secondary))]...)
		allCandidates := append(append([]SearchHit{}, primary...), secondary...)
		requiredHits := []SearchHit{}
		if tableFirst && len(signals.IdentityGroups) > 0 {
			for _, hit := range designSearch.Hits {
				if matchesIdentitySignals(hit.Title, hit.RelativePath, signals) {
					requiredHits = append(requiredHits, hit)
					break
				}
			}
		}
		for _, anchor := range signals.DocumentAnchors {
			for _, hit := range allCandidates {
				matched := strings.Contains(hitHaystack(hit), anchor)
				if restrictIdentity {
					matched = matchesDocumentIdentity(hit.Title, hit.RelativePath, []string{anchor})
				}
				if matched {
					requiredHits = append(requiredHits, hit)
					break
				}
			}
		}
		selected := map[string]SearchHit{}
		order := []string{}
		for _, hit := range append(requiredHits, append(quotaHits, allCandidates...)...) {
			if len(selected) >= maxDocuments {
				break
			}
			if _, ok := selected[hit.DocumentID]; !ok {
				selected[hit.DocumentID] = hit
				order = append(order, hit.DocumentID)
			}
		}
		mergedHits := make([]SearchHit, len(order))
		for index, id := range order {
			mergedHits[index] = selected[id]
		}
		if !tableFirst {
			engine.sortHits(mergedHits, designSearch.Sort, &rank)
		}
		search = designSearch
		search.Hits = mergedHits[:min(maxDocuments, len(mergedHits))]
		search.SemanticUsed = designSearch.SemanticUsed || tableSearch.SemanticUsed
		if search.SemanticUsed {
			search.ActualMode = "hybrid"
		}
		search.SemanticCoverage = math.Max(designSearch.SemanticCoverage, tableSearch.SemanticCoverage)
		search.TotalCandidates = designSearch.TotalCandidates + tableSearch.TotalCandidates
		search.TookMS = math.Round((designSearch.TookMS+tableSearch.TookMS)*10) / 10
		search.Warnings = uniqueStrings(append(designSearch.Warnings, tableSearch.Warnings...))
	} else {
		baseRequest.Limit = max(maxDocuments, baseRequest.Limit)
		var err error
		search, err = engine.search(ctx, baseRequest, candidateScope, maxChunks)
		if err != nil {
			return RetrievalBundle{}, err
		}
	}
	allowed := map[string]bool{}
	for _, id := range selectedDocumentIDs {
		allowed[id] = true
	}
	if len(allowed) > 0 {
		available := map[string]bool{}
		for _, hit := range search.Hits {
			available[hit.DocumentID] = true
		}
		missing := []string{}
		for _, id := range selectedDocumentIDs {
			if !available[id] {
				missing = append(missing, id)
			}
		}
		search.Hits = filterHits(search.Hits, func(hit SearchHit) bool { return allowed[hit.DocumentID] })
		if len(missing) > 0 {
			search.Warnings = append(search.Warnings, "以下 documentId 不存在、已禁用或不符合筛选条件："+strings.Join(missing, ", "))
		}
	}
	bundle := RetrievalBundle{Kind: "drag_retrieval_bundle_v1", Trust: "untrusted_reference_data", Query: search.Query, IndexRevision: search.IndexRevision, ActualMode: search.ActualMode, GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), Evidence: []RetrievalEvidence{}, Search: search}
	for _, hit := range search.Hits[:min(maxDocuments, len(search.Hits))] {
		if len(allowed) > 0 && !allowed[hit.DocumentID] {
			continue
		}
		for _, excerpt := range hit.Excerpts[:min(maxChunks, len(hit.Excerpts))] {
			next := bundle.CharacterCount + utf16Length(excerpt.Text)
			if next > maxChars {
				bundle.Truncated = true
				break
			}
			bundle.Evidence = append(bundle.Evidence, RetrievalEvidence{CitationID: excerpt.Citation.CitationID, Title: hit.Title, EffectiveUpdatedAt: hit.EffectiveUpdatedAt, DateSource: hit.DateSource, SectionType: excerpt.SectionType, Locator: excerpt.Locator, RelativePath: hit.RelativePath, AbsolutePath: hit.AbsolutePath, SourceLink: excerpt.Citation.SourceLink, Content: excerpt.Text, IndexedContentHash: excerpt.Citation.IndexedContentHash})
			bundle.CharacterCount = next
		}
		if bundle.Truncated {
			break
		}
	}
	return bundle, nil
}

func (engine *SearchEngine) ReadCitation(ctx context.Context, citationID string, expectedRevision *int64) (CitationReadResult, error) {
	if err := engine.refresh(); err != nil {
		return CitationReadResult{}, err
	}
	snapshotRevision, err := engine.database.Revision()
	if err != nil {
		return CitationReadResult{}, err
	}
	config := engine.getConfig()
	configSignature := searchConfigSignature(config)
	reference, err := parseCitationReference(citationID)
	if err != nil {
		return CitationReadResult{}, err
	}
	row, err := engine.database.GetChunk(ctx, reference.ChunkID)
	if err != nil {
		return CitationReadResult{}, err
	}
	if row == nil {
		return CitationReadResult{}, fmt.Errorf("引用不存在或已删除：%s", citationID)
	}
	enabled := map[string]Source{}
	for _, source := range config.Sources {
		if source.Enabled {
			enabled[source.ID] = source
		}
	}
	if !matchesConfiguredSource(*row, enabled) {
		return CitationReadResult{}, fmt.Errorf("引用所属资料源已禁用、不存在或已变更：%s", citationID)
	}
	revision, err := engine.database.Revision()
	if err != nil {
		return CitationReadResult{}, err
	}
	if revision != snapshotRevision {
		return CitationReadResult{}, fmt.Errorf("%w，请重试引用回读", errIndexChangedDuringRead)
	}
	if searchConfigSignature(engine.getConfig()) != configSignature {
		return CitationReadResult{}, fmt.Errorf("资料源配置在引用回读期间发生变化，请重试")
	}
	var projection *ExcerptProjection
	canonicalID := ""
	if reference.ScopeV1 != nil {
		expectedDigest := citationDigest("drag-scoped-citation-v1\x00", row.ChunkID, row.ContentHash, []byte(reference.PayloadV1))
		if reference.PayloadV1 == "" || reference.Digest == "" || expectedDigest != reference.Digest {
			return CitationReadResult{}, fmt.Errorf("引用范围无效或已损坏：scoped citation 校验失败")
		}
		content, err := renderSpreadsheetCitationScope(row.Text, row.Locator, *reference.ScopeV1)
		if err != nil {
			return CitationReadResult{}, err
		}
		projection = &ExcerptProjection{Text: content, Locator: reference.ScopeV1.Locator, Scope: reference.ScopeV1}
		canonicalID, err = scopedCitationIDV1(*row, *reference.ScopeV1)
		if err != nil {
			return CitationReadResult{}, err
		}
	} else if reference.ScopeV2 != nil {
		expectedDigest := citationDigest("drag-scoped-citation-v2\x00", "", row.ContentHash, reference.PayloadV2)
		if len(reference.PayloadV2) == 0 || reference.Digest == "" || expectedDigest != reference.Digest {
			return CitationReadResult{}, fmt.Errorf("引用范围无效或已损坏：短 scoped citation 校验失败")
		}
		scope, err := decodeSpreadsheetCitationScopeV2(row.Locator, *reference.ScopeV2)
		if err != nil {
			return CitationReadResult{}, err
		}
		content, err := renderSpreadsheetCitationScope(row.Text, row.Locator, scope)
		if err != nil {
			return CitationReadResult{}, err
		}
		projection = &ExcerptProjection{Text: content, Locator: scope.Locator, Scope: &scope}
		canonicalID, err = scopedCitationID(*row, scope)
		if err != nil {
			return CitationReadResult{}, err
		}
	} else if reference.TextSlice != nil {
		expectedDigest := citationDigest("drag-scoped-text-v1\x00", "", row.ContentHash, reference.PayloadText)
		if reference.Digest == "" || expectedDigest != reference.Digest {
			return CitationReadResult{}, fmt.Errorf("引用范围无效或已损坏：文本 scoped citation 校验失败")
		}
		content, err := renderExcerptSlice(row.Text, *reference.TextSlice)
		if err != nil {
			return CitationReadResult{}, err
		}
		projection = &ExcerptProjection{Text: content, Locator: row.Locator, TextSlice: reference.TextSlice}
		canonicalID, err = scopedTextCitationID(*row, *reference.TextSlice)
		if err != nil {
			return CitationReadResult{}, err
		}
	}
	if canonicalID != "" && strings.HasPrefix(citationID, CitationPrefix) && canonicalID != citationID {
		return CitationReadResult{}, fmt.Errorf("引用范围无效或已损坏：scoped citation 不一致")
	}
	if err := engine.refresh(); err != nil {
		return CitationReadResult{}, err
	}
	if searchConfigSignature(engine.getConfig()) != configSignature {
		return CitationReadResult{}, fmt.Errorf("资料源配置在引用回读期间发生变化，请重试")
	}
	citation, err := MakeCitation(*row, revision, projection, canonicalID)
	if err != nil {
		return CitationReadResult{}, err
	}
	content := row.Text
	if projection != nil {
		content = projection.Text
	}
	return CitationReadResult{Citation: citation, Content: content, Changed: expectedRevision != nil && *expectedRevision != revision, CurrentIndexRevision: revision}, nil
}

func (engine *SearchEngine) ListVersions(ctx context.Context, documentID, familyKey string, limit int) ([]VersionEntry, error) {
	if err := engine.refresh(); err != nil {
		return nil, err
	}
	configSignature := searchConfigSignature(engine.getConfig())
	revision, err := engine.database.Revision()
	if err != nil {
		return nil, err
	}
	config := engine.getConfig()
	enabled := map[string]Source{}
	scopes := []SourceIdentityScope{}
	for _, source := range config.Sources {
		if source.Enabled {
			enabled[source.ID] = source
			scopes = append(scopes, SourceIdentityScope{SourceID: source.ID, SourceIdentity: SourceIndexIdentity(source)})
		}
	}
	if documentID != "" {
		document, err := engine.database.GetDocument(ctx, documentID)
		if err != nil {
			return nil, err
		}
		source, ok := enabled[func() string {
			if document == nil {
				return ""
			}
			return document.SourceID
		}()]
		if document == nil || !ok || document.SourceIdentity != SourceIndexIdentity(source) {
			return nil, fmt.Errorf("文档不存在，或所属资料源已禁用、删除或变更")
		}
		if familyKey == "" {
			familyKey = document.FamilyKey
		}
	}
	if familyKey == "" {
		return nil, fmt.Errorf("documentId 或 familyKey 至少提供一个")
	}
	if limit == 0 {
		limit = 30
	}
	documents, err := engine.database.GetVersions(ctx, familyKey, min(100, max(1, limit)), scopes)
	if err != nil {
		return nil, err
	}
	result := []VersionEntry{}
	for _, document := range documents {
		source, ok := enabled[document.SourceID]
		if !ok || document.SourceIdentity != SourceIndexIdentity(source) {
			continue
		}
		result = append(result, VersionEntry{DocumentID: document.ID, SourceID: document.SourceID, SourceLabel: document.SourceLabel, SourceKind: document.SourceKind, Title: document.Title, EffectiveUpdatedAt: document.EffectiveUpdatedAt, DateSource: document.DateSource, RelativePath: document.RelativePath, FamilyKey: document.FamilyKey, FamilyConfidence: document.FamilyConfidence, Canonical: document.ID == document.CanonicalID, Stale: document.Stale})
	}
	if err := engine.refresh(); err != nil {
		return nil, err
	}
	currentRevision, err := engine.database.Revision()
	if err != nil {
		return nil, err
	}
	if currentRevision != revision || searchConfigSignature(engine.getConfig()) != configSignature {
		return nil, fmt.Errorf("%w，请重试版本读取", errIndexChangedDuringRead)
	}
	return result, nil
}
