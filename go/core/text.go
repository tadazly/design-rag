package core

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

var (
	spacePattern       = regexp.MustCompile(`\s+`)
	zeroWidthPattern   = regexp.MustCompile(`[\x{200B}-\x{200D}\x{FEFF}]`)
	dateSeparated      = regexp.MustCompile(`(^|\D)(20\d{2})[-/.年_](0?[1-9]|1[0-2])([-/.月_](0?[1-9]|[12]\d|3[01])日?)?(\D|$)`)
	dateCompact        = regexp.MustCompile(`(^|\D)(20\d{2})(0[1-9]|1[0-2])([0-2]\d|3[01])(\D|$)`)
	versionLinePattern = regexp.MustCompile(`(?i)(版本|修改|更新|复用|迭代|变更|version|revision)`)
	familyPatterns     = []*regexp.Regexp{
		regexp.MustCompile(`【\s*(复用|通用|历史|旧版|最终版?)\s*】`),
		regexp.MustCompile(`[（(]\s*(复用|通用|历史|旧版|最终版?)\s*[)）]`),
		regexp.MustCompile(`(?i)\b(v|ver|version)\s*\d+(\.\d+){0,3}\b`),
		regexp.MustCompile(`20\d{2}[-_.年/]?\d{1,2}([-_.月/]?\d{1,2}日?)?`),
		regexp.MustCompile(`[_\-—]+`),
	}
	familyGenericSuffix = regexp.MustCompile(`(策划案|需求文档|配置表|活动方案|说明文档)$`)
	sectionRules        = []sectionRule{
		{"version_history", regexp.MustCompile(`(?i)(版本|修订|修改记录|更新记录|历史改动|变更|迭代|复用记录|revision|changelog)`)},
		{"animation_requirement", regexp.MustCompile(`(?i)(动画|动效|特效需求|动画需求)`)},
		{"art_requirement", regexp.MustCompile(`(?i)(美术|原画|资源需求|音效|ui需求)`)},
		{"statistics", regexp.MustCompile(`(?i)(统计|埋点|数据上报|行为分析)`)},
		{"reward_value", regexp.MustCompile(`(?i)(奖励数值|奖励配置|奖励和消耗|奖品价值|价值表)`)},
		{"panel_logic", regexp.MustCompile(`(?i)(面板.*逻辑|界面逻辑|panel|页面逻辑)`)},
		{"flow", regexp.MustCompile(`(?i)(流程|步骤|交互|逻辑|入口|界面流转|状态机|时序)`)},
		{"gameplay", regexp.MustCompile(`(?i)(玩法|规则|奖励|概率|抽奖|轮盘|转盘|奖池|保底|次数|积分|活动)`)},
		{"config", regexp.MustCompile(`(?i)(配置|配表|字段|参数|数值|id\b|枚举|掉落|activity|module|dropunit)`)},
		{"overview", regexp.MustCompile(`(?i)(概述|背景|目标|需求说明|简介|总览|summary|overview)`)},
	}
)

type sectionRule struct {
	typeName string
	pattern  *regexp.Regexp
}

func NormalizeText(value string) string {
	value = norm.NFKC.String(value)
	value = strings.ToLower(strings.TrimSpace(zeroWidthPattern.ReplaceAllString(value, "")))
	return spacePattern.ReplaceAllString(value, " ")
}

func HashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func HashString(value string) string { return HashBytes([]byte(value)) }

func CanonicalPathKey(value string) string {
	abs, err := filepath.Abs(value)
	if err == nil {
		value = abs
	}
	value = norm.NFC.String(filepath.Clean(value))
	if filepath.Separator == '\\' {
		value = strings.ToLower(value)
	}
	return value
}

func ClassifySection(headings []string, text string) string {
	heading := NormalizeText(strings.Join(headings, " / "))
	for _, rule := range sectionRules {
		if heading != "" && rule.pattern.MatchString(heading) {
			return rule.typeName
		}
	}
	sample := []rune(NormalizeText(text))
	if len(sample) > 240 {
		sample = sample[:240]
	}
	for _, rule := range sectionRules {
		if rule.pattern.MatchString(string(sample)) {
			return rule.typeName
		}
	}
	return "other"
}

func MakeFamilyKey(title string) (string, float64) {
	key := NormalizeText(title)
	for _, pattern := range familyPatterns {
		key = pattern.ReplaceAllString(key, " ")
	}
	key = strings.TrimSpace(spacePattern.ReplaceAllString(key, " "))
	beforeGeneric := key
	key = familyGenericSuffix.ReplaceAllString(key, "")
	key = strings.TrimSpace(key)
	if key == "" {
		key = NormalizeText(title)
	}
	if beforeGeneric == NormalizeText(title) {
		return key, 0.55
	}
	removed := utf8.RuneCountInString(NormalizeText(title)) - utf8.RuneCountInString(key)
	confidence := 0.62 + float64(max(0, removed))/float64(max(20, utf8.RuneCountInString(title)))
	if confidence < 0.45 {
		confidence = 0.45
	}
	if confidence > 0.95 {
		confidence = 0.95
	}
	return key, confidence
}

func BuildSearchTerms(values ...string) string {
	normalized := make([]string, len(values))
	for index, value := range values {
		normalized[index] = NormalizeText(value)
	}
	return buildSearchTerms(normalized)
}

func BuildBodySearchTerms(value string) string {
	return buildSearchTerms([]string{value})
}

func buildSearchTerms(values []string) string {
	pairs := make(map[uint64]struct{}, 256)
	asciiTokens := make(map[string]struct{}, 32)
	for _, value := range values {
		var ascii []rune
		flushASCII := func() {
			if len(ascii) > 1 {
				token := string(ascii)
				asciiTokens[token] = struct{}{}
				// The FTS tokenizer keeps identifier punctuation so quoted strong IDs
				// remain single-token queries with detail=column. Also index their
				// components once to retain partial filename/ID recall without the
				// repeated position lists produced by splitting every occurrence.
				for _, pathComponent := range strings.Split(token, "/") {
					if utf8.RuneCountInString(pathComponent) > 1 {
						asciiTokens[pathComponent] = struct{}{}
					}
				}
				for _, component := range strings.FieldsFunc(token, func(r rune) bool {
					return strings.ContainsRune("_./:-", r)
				}) {
					if utf8.RuneCountInString(component) > 1 {
						asciiTokens[component] = struct{}{}
					}
				}
			}
			ascii = ascii[:0]
		}
		var previous rune
		inCJKRun := false
		for _, r := range value {
			if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
				flushASCII()
				if inCJKRun {
					pairs[uint64(uint32(previous))<<32|uint64(uint32(r))] = struct{}{}
				}
				previous = r
				inCJKRun = true
				continue
			}
			inCJKRun = false
			if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_./:-", r) {
				ascii = append(ascii, unicode.ToLower(r))
			} else {
				flushASCII()
			}
		}
		flushASCII()
	}
	var output strings.Builder
	for pair := range pairs {
		if output.Len() > 0 {
			output.WriteByte(' ')
		}
		output.WriteRune(rune(uint32(pair >> 32)))
		output.WriteRune(rune(uint32(pair)))
	}
	for token := range asciiTokens {
		if output.Len() > 0 {
			output.WriteByte(' ')
		}
		output.WriteString(token)
	}
	return output.String()
}
