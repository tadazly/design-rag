package core

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	htmlnode "golang.org/x/net/html"
	"golang.org/x/text/encoding/simplifiedchinese"
	unicodeencoding "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

var (
	markdownHeading = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)
	htmlHeadingLike = regexp.MustCompile(`^(?:第[一二三四五六七八九十百\d]+[章节部分]|[一二三四五六七八九十]+[、.]|\d+(?:\.\d+){0,3}[、.\s]|【[^】]+】)|(?:概述|版本|修订|流程|玩法|规则|面板|逻辑|配置|配表|奖励|数值|统计|美术|原画|动画|需求)$`)
	htmlSentenceEnd = regexp.MustCompile(`[。！？!?；;]$`)
)

func stripTextControls(value string) string {
	return strings.Map(func(character rune) rune {
		if character == 0 || (character < 32 && character != '\n' && character != '\r' && character != '\t') {
			return -1
		}
		return character
	}, value)
}

func decodeText(data []byte) (string, string) {
	if len(data) >= 2 && ((data[0] == 0xff && data[1] == 0xfe) || (data[0] == 0xfe && data[1] == 0xff)) {
		endian := unicodeencoding.LittleEndian
		name := "utf16le"
		if data[0] == 0xfe {
			endian = unicodeencoding.BigEndian
			name = "utf16be"
		}
		decoded, err := io.ReadAll(transform.NewReader(bytes.NewReader(data), unicodeencoding.UTF16(endian, unicodeencoding.ExpectBOM).NewDecoder()))
		if err == nil && utf8.Valid(decoded) {
			return stripTextControls(string(decoded)), name
		}
	}
	data = bytesTrimBOM(data)
	if utf8.Valid(data) {
		return stripTextControls(string(data)), "utf8"
	}
	decoded, err := io.ReadAll(transform.NewReader(bytes.NewReader(data), simplifiedchinese.GB18030.NewDecoder()))
	if err == nil && utf8.Valid(decoded) {
		return stripTextControls(string(decoded)), "gb18030"
	}
	return stripTextControls(strings.ToValidUTF8(string(data), "")), "utf8-fallback"
}

func bytesTrimBOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xef && data[1] == 0xbb && data[2] == 0xbf {
		return data[3:]
	}
	return data
}

func extractLineBlocks(text string, markdown bool, locatorPrefix string) []Block {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	blocks := []Block{}
	headings := []string{}
	buffer := []string{}
	startLine := 1
	flush := func(endLine int) {
		content := strings.TrimSpace(strings.Join(buffer, "\n"))
		if content != "" {
			locator := fmt.Sprintf("%s %d", locatorPrefix, startLine)
			if endLine > startLine {
				locator = fmt.Sprintf("%s %d-%d", locatorPrefix, startLine, endLine)
			}
			blocks = append(blocks, Block{
				Ordinal:     len(blocks),
				Text:        content,
				HeadingPath: append([]string(nil), headings...),
				SectionType: ClassifySection(headings, content),
				Locator:     locator,
			})
		}
		buffer = nil
	}
	for index, line := range lines {
		lineNumber := index + 1
		if markdown {
			match := markdownHeading.FindStringSubmatch(line)
			if len(match) == 3 {
				flush(lineNumber - 1)
				level := len(match[1])
				if len(headings) >= level {
					headings = headings[:level-1]
				}
				for len(headings) < level-1 {
					headings = append(headings, "")
				}
				headings = append(headings, strings.TrimSpace(match[2]))
				startLine = lineNumber + 1
				continue
			}
		}
		if strings.TrimSpace(line) == "" {
			flush(lineNumber - 1)
			startLine = lineNumber + 1
			continue
		}
		if len(buffer) == 0 {
			startLine = lineNumber
		}
		buffer = append(buffer, line)
		if len(buffer) >= 80 {
			flush(lineNumber)
			startLine = lineNumber + 1
		}
	}
	flush(len(lines))
	return blocks
}

func extractCSV(filePath string) (ExtractedDocument, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return ExtractedDocument{}, err
	}
	text, encoding := decodeText(data)
	reader := csv.NewReader(strings.NewReader(text))
	reader.Comma = detectCSVDelimiter(text)
	reader.FieldsPerRecord = -1
	reader.ReuseRecord = false
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true
	rows := []sheetRow{}
	headers := map[int]string{}
	blocks := []Block{}
	dateCollector := newSheetDateCollector("CSV")
	rowNumber := 0
	flush := func() {
		if block, ok := sheetBlock("CSV", rows, headers, len(blocks)); ok {
			blocks = append(blocks, block)
		}
		rows = rows[:0]
	}
	for {
		record, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return ExtractedDocument{}, readErr
		}
		rowNumber++
		row := sheetRow{Number: rowNumber}
		for column, value := range record {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			row.Cells = append(row.Cells, sheetCell{Address: fmt.Sprintf("%s%d", encodeColumn(column), rowNumber), Column: column, Text: value})
		}
		if len(row.Cells) == 0 {
			continue
		}
		dateCollector.observe(row)
		if len(headers) == 0 && len(row.Cells) >= 2 {
			for _, cell := range row.Cells {
				headers[cell.Column] = cell.Text
			}
		}
		rows = append(rows, row)
		if len(rows) >= 24 {
			flush()
		}
	}
	flush()
	warnings := []string{}
	if encoding != "utf8" {
		warnings = append(warnings, "文本编码检测为 "+encoding)
	}
	if reader.Comma != ',' {
		warnings = append(warnings, fmt.Sprintf("CSV 分隔符检测为 %q", string(reader.Comma)))
	}
	return ExtractedDocument{Title: strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath)), Blocks: blocks, Warnings: warnings, BytesRead: int64(len(data)), DateEvidence: dateCollector.finish()}, nil
}

func detectCSVDelimiter(text string) rune {
	bestDelimiter, bestScore := ',', -1
	for _, delimiter := range []rune{',', '\t', ';'} {
		reader := csv.NewReader(strings.NewReader(text))
		reader.Comma = delimiter
		reader.FieldsPerRecord = -1
		reader.LazyQuotes = true
		counts := map[int]int{}
		rows, fields := 0, 0
		for rows < 20 {
			record, err := reader.Read()
			if err != nil {
				break
			}
			if len(record) > 0 {
				rows++
				counts[len(record)]++
				fields += len(record)
			}
		}
		consistent := 0
		for width, count := range counts {
			if width > 1 && count > consistent {
				consistent = count
			}
		}
		score := consistent*1000 + fields
		if score > bestScore {
			bestDelimiter, bestScore = delimiter, score
		}
	}
	return bestDelimiter
}

func normalizedHTMLText(node *htmlnode.Node) string {
	parts := []string{}
	var collect func(*htmlnode.Node)
	collect = func(current *htmlnode.Node) {
		if current.Type == htmlnode.ElementNode {
			tag := strings.ToLower(current.Data)
			if tag == "script" || tag == "style" || tag == "noscript" {
				return
			}
		}
		if current.Type == htmlnode.TextNode {
			if value := strings.TrimSpace(strings.Join(strings.Fields(current.Data), " ")); value != "" {
				parts = append(parts, value)
			}
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			collect(child)
		}
	}
	collect(node)
	return strings.Join(parts, " ")
}

func htmlHeadingLevel(tag, text string) (int, bool) {
	if len(tag) == 2 && tag[0] == 'h' && tag[1] >= '1' && tag[1] <= '6' {
		return int(tag[1] - '0'), true
	}
	if len([]rune(text)) >= 2 && len([]rune(text)) <= 80 && !htmlSentenceEnd.MatchString(text) && htmlHeadingLike.MatchString(text) {
		return min(6, max(1, strings.Count(text, ".")+1)), true
	}
	return 0, false
}

func htmlTableRows(table *htmlnode.Node) []string {
	rows := []string{}
	var visitRows func(*htmlnode.Node)
	visitRows = func(node *htmlnode.Node) {
		if node != table && node.Type == htmlnode.ElementNode && strings.EqualFold(node.Data, "table") {
			return
		}
		if node.Type == htmlnode.ElementNode && strings.EqualFold(node.Data, "tr") {
			cells := []string{}
			var visitCells func(*htmlnode.Node)
			visitCells = func(candidate *htmlnode.Node) {
				if candidate != node && candidate.Type == htmlnode.ElementNode && strings.EqualFold(candidate.Data, "tr") {
					return
				}
				if candidate.Type == htmlnode.ElementNode && (strings.EqualFold(candidate.Data, "th") || strings.EqualFold(candidate.Data, "td")) {
					cells = append(cells, normalizedHTMLText(candidate))
					return
				}
				for child := candidate.FirstChild; child != nil; child = child.NextSibling {
					visitCells(child)
				}
			}
			visitCells(node)
			hasContent := false
			for _, cell := range cells {
				hasContent = hasContent || cell != ""
			}
			if hasContent {
				rows = append(rows, strings.Join(cells, " | "))
			}
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visitRows(child)
		}
	}
	visitRows(table)
	return rows
}

func extractHTMLBlocks(text string) ([]Block, error) {
	document, err := htmlnode.Parse(strings.NewReader(text))
	if err != nil {
		return nil, err
	}
	blocks := []Block{}
	headings := []string{}
	paragraph, tableIndex := 0, 0
	var visit func(*htmlnode.Node)
	visit = func(node *htmlnode.Node) {
		if node.Type == htmlnode.ElementNode {
			tag := strings.ToLower(node.Data)
			if tag == "script" || tag == "style" || tag == "noscript" {
				return
			}
			if tag == "table" {
				tableIndex++
				rows := htmlTableRows(node)
				if len(rows) > 0 {
					content := strings.Join(rows, "\n")
					blocks = append(blocks, Block{Ordinal: len(blocks), Text: content, HeadingPath: append([]string(nil), headings...), SectionType: ClassifySection(headings, content), Locator: fmt.Sprintf("表格 %d 行 1-%d", tableIndex, len(rows))})
				}
				return
			}
			if tag == "p" || tag == "li" || (len(tag) == 2 && tag[0] == 'h' && tag[1] >= '1' && tag[1] <= '6') {
				content := normalizedHTMLText(node)
				if content == "" {
					return
				}
				paragraph++
				if level, heading := htmlHeadingLevel(tag, content); heading {
					if len(headings) >= level {
						headings = headings[:level-1]
					}
					for len(headings) < level-1 {
						headings = append(headings, "")
					}
					headings = append(headings, content)
					return
				}
				blocks = append(blocks, Block{Ordinal: len(blocks), Text: content, HeadingPath: append([]string(nil), headings...), SectionType: ClassifySection(headings, content), Locator: fmt.Sprintf("HTML 段落 %d", paragraph)})
				return
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(document)
	return blocks, nil
}

func extractText(filePath string) (ExtractedDocument, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return ExtractedDocument{}, err
	}
	text, encoding := decodeText(data)
	extension := strings.ToLower(filepath.Ext(filePath))
	var blocks []Block
	if extension == ".html" || extension == ".htm" {
		blocks, err = extractHTMLBlocks(text)
		if err != nil {
			return ExtractedDocument{}, err
		}
	} else {
		blocks = extractLineBlocks(text, extension == ".md" || extension == ".markdown", "行")
	}
	warnings := []string{}
	if encoding != "utf8" {
		warnings = append(warnings, "文本编码检测为 "+encoding)
	}
	return ExtractedDocument{
		Title:     strings.TrimSuffix(filepath.Base(filePath), extension),
		Blocks:    blocks,
		Warnings:  warnings,
		BytesRead: int64(len(data)),
	}, nil
}
