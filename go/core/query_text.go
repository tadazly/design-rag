package core

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

var synonymGroups = [][]string{
	{"轮盘", "转盘", "幸运轮盘", "抽奖轮盘", "roulette"},
	{"抽奖", "抽取", "奖池", "概率", "保底", "扭蛋"},
	{"签到", "登录奖励", "每日登录", "累签", "补签"},
	{"复用", "沿用", "套用", "模板", "通用版"},
	{"历史改动", "版本记录", "修改记录", "更新记录", "迭代记录", "变更记录"},
	{"配置", "配表", "字段", "参数", "数值"},
	{"流程", "步骤", "交互", "逻辑", "时序"},
	{"玩法", "规则", "机制"},
	{"奖励", "奖品", "掉落", "兑换", "产出"},
}

var numericTermPattern = regexp.MustCompile(`^\d+$`)

var queryClauseSeparator = regexp.MustCompile(`[\s,，。！？、;；:：]+`)

// queryStopPhrases 是自然语言提问里的请求、疑问、时间和虚词片段。抽取关键词时把它们当作分隔符，
// 留下实体名、玩法名和配置名等核心词，避免“需要”“一个”这类泛化双字词撑满 FTS 候选并主导相关度。
var queryStopPhrases = []string{
	"我需要", "我要", "我想", "想要", "需要", "帮我把", "帮我", "给我", "请问", "麻烦", "帮忙",
	"看一下", "看看", "看下", "查一下", "查下", "找一下", "找到", "找出", "查找", "查询", "搜索", "告诉我",
	"说明一下", "解释一下", "介绍一下", "整理一下", "总结一下", "列出来", "列一下", "列出",
	"有哪些", "有没有", "是多少", "是什么", "哪些", "哪个", "哪张", "哪里", "什么", "怎么样", "怎么", "怎样", "如何", "多少",
	"最近几期", "最新的", "最新", "最近", "新增", "新的", "一个", "一下", "这个", "那个", "这些", "那些",
	"里面的", "里面", "其中", "以及", "还有", "分别", "可以", "能否", "能不能", "应该",
	"的", "了", "吗", "呢", "吧", "把", "和", "与", "及",
	// “表格”在提问中表达“要配哪些表”的意图，已由 tableIntentPattern 识别；它在语料里罕见，
	// 若作为内容关键词会因区分度高而压过实体名。
	"表格",
	// “产出逻辑”“玩法逻辑”里的“逻辑”表示要解释运作方式；策划的“面板&逻辑”“玩法&逻辑”页名都含这个词，
	// 作为内容关键词会让这些页压过“奖励数值”等真正相关的页。
	"逻辑",
}

// genericASCIIQueryWords 在策划提问里只表示字段类别，不能作为文档锚点或关键词。
var genericASCIIQueryWords = map[string]bool{"id": true, "ids": true}

func init() {
	sort.SliceStable(queryStopPhrases, func(i, j int) bool {
		return utf8.RuneCountInString(queryStopPhrases[i]) > utf8.RuneCountInString(queryStopPhrases[j])
	})
}

func isASCIILetter(r rune) bool {
	return r < utf8.RuneSelf && unicode.IsLetter(r)
}

// keywordVocabularyExtras 是切分长片段时额外使用的领域名词；同义词组中的词也会加入词表。
var keywordVocabularyExtras = []string{
	"活动", "玩法", "策划", "版本", "道具", "精灵", "皮肤", "礼包", "商店", "任务", "剧情", "产出", "消耗", "设计", "内容",
}

var keywordVocabulary []string

func init() {
	seen := map[string]bool{}
	for _, group := range append(append([][]string{}, synonymGroups...), keywordVocabularyExtras) {
		for _, term := range group {
			term = NormalizeText(term)
			runes := []rune(term)
			if len(runes) >= 2 && isCJK(runes[0]) && !seen[term] {
				seen[term] = true
				keywordVocabulary = append(keywordVocabulary, term)
			}
		}
	}
	sort.SliceStable(keywordVocabulary, func(i, j int) bool {
		return utf8.RuneCountInString(keywordVocabulary[i]) > utf8.RuneCountInString(keywordVocabulary[j])
	})
}

// segmentKeyword 用领域词表对中文片段做最长匹配切分，如“复用轮盘抽奖活动”切为“复用/轮盘/抽奖/活动”。
// 只有每一段都不少于两个字时才采用切分结果，因此“扭蛋机”“星河龙888”这类专名保持完整。
func segmentKeyword(fragment []rune) [][]rune {
	pieces := [][]rune{}
	buffer := []rune{}
	for index := 0; index < len(fragment); {
		matched := ""
		for _, term := range keywordVocabulary {
			if strings.HasPrefix(string(fragment[index:]), term) {
				matched = term
				break
			}
		}
		if matched == "" {
			buffer = append(buffer, fragment[index])
			index++
			continue
		}
		if len(buffer) > 0 {
			pieces = append(pieces, buffer)
			buffer = []rune{}
		}
		pieces = append(pieces, []rune(matched))
		index += utf8.RuneCountInString(matched)
	}
	if len(buffer) > 0 {
		pieces = append(pieces, buffer)
	}
	for _, piece := range pieces {
		if len(piece) < 2 {
			return [][]rune{fragment}
		}
	}
	return pieces
}

func isKeywordRune(r rune) bool {
	return isCJK(r) || unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_./:-", r)
}

// QueryKeywordTerms 从规范化查询中去掉停用片段，在标点、中英文字母交界处切分，再用领域词表切开长片段，
// 返回至少两个字符的核心词。
func QueryKeywordTerms(normalized string) []string {
	result := []string{}
	seen := map[string]bool{}
	add := func(value []rune) {
		for _, piece := range segmentKeyword(value) {
			fragment := strings.Trim(string(piece), "_./:-")
			if utf8.RuneCountInString(fragment) >= 2 && !genericASCIIQueryWords[fragment] && !seen[fragment] {
				seen[fragment] = true
				result = append(result, fragment)
			}
		}
	}
	runes := []rune(normalized)
	current := []rune{}
	flush := func() {
		if len(current) > 0 {
			add(current)
			current = []rune{}
		}
	}
	for index := 0; index < len(runes); {
		if !isKeywordRune(runes[index]) {
			flush()
			index++
			continue
		}
		matched := ""
		for _, phrase := range queryStopPhrases {
			if strings.HasPrefix(string(runes[index:]), phrase) {
				matched = phrase
				break
			}
		}
		if matched != "" {
			flush()
			index += utf8.RuneCountInString(matched)
			continue
		}
		if len(current) > 0 && (isCJK(runes[index]) && isASCIILetter(current[len(current)-1]) || isASCIILetter(runes[index]) && isCJK(current[len(current)-1])) {
			flush()
		}
		current = append(current, runes[index])
		index++
	}
	flush()
	return result
}

func matchedSynonymTerms(normalized string) []string {
	result := []string{}
	for _, group := range synonymGroups {
		matched := false
		for _, term := range group {
			if strings.Contains(normalized, NormalizeText(term)) {
				matched = true
				break
			}
		}
		if matched {
			result = append(result, group...)
		}
	}
	return result
}

func ExpandQueryTerms(query string, enabled bool) []string {
	normalized := NormalizeText(query)
	result := []string{}
	seen := map[string]bool{}
	add := func(value string) {
		value = NormalizeText(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	add(normalized)
	for _, value := range queryClauseSeparator.Split(normalized, -1) {
		if len([]rune(value)) >= 2 {
			add(value)
		}
	}
	for _, keyword := range QueryKeywordTerms(normalized) {
		add(keyword)
	}
	if enabled {
		for _, term := range matchedSynonymTerms(normalized) {
			add(term)
		}
	}
	return result
}

// QueryLexicalTerms 返回用于生成 FTS 词元的检索词：关键词与同义词。整句和子句只在抽不出关键词时使用，
// 以免其中请求词、疑问词形成的双字词把候选扩大到全库。
func QueryLexicalTerms(query string, enabled bool) []string {
	normalized := NormalizeText(query)
	keywords := QueryKeywordTerms(normalized)
	if len(keywords) == 0 {
		return ExpandQueryTerms(query, enabled)
	}
	result := append([]string{}, keywords...)
	if enabled {
		seen := map[string]bool{}
		for _, keyword := range keywords {
			seen[keyword] = true
		}
		for _, term := range matchedSynonymTerms(normalized) {
			term = NormalizeText(term)
			if !seen[term] {
				seen[term] = true
				result = append(result, term)
			}
		}
	}
	return result
}

func QueryConceptGroups(query string) [][]string {
	normalized := NormalizeText(query)
	result := [][]string{}
	for _, group := range synonymGroups {
		matched := false
		normalizedGroup := make([]string, len(group))
		for index, term := range group {
			normalizedGroup[index] = NormalizeText(term)
			if strings.Contains(normalized, normalizedGroup[index]) {
				matched = true
			}
		}
		if matched {
			result = append(result, normalizedGroup)
		}
	}
	return result
}

func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

func CJKSearchTerms(value string) []string {
	normalized := NormalizeText(value)
	result := []string{}
	seen := map[string]bool{}
	add := func(value string) {
		if utf8.RuneCountInString(value) > 0 && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	var ascii []rune
	var cjk []rune
	flushASCII := func() {
		if len(ascii) > 1 {
			add(string(ascii))
		}
		ascii = ascii[:0]
	}
	flushCJK := func() {
		for _, r := range cjk {
			add(string(r))
		}
		for index := 0; index+1 < len(cjk); index++ {
			add(string(cjk[index : index+2]))
		}
		if len(cjk) > 0 && len(cjk) <= 8 {
			add(string(cjk))
		}
		cjk = cjk[:0]
	}
	for _, r := range normalized {
		if isCJK(r) {
			flushASCII()
			cjk = append(cjk, r)
			continue
		}
		flushCJK()
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_./:-", r) {
			ascii = append(ascii, unicode.ToLower(r))
		} else {
			flushASCII()
		}
	}
	flushASCII()
	flushCJK()
	return result
}

// FTSConjunction 把一个检索词转换为 FTS5 AND 表达式：中文按相邻双字（索引只存双字词元），
// 英文数字按整段词元。没有可检索词元时返回空串。
func FTSConjunction(term string) string {
	tokens := []string{}
	add := func(value string) {
		if !containsString(tokens, value) {
			tokens = append(tokens, value)
		}
	}
	var ascii, cjk []rune
	flushASCII := func() {
		if len(ascii) > 1 {
			add(string(ascii))
		}
		ascii = ascii[:0]
	}
	flushCJK := func() {
		for index := 0; index+1 < len(cjk); index++ {
			add(string(cjk[index : index+2]))
		}
		cjk = cjk[:0]
	}
	for _, r := range NormalizeText(term) {
		if isCJK(r) {
			flushASCII()
			cjk = append(cjk, r)
			continue
		}
		flushCJK()
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_./:-", r) {
			ascii = append(ascii, unicode.ToLower(r))
		} else {
			flushASCII()
		}
	}
	flushASCII()
	flushCJK()
	parts := make([]string, len(tokens))
	for index, token := range tokens {
		parts[index] = EscapeFTSToken(token)
	}
	return strings.Join(parts, " AND ")
}

func EscapeFTSToken(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func HighlightTerms(value string, terms []string) string {
	seen := map[string]bool{}
	unique := []string{}
	for _, term := range terms {
		term = NormalizeText(term)
		if len([]rune(term)) >= 2 && !seen[term] {
			seen[term] = true
			unique = append(unique, term)
		}
	}
	sort.SliceStable(unique, func(i, j int) bool { return len([]rune(unique[i])) > len([]rune(unique[j])) })
	if len(unique) > 12 {
		unique = unique[:12]
	}
	result := value
	for _, term := range unique {
		pattern, err := regexp.Compile(`(?i)` + regexp.QuoteMeta(term))
		if err == nil {
			result = pattern.ReplaceAllStringFunc(result, func(match string) string { return "**" + match + "**" })
		}
	}
	return result
}

func uniqueNormalizedTerms(values []string) []string {
	type entry struct {
		value string
		index int
	}
	seen := map[string]bool{}
	entries := []entry{}
	for _, value := range values {
		value = NormalizeText(value)
		if len([]rune(value)) >= 2 && !seen[value] {
			seen[value] = true
			entries = append(entries, entry{value, len(entries)})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		leftNumeric := numericTermPattern.MatchString(entries[i].value)
		rightNumeric := numericTermPattern.MatchString(entries[j].value)
		if leftNumeric != rightNumeric {
			return !leftNumeric
		}
		leftLength := len([]rune(entries[i].value))
		rightLength := len([]rune(entries[j].value))
		if leftLength != rightLength {
			return leftLength > rightLength
		}
		return entries[i].index < entries[j].index
	})
	result := make([]string, len(entries))
	for index, item := range entries {
		result[index] = item.value
	}
	return result
}
