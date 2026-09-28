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
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

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

// targetedExcerptLength 是定向取证时每个分块摘录的长度上限。定向取证（retrieve 指定 documentIds）
// 通常是为了读全文档的相关段落，摘录按预算放宽，调用方不必逐行追查。
const targetedExcerptLength = 2400

func searchConfigSignature(config AppConfig) string {
	raw, _ := json.Marshal(config)
	return string(raw)
}

type searchCandidateScope struct {
	DocumentIDs       []string
	ChunksPerDocument int
	// ExcerptLength 是定向取证时每个分块的摘录长度；不超过默认长度时按默认处理。
	ExcerptLength int
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

	ftsStatsMutex    sync.Mutex
	ftsStatsRevision int64
	ftsChunkTotal    int
	ftsMatchCounts   map[string]int
}

// ftsIDF 返回 MATCH 表达式的 BM25 IDF；命中数按索引 revision 缓存，避免重复计数。
func (engine *SearchEngine) ftsIDF(ctx context.Context, revision int64, expression string) (float64, error) {
	engine.ftsStatsMutex.Lock()
	defer engine.ftsStatsMutex.Unlock()
	if engine.ftsMatchCounts == nil || engine.ftsStatsRevision != revision {
		total, err := engine.database.ChunkCount(ctx)
		if err != nil {
			return 0, err
		}
		engine.ftsStatsRevision, engine.ftsChunkTotal, engine.ftsMatchCounts = revision, total, map[string]int{}
	}
	count, ok := engine.ftsMatchCounts[expression]
	if !ok {
		var err error
		if count, err = engine.database.CountFTSMatches(ctx, expression); err != nil {
			return 0, err
		}
		if len(engine.ftsMatchCounts) >= 4096 {
			engine.ftsMatchCounts = map[string]int{}
		}
		engine.ftsMatchCounts[expression] = count
	}
	if count == 0 {
		return 0, nil
	}
	total := float64(max(engine.ftsChunkTotal, count))
	return math.Log(1 + (total-float64(count)+0.5)/(float64(count)+0.5)), nil
}

// conceptFTSMatches 为每个检索概念生成“任意字段”和“标题/层级/路径”两个 MATCH 分支，
// 权重取该概念在全部分块中的区分度；同义词合成一个 OR 分支并减半，使稀有的实体名、
// 系统名优先于“配置”“表格”这类泛化词进入候选。概念权重只取关键词本身的区分度，
// 关键词在索引中不存在时才用同义词分支的权重，泛化词不会借稀有同义词抬高权重。
func (engine *SearchEngine) conceptFTSMatches(ctx context.Context, revision int64, concepts []queryConcept) ([]WeightedFTSMatch, error) {
	result := []WeightedFTSMatch{}
	for conceptIndex := range concepts {
		concept := &concepts[conceptIndex]
		primaryWeight, alternateWeight := 0.0, 0.0
		alternates := []string{}
		for _, alternate := range concept.alternates {
			if expression := FTSConjunction(alternate); expression != "" {
				alternates = append(alternates, "("+expression+")")
			}
		}
		branches := []struct {
			expression string
			factor     float64
		}{{FTSConjunction(concept.primary), 1}, {strings.Join(alternates, " OR "), 0.5}}
		for index, branch := range branches {
			if branch.expression == "" {
				continue
			}
			expression := "(" + branch.expression + ")"
			idf, err := engine.ftsIDF(ctx, revision, expression)
			if err != nil {
				return nil, err
			}
			if idf <= 0 {
				continue
			}
			weight := idf * branch.factor
			if index == 0 {
				primaryWeight = weight
			} else {
				alternateWeight = weight
			}
			result = append(result, WeightedFTSMatch{Match: expression, Weight: weight}, WeightedFTSMatch{Match: "{title_terms heading_terms path_terms} : " + expression, Weight: weight})
		}
		concept.weight = primaryWeight
		if concept.weight == 0 {
			concept.weight = alternateWeight
		}
	}
	// 索引中完全没有词元的概念（如只能靠标题子串命中的词）取已知概念的最小权重，避免被忽略。
	minimum := 0.0
	for _, concept := range concepts {
		if concept.weight > 0 && (minimum == 0 || concept.weight < minimum) {
			minimum = concept.weight
		}
	}
	for index := range concepts {
		if concepts[index].weight == 0 {
			concepts[index].weight = minimum
		}
	}
	normalizeConceptWeights(concepts)
	return result, nil
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

// queryConcept 是查询中的一个检索概念：关键词本身为 primary，与它相关的领域同义词为 alternates。
// weight 是该概念在全部概念中的相对区分度，所有概念的 weight 之和为 1。
type queryConcept struct {
	primary    string
	alternates []string
	weight     float64
	identity   *documentIdentityGroup
}

// identityKeywordPattern 匹配“实体名+编号”形式的关键词，如“晨星888”“星河龙888”。
var identityKeywordPattern = regexp.MustCompile(`^(\p{Han}{2,24})(\d{2,})$`)

// identityMatchStrength 用容忍间隔的身份匹配判断“晨星888”是否对应“晨星·守望者888活动”这类标题。
func identityMatchStrength(normalized normalizedCandidateFields, group documentIdentityGroup) float64 {
	switch {
	case identityGroupAtBoundary(normalized.title, group):
		return 0.8
	case identityGroupAtBoundary(normalized.heading, group) || identityGroupAtBoundary(normalized.relativePath, group):
		return 0.6
	case identityGroupScore(normalized.title, group) > 0:
		return 0.5
	case identityGroupScore(normalized.heading, group) > 0 || identityGroupScore(normalized.relativePath, group) > 0:
		return 0.4
	case strings.Contains(normalized.text, group.Terms[0]) && strings.Contains(normalized.text, group.Terms[1]):
		return 0.25
	default:
		return 0
	}
}

// normalizeConceptWeights 把概念权重归一化；没有可用权重时平均分配。
func normalizeConceptWeights(concepts []queryConcept) {
	total := 0.0
	for _, concept := range concepts {
		total += math.Max(0, concept.weight)
	}
	for index := range concepts {
		if total > 0 {
			concepts[index].weight = math.Max(0, concepts[index].weight) / total
		} else {
			concepts[index].weight = 1 / float64(len(concepts))
		}
	}
}

// buildQueryConcepts 以关键词为单位组织相关度：命中关键词本身得满分，只命中同义词得半分，
// 文档不能靠命中同一概念的多个同义词累积相关度。整句和子句只作为短语加分，不计入覆盖率。
// 抽不出关键词时退回旧行为，每个扩展词各自成为一个概念。
func buildQueryConcepts(query string, synonyms bool) ([]queryConcept, []string) {
	normalized := NormalizeText(query)
	keywords := QueryKeywordTerms(normalized)
	if len(keywords) == 0 {
		concepts := []queryConcept{}
		for _, term := range uniqueNormalizedTerms(ExpandQueryTerms(query, synonyms)) {
			concepts = append(concepts, queryConcept{primary: term})
		}
		normalizeConceptWeights(concepts)
		return concepts, nil
	}
	concepts := make([]queryConcept, 0, len(keywords))
	for _, keyword := range keywords {
		concept := queryConcept{primary: keyword}
		if match := identityKeywordPattern.FindStringSubmatch(keyword); match != nil {
			concept.identity = &documentIdentityGroup{Phrase: keyword, Terms: []string{match[1], match[2]}}
		}
		if synonyms {
			for _, group := range synonymGroups {
				related := false
				for _, term := range group {
					term = NormalizeText(term)
					if strings.Contains(keyword, term) || strings.Contains(term, keyword) {
						related = true
						break
					}
				}
				if related {
					for _, term := range group {
						if term = NormalizeText(term); term != keyword {
							concept.alternates = appendUnique(concept.alternates, term)
						}
					}
				}
			}
		}
		concepts = append(concepts, concept)
	}
	normalizeConceptWeights(concepts)
	phrases := []string{}
	for _, value := range append([]string{normalized}, queryClauseSeparator.Split(normalized, -1)...) {
		if utf8.RuneCountInString(value) >= 4 && !containsString(keywords, value) {
			phrases = appendUnique(phrases, value)
		}
	}
	return concepts, phrases
}

// exactCellColumn 返回配表行中值恰好等于 term 的单元格列号（规范化后的行形如 “| i=晨星守望者 | j=3826”）；
// 没有时返回空串。定义实体的配置行通常以名称为完整单元格，汇总表多在长文本里顺带提到。
func exactCellColumn(text, term string) string {
	needle := "=" + term
	for offset := 0; ; {
		index := strings.Index(text[offset:], needle)
		if index < 0 {
			return ""
		}
		start := offset + index
		end := start + len(needle)
		column := start
		for column > 0 && text[column-1] >= 'a' && text[column-1] <= 'z' {
			column--
		}
		if column < start && column > 0 && text[column-1] == ' ' && (end == len(text) || strings.HasPrefix(text[end:], " |") || strings.HasPrefix(text[end:], " 行 ")) {
			return text[column:start]
		}
		offset = end
	}
}

// isNameColumn 判断表头中该列是否为名称列，如 “| i=name |”、“| c=petname |”、“| b=名称 |”。
func isNameColumn(text, column string) bool {
	header, _, _ := strings.Cut(text, " 行 ")
	marker := " " + column + "="
	for offset := 0; ; {
		index := strings.Index(header[offset:], marker)
		if index < 0 {
			return false
		}
		value := header[offset+index+len(marker):]
		if cut := strings.Index(value, " |"); cut >= 0 {
			value = value[:cut]
		}
		if strings.HasSuffix(value, "name") || value == "名称" || value == "名字" {
			return true
		}
		offset += index + len(marker)
	}
}

// fieldMatchStrength 返回 term 在文档中最强命中位置的强度：标题完全相同 1，标题包含 0.85，
// 名称列单元格完全相同 0.85，配表所在目录名 0.8，其他单元格完全相同 0.75，层级标题或路径 0.65，
// 仅正文 0.3；未命中返回 0。配置仓库按系统分目录（如“扭蛋机\alphaLottery.xlsx”），目录名就是表的归属。
func fieldMatchStrength(normalized normalizedCandidateFields, term string, table bool) float64 {
	switch {
	case normalized.title == term:
		return 1
	case strings.Contains(normalized.title, term):
		return 0.85
	}
	if table {
		column := exactCellColumn(normalized.text, term)
		switch {
		case column != "" && isNameColumn(normalized.text, column):
			return 0.85
		case strings.Contains(pathDirectory(normalized.relativePath), term):
			return 0.8
		case column != "":
			return 0.75
		}
	}
	switch {
	case strings.Contains(normalized.heading, term) || strings.Contains(normalized.relativePath, term):
		return 0.65
	case strings.Contains(normalized.text, term):
		return 0.3
	default:
		return 0
	}
}

// pathDirectory 返回相对路径中文件名之前的目录部分。
func pathDirectory(relativePath string) string {
	if index := strings.LastIndexAny(relativePath, `\/`); index >= 0 {
		return relativePath[:index]
	}
	return ""
}

// lexicalRankBonus 把候选 SQL 的 rank（越负越相关）映射为 0-0.06 的加分；精确锚点候选的 rank 为 0，
// 视为最强命中；LIKE 兜底候选没有可比的相关度，不加分。概念覆盖已经决定主要相关度，这里只做细分。
func lexicalRankBonus(rank float64) float64 {
	switch {
	case rank == 0:
		return 0.06
	case rank < 0:
		return -rank / (1 - rank) * 0.06
	default:
		return 0
	}
}

// scoreSearchCandidate 按概念权重累加字段强度：稀有概念命中在标题、路径或名称单元格时得分最高，
// 只在正文顺带提到时得分较低。命中关键词本身计全额，命中同义词计一半，同一概念取两者中较强的一处，
// 例如“产出”只在正文出现、而层级标题是“奖励数值”时按后者计。
// declaredRerun 表示正文写明本文档是该活动的返场或复用稿（见 bodyDeclaredIdentityDocuments），
// 身份强度与目录命中同级，不再只按正文顺带提到计分。
func scoreSearchCandidate(row LexicalCandidateRow, normalized normalizedCandidateFields, concepts []queryConcept, phrases []string, semanticScore float64, declaredRerun bool) scoredCandidate {
	matched := []string{}
	table := row.SourceKind == "table"
	strength := 0.0
	for _, concept := range concepts {
		value := 0.0
		if concept.identity != nil {
			value = identityMatchStrength(normalized, *concept.identity)
			if declaredRerun {
				value = math.Max(value, 0.6)
			}
		} else {
			value = fieldMatchStrength(normalized, concept.primary, table)
		}
		if value > 0 {
			matched = append(matched, concept.primary)
		}
		if value < 0.5 {
			best, bestTerm := 0.0, ""
			for _, alternate := range concept.alternates {
				if alternateValue := fieldMatchStrength(normalized, alternate, table); alternateValue > best {
					best, bestTerm = alternateValue, alternate
				}
			}
			if best/2 > value {
				value = best / 2
				matched = append(matched, bestTerm)
			}
		}
		strength += concept.weight * value
	}
	score := 0.18 + strength*0.72
	for _, phrase := range phrases {
		if strings.Contains(normalized.title, phrase) || strings.Contains(normalized.heading, phrase) || strings.Contains(normalized.relativePath, phrase) {
			score += 0.08
		} else if strings.Contains(normalized.text, phrase) {
			score += 0.03
		}
	}
	score += lexicalRankBonus(row.LexicalRank)
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
	identity, _ := compactIdentityJoins(value)
	return identity
}

// compactIdentityJoins 在 compactIdentity 的基础上记录哪些字节偏移处的汉字在原文中紧跟另一个汉字。
// 实体名紧跟汉字时只是更长名称的一部分，如“破晨星·裂空”中的“晨星”。
func compactIdentityJoins(value string) (string, map[int]bool) {
	var builder strings.Builder
	joined := map[int]bool{}
	previousHan := false
	for _, r := range NormalizeText(value) {
		if !isCJK(r) && !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			previousHan = false
			continue
		}
		han := unicode.Is(unicode.Han, r)
		if han && previousHan {
			joined[builder.Len()] = true
		}
		builder.WriteRune(r)
		previousHan = han
	}
	return builder.String(), joined
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

// identityGroupScore 返回活动身份在 value 中的匹配分：整体短语命中 4，按顺序分散命中 2-3.5（间隔越长越低）；
// 实体名嵌在更长的名称里时再减 1，使“晨星888”优先对应“晨星·守望者888”而不是“破晨星·裂空888”。
func identityGroupScore(value string, group documentIdentityGroup) float64 {
	score, embedded := matchIdentityGroup(value, group)
	if embedded {
		score--
	}
	return score
}

// identityGroupAtBoundary 判断 value 中的活动身份是否以独立名称出现。
func identityGroupAtBoundary(value string, group documentIdentityGroup) bool {
	score, embedded := matchIdentityGroup(value, group)
	return score > 0 && !embedded
}

func matchIdentityGroup(value string, group documentIdentityGroup) (float64, bool) {
	identity, joined := compactIdentityJoins(value)
	if identity == "" {
		return 0, false
	}
	if index := strings.Index(identity, group.Phrase); index >= 0 {
		return 4, joined[index]
	}
	cursor, first, last := 0, -1, -1
	for _, term := range group.Terms {
		position := strings.Index(identity[cursor:], term)
		if position < 0 {
			return 0, false
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
	return math.Max(2, 3.5-math.Min(1.5, float64(gap)/8)), joined[first]
}

// identitySpans 返回 value 中以独立名称出现的活动名（紧凑形式，从实体名到其后第一个编号），
// 如“晨星·守望者888活动”得到“晨星守望者888”。
func identitySpans(value string, group documentIdentityGroup) []string {
	identity, joined := compactIdentityJoins(value)
	entity, numeric := group.Terms[0], group.Terms[len(group.Terms)-1]
	spans := []string{}
	for offset := 0; offset < len(identity); {
		index := strings.Index(identity[offset:], entity)
		if index < 0 {
			break
		}
		index += offset
		offset = index + len(entity)
		if joined[index] {
			continue
		}
		if end := strings.Index(identity[offset:], numeric); end >= 0 {
			spans = append(spans, identity[index:offset+end+len(numeric)])
		}
	}
	return spans
}

// namesStandalone 判断 value 中是否以独立名称写出 name（紧凑形式，前面不紧跟汉字），不要求编号。
func namesStandalone(value, name string) bool {
	identity, joined := compactIdentityJoins(value)
	for offset := 0; offset < len(identity); {
		index := strings.Index(identity[offset:], name)
		if index < 0 {
			return false
		}
		if !joined[offset+index] {
			return true
		}
		offset += index + len(name)
	}
	return false
}

// normalizedSpanText 是逐行规范化、保留换行的原文，以及去掉空白和标点的紧凑形式中每个字符在原文里的位置。
type normalizedSpanText struct {
	runes     []rune
	compact   []rune
	positions []int
}

func newNormalizedSpanText(value string) normalizedSpanText {
	lines := []string{}
	for _, line := range strings.Split(value, "\n") {
		if line = NormalizeText(line); line != "" {
			lines = append(lines, line)
		}
	}
	text := normalizedSpanText{runes: []rune(strings.Join(lines, "\n"))}
	for index, r := range text.runes {
		if isCJK(r) || unicode.IsLetter(r) || unicode.IsNumber(r) {
			text.compact = append(text.compact, r)
			text.positions = append(text.positions, index)
		}
	}
	return text
}

// occurrences 返回紧凑形式的活动名 span 每次出现时在原文中的首尾字符位置。
// 正文里活动名常紧跟在“为”“复用”等字后面，这里不像标题那样区分独立名称与嵌在更长名称里的写法。
func (text normalizedSpanText) occurrences(span string) [][2]int {
	target := []rune(span)
	result := [][2]int{}
	if len(target) == 0 {
		return result
	}
	for start := 0; start+len(target) <= len(text.compact); start++ {
		if text.compact[start] == target[0] && slices.Equal(text.compact[start:start+len(target)], target) {
			result = append(result, [2]int{text.positions[start], text.positions[start+len(target)-1]})
		}
	}
	return result
}

// context 返回原文第 first 到 last 个字符所在的片段，前后各留 radius 个字符，供调用方核对。
func (text normalizedSpanText) context(first, last, radius int) string {
	runes := text.runes
	from, to := max(0, first-radius), min(len(runes), last+1+radius)
	lead, tail := from > 0, to < len(runes)
	// 配表行以“列=值”和“ | ”分隔单元格，只保留活动名所在的单元格与行。
	for index := first - 1; index >= from; index-- {
		if runes[index] == '=' || runes[index] == '|' || runes[index] == '\n' {
			from, lead = index+1, false
			break
		}
	}
	for index := last + 1; index < to; index++ {
		if runes[index] == '|' || runes[index] == '\n' {
			to, tail = index, false
			break
		}
	}
	excerpt := strings.TrimSpace(string(runes[from:to]))
	if lead {
		excerpt = "…" + excerpt
	}
	if tail {
		excerpt += "…"
	}
	return excerpt
}

// bodyIdentityMatch 记录按正文归入活动身份的文档：正文里的完整活动名、标题或目录写出同一活动名的身份文档标题，
// 以及正文是否直接写明本文档是该活动的返场或复用稿（declared）和该处的原文片段。
type bodyIdentityMatch struct {
	name       string
	context    string
	ownerTitle string
	declared   bool
}

var (
	// rerunMarkers 是写明返场、复用关系的词。
	rerunMarkers = []string{"返场", "复刻", "复用", "重开"}
	// rerunSubjects 是正文指代本文档活动的主语，rerunConnectors 是主语与活动名或关系词之间允许的系词。
	rerunSubjects   = []string{"本次活动", "本活动", "本次", "本期", "此次", "这次", "本版本"}
	rerunConnectors = []string{"作为", "为", "是", ""}
	// rerunLeadBoundaries 是行、单元格、句子或列表项的起点。冒号不算：“玩法参考：××888活动返场方案”是在引用别的文档。
	rerunLeadBoundaries = "\n|=·•●*-#。；;！!？?、.)）]】"
	// rerunClauseBoundaries 是分句的硬边界，用来截取活动名所在的分句、检查其中有没有引用词。
	rerunClauseBoundaries = "\n|=。；;！!？?"
	rerunReferenceWords   = []string{"参考", "参照", "借鉴", "类似", "对标", "仿照", "对照"}
	// rerunClauseEnds 是分句结束处（含逗号），用来截取活动名所在的分句检查疑问语气。
	rerunClauseEnds = "\n|。；;！!？?，,"
	// rerunTagQuestions 是跟在陈述后面、把整句变成疑问的附加问句，如“……返场，对吗？”。
	rerunTagQuestions = []string{"对吗", "是吗", "对不对", "是不是", "对么", "是么", "对吧", "是吧"}
	// rerunRowLine 是配表行的“行 N |”开头，rerunCellPrefix 是单元格的列名前缀（规范化后为小写，可带表头，如“a[栏目]=”）。
	rerunRowLine    = regexp.MustCompile(`^行 \d+ \|`)
	rerunCellPrefix = regexp.MustCompile(`^([a-z]+)(?:\[[^\]]*\])?=`)
	// rerunSectionHeading 是小节标题的开头：Markdown 标题或“一、”式编号。
	rerunSectionHeading = regexp.MustCompile(`^(?:#+|[一二三四五六七八九十]+[、.])`)
	// rerunListMarkers 是清单条目的几种开头：符号、数字编号、括号编号、中文编号与 Markdown 标题；同一种开头的是同级条目。
	rerunListMarkers = []*regexp.Regexp{
		regexp.MustCompile(`^[·•●*\-]`),
		regexp.MustCompile(`^\d+[.、)]`),
		regexp.MustCompile(`^\(\d+\)`),
		regexp.MustCompile(`^[一二三四五六七八九十]+[、.]`),
		regexp.MustCompile(`^#+`),
	}
	// rerunShortHeading 是单独成行、可当作标题的短文字的最大字数；带句读的不算标题。
	rerunShortHeading = 16
	// rerunContextChunks 是判断返场声明时最多往前补看的同一小节文本块数。
	rerunContextChunks = 3
	// rerunDocumentNouns 紧跟在行首声明的关系词后时，写的是另一份文档（如“××888活动返场方案”），不是本文档的声明。
	rerunDocumentNouns = []string{"方案", "策划", "文档", "案", "稿"}
)

// qualifiedRerunStatement 判断活动名所在的陈述是否只是引用、转述或提问：所在分句有“参考”等引用词、
// 活动名处在所在行（或配表单元格）里未闭合的引号中、所在分句是疑问句（带“吗”、以问号结尾或后接“对吗”
// 等附加问句），或所在行属于参考标题下的清单或小节（如“参考活动：”下的条目）。这类陈述说的不是本文档，
// 不能作为返场声明。
func qualifiedRerunStatement(text normalizedSpanText, first, last int) bool {
	runes := text.runes
	start := first
	for start > 0 && !strings.ContainsRune(rerunClauseBoundaries, runes[start-1]) {
		start--
	}
	if rerunHasReference(string(runes[start:first])) {
		return true
	}
	// 引号可能跨句：按所在行或配表单元格统计未闭合的引号。
	cellStart := first
	for cellStart > 0 && runes[cellStart-1] != '\n' && runes[cellStart-1] != '|' {
		cellStart--
	}
	cell := string(runes[cellStart:first])
	for _, quotes := range [][2]string{{"“", "”"}, {"「", "」"}, {"『", "』"}} {
		if strings.Count(cell, quotes[0]) > strings.Count(cell, quotes[1]) {
			return true
		}
	}
	if strings.Count(cell, "\"")%2 == 1 {
		return true
	}
	// 疑问只看活动名所在分句，“……返场，是否需要新增入口？”问的是别的事，不算。
	end := last + 1
	for end < len(runes) && !strings.ContainsRune(rerunClauseEnds, runes[end]) {
		end++
	}
	if strings.Contains(string(runes[last+1:end]), "吗") || end < len(runes) && (runes[end] == '？' || runes[end] == '?') {
		return true
	}
	if end < len(runes) && (runes[end] == '，' || runes[end] == ',') {
		next := end + 1
		for next < len(runes) && !strings.ContainsRune(rerunClauseEnds, runes[next]) {
			next++
		}
		if next < len(runes) && (runes[next] == '？' || runes[next] == '?') && slices.Contains(rerunTagQuestions, strings.TrimSpace(string(runes[end+1:next]))) {
			return true
		}
	}
	// 参考清单或小节：活动名之前最近的标题决定它属于哪个清单或小节。参考标题说明只是引用；别的标题（如“本期改动：”）
	// 说明活动名属于另一个清单或小节，不再受更上面的参考标题影响。先看同一行里活动名之前的单元格，再逐行往上。
	lineStart := first
	for lineStart > 0 && runes[lineStart-1] != '\n' {
		lineStart--
	}
	own := rerunLineCells(string(runes[lineStart : last+1]))
	kind := 0
	if len(own) > 0 {
		kind = rerunMarkerKind(own[len(own)-1].text)
	}
	// 同一行：紧挨着活动名所在单元格的标题后面直接写清单条目（如“本期改动： | ·××活动返场”）时，它是这一行的标题；
	// “活动名： | ××活动返场”这类字段名不是标题，继续往前、往上找。
	for index := len(own) - 2; index >= 0; index-- {
		next := own[index+1].column == own[index].column+1 && (index < len(own)-2 || kind == 0)
		if heading, reference := rerunHeading(own[index].text, false, next); heading {
			return reference
		}
	}
	// 往上逐行：清单条目、说明文字和字段行继续往上；与活动名同级的编号条目（如同以“1.”“2.”开头）是兄弟条目，
	// 其中的非参考标题不截断。
	for lineEnd := lineStart - 1; lineEnd > 0; {
		lineHead := lineEnd
		for lineHead > 0 && runes[lineHead-1] != '\n' {
			lineHead--
		}
		cells := rerunLineCells(string(runes[lineHead:lineEnd]))
		if found, reference := rerunLineHeading(cells); found && (reference || kind == 0 || rerunMarkerKind(cells[0].text) != kind) {
			return reference
		}
		lineEnd = lineHead - 1
	}
	return false
}

// rerunHasReference 判断文字里有没有“参考”“借鉴”等引用词。
func rerunHasReference(value string) bool {
	return slices.ContainsFunc(rerunReferenceWords, func(word string) bool { return strings.Contains(value, word) })
}

// rerunColonEnded 判断单元格是否以冒号结尾，即引出下面内容的标题。
func rerunColonEnded(cell string) bool {
	return strings.HasSuffix(cell, "：") || strings.HasSuffix(cell, ":")
}

// rerunMarkerKind 返回条目开头的种类（0 表示不是条目）；同种开头的条目是同一清单的同级条目。
func rerunMarkerKind(cell string) int {
	for index, marker := range rerunListMarkers {
		if marker.MatchString(cell) {
			return index + 1
		}
	}
	return 0
}

// rerunSelfSubject 判断单元格（去掉条目开头后）是否以“本期”“本次”等指代本文档的主语开头，如“本期改动：”。
func rerunSelfSubject(cell string) bool {
	if marker := rerunMarkerKind(cell); marker > 0 {
		cell = strings.TrimSpace(cell[rerunListMarkers[marker-1].FindStringIndex(cell)[1]:])
	}
	return slices.ContainsFunc(rerunSubjects, func(subject string) bool { return strings.HasPrefix(cell, subject) })
}

// rerunHeading 判断单元格是不是清单或小节的标题，以及是不是参考、借鉴一类的标题：
//   - “三、”或 Markdown 小节标题；
//   - 以冒号结尾：带引用词（如“参考活动：”“1.参考活动：”）、以“本期”“本次”等主语开头（如“本期改动：”），或
//     同一行紧挨着的下一列没有内容（next 为 false：内容写在下面几行，或旁边只有备注列）；“活动名： | ××”这类
//     下一列紧跟内容的是字段名，不是标题；
//   - 单独成行（alone）、不带句读的短文字：带引用词（如“参考活动”）或以“本期”等主语开头（如“本期内容”）。
func rerunHeading(cell string, alone, next bool) (heading, reference bool) {
	reference = rerunHasReference(cell)
	switch {
	case rerunSectionHeading.MatchString(cell):
		return true, reference
	case rerunColonEnded(cell):
		return reference || rerunSelfSubject(cell) || !next, reference
	case alone && utf8.RuneCountInString(cell) <= rerunShortHeading && !strings.ContainsAny(cell, "。，,；;！!？?"):
		return reference || rerunSelfSubject(cell), reference
	}
	return false, false
}

// rerunLineHeading 从后往前找一行里离下文最近的标题单元格，返回是否找到以及它是不是参考标题。
func rerunLineHeading(cells []rerunCell) (found, reference bool) {
	for index := len(cells) - 1; index >= 0; index-- {
		next := index+1 < len(cells) && cells[index+1].column == cells[index].column+1
		if heading, reference := rerunHeading(cells[index].text, len(cells) == 1, next); heading {
			return true, reference
		}
	}
	return false, false
}

// rerunReferenceSection 判断文本块所在的小节（标题路径最后一级）是不是参考标题，如“参考活动”“参考表”。
func rerunReferenceSection(headingPathJSON string) bool {
	var path []string
	if json.Unmarshal([]byte(headingPathJSON), &path) != nil || len(path) == 0 {
		return false
	}
	found, reference := rerunLineHeading(rerunLineCells(NormalizeText(path[len(path)-1])))
	return found && reference
}

// rerunCell 是一行里的一个非空单元格正文与列序号：配表按列字母（去掉“行 N”与列名前缀），Markdown、Word 表格按
// “|”分隔的位置（空单元格占位），普通文本整行是一个单元格。
type rerunCell struct {
	text   string
	column int
}

func rerunLineCells(line string) []rerunCell {
	parts := []string{line}
	spreadsheet := rerunRowLine.MatchString(strings.TrimSpace(line))
	if spreadsheet {
		parts = strings.Split(line, "|")[1:]
	} else if strings.Contains(line, "|") {
		parts = strings.Split(line, "|")
	}
	cells := []rerunCell{}
	for index, part := range parts {
		part = strings.TrimSpace(part)
		column := index
		if prefix := rerunCellPrefix.FindStringSubmatchIndex(part); prefix != nil {
			if spreadsheet {
				column = 0
				for _, letter := range part[prefix[2]:prefix[3]] {
					column = column*26 + int(letter-'a') + 1
				}
			}
			part = strings.TrimSpace(part[prefix[1]:])
		}
		if part != "" {
			cells = append(cells, rerunCell{text: part, column: column})
		}
	}
	return cells
}

// titleDeclaresRerun 判断标题是否写明本文档是返场或复用稿；“非返场”“不是复用”“不复用”等否定写法不算。
func titleDeclaresRerun(title string) bool {
	for _, marker := range rerunMarkers {
		for rest := title; ; {
			index := strings.Index(rest, marker)
			if index < 0 {
				break
			}
			if before := rest[:index]; !strings.HasSuffix(before, "非") && !strings.HasSuffix(before, "不是") && !strings.HasSuffix(before, "不") {
				return true
			}
			rest = rest[index+len(marker):]
		}
	}
	return false
}

// declaredRerunContext 判断正文是否明确声明本文档就是活动 span 的返场或复用稿，满足时返回该处的原文片段。
// 只认两种写法；引用、参考、否定、嵌在更长名称里或说不清主语的写法一律不算，文档仍保留为普通候选：
//   - 行首声明：行、单元格、句子或列表项以活动名开头，后接“返场”“复刻”“复用”“重开”（中间只允许“活动”“的”），
//     如“·晨星守望者888活动返场相关调整”；关系词后不能紧跟“方案”“策划”等文档名词。
//   - 主语陈述：以“本次”“本期”“本活动”等指代本文档的主语直接陈述，如“本次为晨星守望者888活动返场”
//     “本期复用晨星守望者888活动的玩法”；主语、系词、关系词与活动名必须紧挨，“本次不复用……”不算。
//
// 两种写法都要求活动名前是边界或主语，因此“破晨星守望者888”这类更长名称里的子串不会被当成目标活动；
// 两种写法还都要排除引用、引号、疑问与参考清单（qualifiedRerunStatement）。
func declaredRerunContext(text normalizedSpanText, span string, radius int) (string, bool) {
	for _, occurrence := range text.occurrences(span) {
		first, last := occurrence[0], occurrence[1]
		before := strings.TrimRight(string(text.runes[max(0, first-64):first]), " ")
		rest := strings.TrimLeft(string(text.runes[last+1:min(len(text.runes), last+17)]), " ")
		rest = strings.TrimPrefix(strings.TrimLeft(strings.TrimPrefix(rest, "活动"), " "), "的")
		declared := false
		for _, marker := range rerunMarkers {
			for _, subject := range rerunSubjects {
				for _, connector := range rerunConnectors {
					// 主语陈述：“本期复用晨星守望者888活动”或“本次为晨星守望者888活动返场”。
					declared = declared || strings.HasSuffix(before, subject+connector+marker) ||
						strings.HasPrefix(rest, marker) && strings.HasSuffix(before, subject+connector)
				}
			}
			if !strings.HasPrefix(rest, marker) || declared {
				continue
			}
			// 行首声明：活动名前是行、单元格、句子或列表项的起点，关系词后不是文档名词。
			boundary, _ := utf8.DecodeLastRuneInString(before)
			tail := strings.TrimPrefix(rest, marker)
			declared = (first == 0 || strings.ContainsRune(rerunLeadBoundaries, boundary)) &&
				!slices.ContainsFunc(rerunDocumentNouns, func(noun string) bool { return strings.HasPrefix(tail, noun) })
		}
		if declared && !qualifiedRerunStatement(text, first, last) {
			return text.context(first, last, radius), true
		}
	}
	return "", false
}

// bodyDeclaredIdentityDocuments 找出标题写出活动名中编号之前的部分、正文写出某份身份文档完整活动名的文档。
// 返场、复用稿常在标题里省略编号，如《晨星守望者限时返场》只在正文写“晨星守望者888活动返场”。
// 正文里的活动名必须与身份文档标题或目录中的活动名完全一致，避免“晨星推送礼包888”这类档位数字误入；
// 标题还须写出编号之前的完整名称（如“晨星守望者”），只写实体名、正文引用该活动的新活动（如《晨星新活动》
// 正文写“参考晨星守望者888活动”）不算。满足这两条的文档进入身份门槛，作为普通候选排序；
// 只有正文明确声明本文档就是该活动的返场或复用稿（declaredRerunContext），且标题也写明返场或复用
// （titleDeclaresRerun），才标为 declared，获得返场稿的名额与提示；只满足其一的文档仍是普通候选。
// 标题写“复刻”不够：《晨星守望者周年庆复刻》可能复刻的是周年庆，正文只是参考了 888 活动。
// preceding 返回候选文本块之前同一小节的文本块（可为 nil），用来补全从清单中间开始的文本块的上下文。
// 这是基于标题与正文的推断，调用方仍应核对原文。
func bodyDeclaredIdentityDocuments(rows []LexicalCandidateRow, identityIDs map[string]bool, groups []documentIdentityGroup, preceding func(LexicalCandidateRow) []string) map[string]bodyIdentityMatch {
	result := map[string]bodyIdentityMatch{}
	for _, group := range groups {
		// 活动名对应的身份文档：标题写出的优先于只在目录写出的，同类取最新。
		owners, pathOwners := map[string]*LexicalCandidateRow{}, map[string]*LexicalCandidateRow{}
		for index := range rows {
			row := &rows[index]
			if !identityIDs[row.ID] {
				continue
			}
			for _, span := range identitySpans(row.Title, group) {
				if newerIdentityExample(row, owners[span]) {
					owners[span] = row
				}
			}
			for _, span := range identitySpans(row.RelativePath, group) {
				if newerIdentityExample(row, pathOwners[span]) {
					pathOwners[span] = row
				}
			}
		}
		for span, row := range pathOwners {
			if owners[span] == nil {
				owners[span] = row
			}
		}
		if len(owners) == 0 {
			continue
		}
		spans := make([]string, 0, len(owners))
		for span := range owners {
			spans = append(spans, span)
		}
		sort.Strings(spans)
		for _, row := range rows {
			if existing, done := result[row.ID]; done && existing.declared || identityIDs[row.ID] || !namesStandalone(row.Title, group.Terms[0]) {
				continue
			}
			text := normalizedSpanText{}
			for _, span := range spans {
				if !namesStandalone(row.Title, strings.TrimSuffix(span, group.Terms[len(group.Terms)-1])) {
					continue
				}
				if text.runes == nil {
					text = newNormalizedSpanText(row.Text)
				}
				if len(text.occurrences(span)) == 0 {
					continue
				}
				match := bodyIdentityMatch{name: span, ownerTitle: owners[span].Title}
				match.context, match.declared = declaredRerunContext(text, span, 16)
				// 返场名额还要求标题也写明返场或复用：正文措辞再复杂，标题与正文同时声明才可靠；
				// 文本块所在小节本身是参考小节（如“参考活动”）时，其中的陈述都不算。
				match.declared = match.declared && titleDeclaresRerun(row.Title) && !rerunReferenceSection(row.HeadingPathJSON)
				// 文本块可能从清单中间开始（Markdown 按空行分段、Word 表格与前面的说明段分开、长配表分块）：
				// 带上同一小节紧挨着的前几个文本块再判断一次，参考标题落在前一个文本块里也能找到。
				if match.declared && row.Ordinal > 0 && preceding != nil {
					if earlier := preceding(row); len(earlier) > 0 {
						match.context, match.declared = declaredRerunContext(newNormalizedSpanText(strings.Join(append(earlier, row.Text), "\n")), span, 16)
					}
				}
				if _, found := result[row.ID]; !found || match.declared {
					result[row.ID] = match
				}
				if match.declared {
					break
				}
			}
		}
	}
	return result
}

// bodyIdentityNotes 附正文原文提示标题未写编号、正文写明返场或复用的文档可能属于哪个活动，每个活动名只列最新的一份。
// 调用方只看标题时无法把这类返场、复用稿和原案联系起来，容易转而采用标题带“复用”的其他活动。
func bodyIdentityNotes(rows []LexicalCandidateRow, matches map[string]bodyIdentityMatch) []string {
	newest := map[string]*LexicalCandidateRow{}
	names := []string{}
	for index := range rows {
		row := &rows[index]
		match, ok := matches[row.ID]
		if !ok || !match.declared {
			continue
		}
		if newest[match.name] == nil {
			names = append(names, match.name)
		}
		if newerIdentityExample(row, newest[match.name]) {
			newest[match.name] = row
		}
	}
	notes := make([]string, 0, len(names))
	for _, name := range names {
		row := newest[name]
		match := matches[row.ID]
		notes = append(notes, fmt.Sprintf("《%s》标题未写编号，正文写有“%s”，可能是《%s》所属活动的返场或复用稿，请核对原文后采用。", row.Title, match.context, match.ownerTitle))
	}
	return notes
}

// titleIdentityKind 判断标题是否命中活动身份，以及命中是否以独立名称出现。
func titleIdentityKind(title string, groups []documentIdentityGroup) (matched, standalone bool) {
	for _, group := range groups {
		if identityGroupScore(title, group) > 0 {
			matched = true
			standalone = standalone || identityGroupAtBoundary(title, group)
		}
	}
	return matched, standalone
}

// newerIdentityExample 比较两份候选作为提示示例的优先级：日期新者优先，同日期取标题较短者
// （主策划通常不带“美术需求”“数值补充”这类附加说明），再按标题排序保证结果确定。
func newerIdentityExample(candidate, current *LexicalCandidateRow) bool {
	if current == nil || candidate.EffectiveUpdatedAtMS != current.EffectiveUpdatedAtMS {
		return current == nil || candidate.EffectiveUpdatedAtMS > current.EffectiveUpdatedAtMS
	}
	candidateLength, currentLength := utf8.RuneCountInString(candidate.Title), utf8.RuneCountInString(current.Title)
	if candidateLength != currentLength {
		return candidateLength < currentLength
	}
	return candidate.Title < current.Title
}

// identityAmbiguityWarnings 在同一活动身份既以独立名称出现在标题中、又只出现在更长的标题名称里时给出提示，
// 例如“晨星888”同时命中《晨星·守望者888活动》和《破晨星·裂空888活动》。两者可能是不同活动，
// 按日期排序时更长名称的文档还可能排在前面，因此列出两类中最新的标题供调用方区分，并说明用户的写法按字面
// 对应独立名称：默认以它为主回答，更长名称的活动作为另一种可能说明，与相关度排序一致。
func identityAmbiguityWarnings(rows []LexicalCandidateRow, groups []documentIdentityGroup) []string {
	warnings := []string{}
	for _, group := range groups {
		var standalone, embedded *LexicalCandidateRow
		for index := range rows {
			row := &rows[index]
			if identityGroupScore(row.Title, group) <= 0 {
				continue
			}
			if identityGroupAtBoundary(row.Title, group) {
				if newerIdentityExample(row, standalone) {
					standalone = row
				}
			} else if newerIdentityExample(row, embedded) {
				embedded = row
			}
		}
		if standalone != nil && embedded != nil {
			warnings = append(warnings, fmt.Sprintf("“%s”按字面对应独立名称《%s》；更长的名称《%s》只是在名称内部包含它，可能是另一个活动。除非用户明确指的是后者，请以独立名称的活动为主回答，并说明另一种可能；原文未写明时不要推断两者之间的复用关系。", group.Phrase, standalone.Title, embedded.Title))
		}
	}
	return warnings
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
		// “ID”等只表示字段类别；两个字母的大写缩写也过短，不能作为必须命中的文档锚点。
		if genericASCIIQueryWords[normalized] {
			continue
		}
		explicit = appendUnique(explicit, normalized)
		if uppercasePattern.MatchString(anchor) && len(anchor) >= 3 || documentMarkPattern.MatchString(anchor) || len(anchor) >= 8 {
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
	concepts, phrases := buildQueryConcepts(query, config.Search.SynonymExpansion)
	lexicalTerms := QueryLexicalTerms(query, config.Search.SynonymExpansion)
	lexicalTokens := []string{}
	for _, term := range lexicalTerms {
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
	excerptLength := defaultExcerptLength
	if documentRestricted {
		excerptLength = max(defaultExcerptLength, min(targetedExcerptLength, scope.ExcerptLength))
	}
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
		conceptMatches, err := engine.conceptFTSMatches(ctx, snapshotRevision, concepts)
		if err != nil {
			return response, err
		}
		var rows []LexicalCandidateRow
		if len(conceptMatches) > 0 {
			rows, err = engine.database.ConceptCandidates(ctx, conceptMatches, indexedLimit, max(8, excerptLimit*3), filter)
		} else {
			rows, err = engine.database.LexicalCandidates(ctx, strings.Join(lexicalParts, " OR "), indexedLimit, filter)
		}
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
			rows, err := engine.database.LikeCandidates(ctx, lexicalTerms, 800, filter)
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
	bodyIdentityIDs, declaredRerunIDs := map[string]bool{}, map[string]bool{}
	if !documentRestricted {
		identityPriority := len(signals.IdentityGroups) > 0 && (!tableIntent || designOnly)
		for _, row := range rows {
			if namedDocumentIdentityScore(row.Title, row.RelativePath, signals.IdentityGroups) > 0 {
				identityDocumentIDs[row.ID] = true
			}
		}
		bodyIdentity := bodyDeclaredIdentityDocuments(rows, identityDocumentIDs, signals.IdentityGroups, func(row LexicalCandidateRow) []string {
			texts, err := engine.database.PrecedingSectionChunks(ctx, row.ID, row.Ordinal, row.HeadingPathJSON, rerunContextChunks)
			if err != nil {
				return nil
			}
			return texts
		})
		for id, match := range bodyIdentity {
			identityDocumentIDs[id] = true
			bodyIdentityIDs[id] = true
			declaredRerunIDs[id] = match.declared
		}
		response.Warnings = append(response.Warnings, bodyIdentityNotes(rows, bodyIdentity)...)
		// 实体名嵌在更长名称里的文档（如“晨星888”命中“破晨星·裂空888”）可能正是用户所指，也可能是另一个活动：
		// 保留它们，只让相关度低于独立名称；两类同时出现时提示调用方按标题区分。
		response.Warnings = append(response.Warnings, identityAmbiguityWarnings(rows, signals.IdentityGroups)...)
		if identityPriority && len(identityDocumentIDs) > 0 {
			rows = filterCandidateRows(rows, func(row LexicalCandidateRow) bool { return identityDocumentIDs[row.ID] })
		} else if len(signals.IdentityGroups) > 0 && len(identityDocumentIDs) > 0 {
			// 配表意图同时检索策划与配表：配表不按活动身份过滤，策划只保留命中该活动身份的文档，
			// 避免其他同编号活动（如别的 888 活动）因更新而挤掉目标策划。
			rows = filterCandidateRows(rows, func(row LexicalCandidateRow) bool { return row.SourceKind != "design" || identityDocumentIDs[row.ID] })
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
		candidate := scoreSearchCandidate(row, normalizedFor(row), concepts, phrases, semanticScores[row.ChunkID], declaredRerunIDs[row.ID])
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
			projection, err := MakeExcerpt(candidate.row.Text, candidate.row.Locator, projectionTerms, excerptLength)
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
		hits = append(hits, SearchHit{DocumentID: best.row.ID, SourceID: best.row.SourceID, SourceLabel: best.row.SourceLabel, SourceKind: best.row.SourceKind, Title: best.row.Title, AbsolutePath: best.row.AbsolutePath, RelativePath: best.row.RelativePath, Extension: best.row.Extension, EffectiveUpdatedAt: best.row.EffectiveUpdatedAt, DateSource: best.row.DateSource, FilesystemModifiedAt: best.row.FilesystemModifiedAt, Relevance: relevance, FamilyKey: best.row.FamilyKey, FamilyConfidence: best.row.FamilyConfidence, Stale: best.row.Stale, SectionTypes: sectionTypes, Excerpts: excerpts, bodyIdentity: bodyIdentityIDs[best.row.ID], declaredRerun: declaredRerunIDs[best.row.ID]})
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
		// 相关度门槛按来源类型分别计算：配表多靠路径和单元格命中，不与中文标题命中的策划案直接比较。
		bestRelevance := map[string]float64{}
		for _, hit := range hits {
			bestRelevance[hit.SourceKind] = math.Max(bestRelevance[hit.SourceKind], hit.Relevance)
		}
		qualityFloor := func(kind string) float64 {
			return math.Min(bestRelevance[kind], math.Max(0.3, bestRelevance[kind]*0.7))
		}
		requiredSet := map[string]bool{}
		for _, id := range requiredExactIDs {
			requiredSet[id] = true
		}
		hits = filterHits(hits, func(hit SearchHit) bool {
			return requiredSet[hit.DocumentID] || identityDocumentIDs[hit.DocumentID] || documentIdentityRoleScore(hit, rankContext) >= 0.9 || hit.Relevance >= qualityFloor(hit.SourceKind)
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
		candidateScope = &searchCandidateScope{DocumentIDs: selectedDocumentIDs, ChunksPerDocument: max(12, maxChunks*3), ExcerptLength: maxChars / (len(selectedDocumentIDs) * maxChunks)}
	}
	baseRequest := copySearchRequest(request)
	// 同时选了策划与配表等同于不限来源类型：仍分别检索两类来源，保留活动身份必选与配表配额。
	if len(baseRequest.SourceIDs) == 0 && containsString(baseRequest.SourceKinds, "design") && containsString(baseRequest.SourceKinds, "table") {
		baseRequest.SourceKinds = nil
	}
	var search SearchResponse
	if len(baseRequest.SourceIDs) == 0 && len(baseRequest.SourceKinds) == 0 {
		// 每类来源多取候选，按相关度补位时才能看到较早但更相关的文档。
		perSourceLimit := max(maxDocuments*3, baseRequest.Limit)
		signals := QueryAnchorSignals(baseRequest.Query)
		if len(signals.IdentityGroups) > 0 {
			// 活动身份问题要在候选里找到独立名称的原案；证据包很小时只取几个候选，
			// 较新的返场稿和名称更长的同编号活动会把较早的原案挤出候选。
			perSourceLimit = max(perSourceLimit, 12)
		}
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
		rank := documentRankContext{Query: baseRequest.Query, Terms: uniqueNormalizedTerms(ExpandQueryTerms(baseRequest.Query, config.Search.SynonymExpansion)), Signals: signals}
		primary, secondary := designSearch.Hits, tableSearch.Hits
		if tableFirst {
			primary, secondary = tableSearch.Hits, designSearch.Hits
		}
		restrictIdentity := !tableFirst && (len(signals.IdentityGroups) > 0 || (signals.LatestIntent && len(signals.DocumentAnchors) > 0))
		if restrictIdentity {
			identityIDs := map[string]bool{}
			for _, hit := range append(append([]SearchHit{}, primary...), secondary...) {
				if hit.bodyIdentity || matchesIdentitySignals(hit.Title, hit.RelativePath, signals) {
					identityIDs[hit.DocumentID] = true
				}
			}
			if len(identityIDs) > 0 {
				primary = filterHits(primary, func(hit SearchHit) bool { return identityIDs[hit.DocumentID] })
				secondary = filterHits(secondary, func(hit SearchHit) bool { return identityIDs[hit.DocumentID] })
			}
		}
		primaryQuota := (maxDocuments*3 + 3) / 4
		// 候选列表按日期排列；同分时 SliceStable 保留新者在前。
		byRelevance := func(hits []SearchHit) []SearchHit {
			result := append([]SearchHit{}, hits...)
			sort.SliceStable(result, func(i, j int) bool { return result[i].Relevance > result[j].Relevance })
			return result
		}
		primaryByRelevance, secondaryByRelevance := byRelevance(primary), byRelevance(secondary)
		selectedPrimary := append([]SearchHit{}, primary[:min(primaryQuota, len(primary))]...)
		if tableFirst {
			// 配表的更新时间反映任意一行的改动，汇总表（活动总表、模块表）几乎每个版本都会更新，
			// 按日期挑表会让它们挤掉目录、标题或名称列直接命中的专用表，所以配表按相关度挑选。
			selectedPrimary = append([]SearchHit{}, primaryByRelevance[:min(primaryQuota, len(primaryByRelevance))]...)
			engine.sortHits(selectedPrimary, "newest", &rank)
		}
		// 辅助来源只占少量名额，按相关度挑选，避免被最近更新但只顺带提到关键词的汇总表占满。
		quotaHits := append(append([]SearchHit{}, selectedPrimary...), secondaryByRelevance[:min(max(0, maxDocuments-primaryQuota), len(secondaryByRelevance))]...)
		allCandidates := append(append([]SearchHit{}, primary...), secondary...)
		fillCandidates := append(append([]SearchHit{}, primaryByRelevance...), secondaryByRelevance...)
		requiredHits := []SearchHit{}
		if len(signals.IdentityGroups) > 0 {
			// 活动身份问题把标题以独立名称命中的策划列为必选：按日期排序时，名称更长、更新的同编号活动
			// 不会把它挤出证据包。没有标题命中时退到只有目录命中的文档（如同名目录下的剧情稿）。
			var titleMatch, pathMatch *SearchHit
			bestTitleScore := 0.0
			for index := range designSearch.Hits {
				hit := &designSearch.Hits[index]
				if score := namedDocumentIdentityScore(hit.Title, "", signals.IdentityGroups); score > bestTitleScore {
					titleMatch, bestTitleScore = hit, score
				}
				if pathMatch == nil && matchesIdentitySignals(hit.Title, hit.RelativePath, signals) {
					pathMatch = hit
				}
			}
			if titleMatch != nil {
				requiredHits = append(requiredHits, *titleMatch)
				// 策划名额够时再依次带上两类文档中最新的一份：同一活动标题省略编号的返场、复用稿（正文写明完整活动名），
				// 以及名称更长的同编号活动（覆盖另一种理解，区别由 warnings 中的歧义提示说明）。
				// 名额按策划计算，以免在配表优先的小证据包里挤掉配表；配表不足配额时，空出的名额归策划。
				// 活动身份问题的配置清单主要写在策划里，配表优先时策划仍至少占 3 个名额（不超过总数一半）。
				designSlots := maxDocuments
				if tableFirst {
					designSlots = max(maxDocuments-min(primaryQuota, len(primary)), min(3, maxDocuments/2))
				}
				_, standaloneTitle := titleIdentityKind(titleMatch.Title, signals.IdentityGroups)
				extras := []func(SearchHit) bool{
					func(hit SearchHit) bool { return hit.declaredRerun },
					func(hit SearchHit) bool {
						matched, standalone := titleIdentityKind(hit.Title, signals.IdentityGroups)
						return standaloneTitle && matched && !standalone
					},
				}
				for _, wanted := range extras {
					for index := range designSearch.Hits {
						if len(requiredHits) < designSlots && wanted(designSearch.Hits[index]) {
							requiredHits = append(requiredHits, designSearch.Hits[index])
							break
						}
					}
				}
			} else if pathMatch != nil {
				requiredHits = append(requiredHits, *pathMatch)
			}
		}
		identityNumbers := map[string]bool{}
		if len(requiredHits) > 0 {
			for _, group := range signals.IdentityGroups {
				identityNumbers[group.Terms[len(group.Terms)-1]] = true
			}
		}
		for _, anchor := range signals.DocumentAnchors {
			// 活动身份里的编号（如“晨星888”的 888）已由上面的身份必选覆盖，不再按显式 ID 另选文档。
			if identityNumbers[anchor] {
				continue
			}
			// 显式 ID 优先选标题或路径就是该 ID 的文档，其次才是正文提到它的文档。
			var chosen *SearchHit
			for index := range allCandidates {
				if matchesDocumentIdentity(allCandidates[index].Title, allCandidates[index].RelativePath, []string{anchor}) {
					chosen = &allCandidates[index]
					break
				}
			}
			if chosen == nil && !restrictIdentity {
				for index := range allCandidates {
					if strings.Contains(hitHaystack(allCandidates[index]), anchor) {
						chosen = &allCandidates[index]
						break
					}
				}
			}
			if chosen != nil {
				requiredHits = append(requiredHits, *chosen)
			}
		}
		selected := map[string]SearchHit{}
		order := []string{}
		for _, hit := range append(requiredHits, append(quotaHits, fillCandidates...)...) {
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
