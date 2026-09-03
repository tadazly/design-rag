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
	{"奖励", "奖品", "掉落", "兑换"},
}

var numericTermPattern = regexp.MustCompile(`^\d+$`)

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
	for _, value := range regexp.MustCompile(`[\s,，。！？、;；:：]+`).Split(normalized, -1) {
		if len([]rune(value)) >= 2 {
			add(value)
		}
	}
	if enabled {
		for _, group := range synonymGroups {
			matched := false
			for _, term := range group {
				if strings.Contains(normalized, NormalizeText(term)) {
					matched = true
					break
				}
			}
			if matched {
				for _, term := range group {
					add(term)
				}
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
