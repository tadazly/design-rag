package core

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var headingLikePattern = regexp.MustCompile(`^(第[一二三四五六七八九十百0-9]+[章节部分]|[一二三四五六七八九十]+[、.]|[0-9]+([.][0-9]+){0,3}[、.[:space:]]|【[^】]+】)`)

type docxParagraph struct {
	text         string
	style        string
	outlineLevel int
}

func attrLocal(start xml.StartElement, local string) string {
	for _, attribute := range start.Attr {
		if strings.EqualFold(attribute.Name.Local, local) {
			return attribute.Value
		}
	}
	return ""
}

func readDocxParagraph(decoder *xml.Decoder, start xml.StartElement) (docxParagraph, error) {
	paragraph := docxParagraph{outlineLevel: -1}
	var text strings.Builder
	depth := 1
	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			return paragraph, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			depth++
			switch strings.ToLower(value.Name.Local) {
			case "pstyle":
				paragraph.style = attrLocal(value, "val")
			case "outlinelvl":
				if parsed, parseErr := strconv.Atoi(attrLocal(value, "val")); parseErr == nil {
					paragraph.outlineLevel = parsed
				}
			case "t":
				var content string
				if decodeErr := decoder.DecodeElement(&content, &value); decodeErr != nil {
					return paragraph, decodeErr
				}
				depth--
				text.WriteString(content)
			case "tab":
				text.WriteByte('\t')
			case "br", "cr":
				text.WriteByte('\n')
			}
		case xml.EndElement:
			depth--
		}
	}
	paragraph.text = strings.TrimSpace(text.String())
	return paragraph, nil
}

func readDocxTable(decoder *xml.Decoder) ([][]string, error) {
	rows := make([][]string, 0, 16)
	var row []string
	var cell strings.Builder
	depth := 1
	inCell := false
	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			depth++
			switch strings.ToLower(value.Name.Local) {
			case "tr":
				row = nil
			case "tc":
				inCell = true
				cell.Reset()
			case "t":
				if inCell {
					var content string
					if decodeErr := decoder.DecodeElement(&content, &value); decodeErr != nil {
						return nil, decodeErr
					}
					depth--
					cell.WriteString(content)
				}
			case "tab":
				if inCell {
					cell.WriteByte('\t')
				}
			case "br", "cr", "p":
				if inCell && cell.Len() > 0 {
					cell.WriteByte(' ')
				}
			}
		case xml.EndElement:
			switch strings.ToLower(value.Name.Local) {
			case "tc":
				content := strings.TrimSpace(spacePattern.ReplaceAllString(cell.String(), " "))
				row = append(row, content)
				inCell = false
			case "tr":
				nonEmpty := false
				for _, value := range row {
					nonEmpty = nonEmpty || value != ""
				}
				if nonEmpty {
					rows = append(rows, append([]string(nil), row...))
				}
			}
			depth--
		}
	}
	return rows, nil
}

func docxHeadingLevel(paragraph docxParagraph) int {
	if paragraph.outlineLevel >= 0 && paragraph.outlineLevel <= 8 {
		return paragraph.outlineLevel + 1
	}
	style := strings.ToLower(strings.ReplaceAll(paragraph.style, " ", ""))
	if strings.Contains(style, "heading") || strings.Contains(style, "标题") {
		for _, r := range style {
			if r >= '1' && r <= '9' {
				return int(r - '0')
			}
		}
		return 1
	}
	text := strings.TrimSpace(paragraph.text)
	endsWithSentence := false
	for _, suffix := range []string{"。", "！", "？", "!", "?", "；", ";"} {
		endsWithSentence = endsWithSentence || strings.HasSuffix(text, suffix)
	}
	if utf8.RuneCountInString(text) >= 2 && utf8.RuneCountInString(text) <= 80 && !endsWithSentence && headingLikePattern.MatchString(text) {
		return min(6, strings.Count(text, ".")+1)
	}
	return 0
}

type paragraphAccumulator struct {
	headings []string
	texts    []string
	start    int
	end      int
	runes    int
}

func (accumulator *paragraphAccumulator) flush(blocks *[]Block) {
	if len(accumulator.texts) == 0 {
		return
	}
	text := strings.Join(accumulator.texts, "\n")
	locator := fmt.Sprintf("段落 %d", accumulator.start)
	if accumulator.end > accumulator.start {
		locator = fmt.Sprintf("段落 %d-%d", accumulator.start, accumulator.end)
	}
	*blocks = append(*blocks, Block{
		Ordinal:     len(*blocks),
		Text:        text,
		HeadingPath: append([]string(nil), accumulator.headings...),
		SectionType: ClassifySection(accumulator.headings, text),
		Locator:     locator,
	})
	accumulator.texts = nil
	accumulator.runes = 0
}

func extractDocx(filePath string) (ExtractedDocument, error) {
	archive, err := zip.OpenReader(filePath)
	if err != nil {
		return ExtractedDocument{}, err
	}
	defer archive.Close()
	documentEntry := zipFile(archive, "word/document.xml")
	if documentEntry == nil {
		return ExtractedDocument{}, fmt.Errorf("DOCX 缺少 word/document.xml")
	}
	stream, err := documentEntry.Open()
	if err != nil {
		return ExtractedDocument{}, err
	}
	defer stream.Close()
	decoder := xml.NewDecoder(io.LimitReader(stream, maxExpandedEntryBytes))
	blocks := make([]Block, 0, 256)
	headings := make([]string, 0, 6)
	paragraphNumber := 0
	tableNumber := 0
	accumulator := paragraphAccumulator{}
	for {
		token, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			return ExtractedDocument{}, tokenErr
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch strings.ToLower(start.Name.Local) {
		case "p":
			paragraph, parseErr := readDocxParagraph(decoder, start)
			if parseErr != nil {
				return ExtractedDocument{}, parseErr
			}
			paragraphNumber++
			if paragraph.text == "" {
				continue
			}
			if level := docxHeadingLevel(paragraph); level > 0 {
				accumulator.flush(&blocks)
				if len(headings) >= level {
					headings = headings[:level-1]
				}
				for len(headings) < level-1 {
					headings = append(headings, "")
				}
				headings = append(headings, paragraph.text)
				continue
			}
			if len(accumulator.texts) > 0 && (!equalStrings(accumulator.headings, headings) || accumulator.runes+utf8.RuneCountInString(paragraph.text) > 7200) {
				accumulator.flush(&blocks)
			}
			if len(accumulator.texts) == 0 {
				accumulator.headings = append([]string(nil), headings...)
				accumulator.start = paragraphNumber
			}
			accumulator.end = paragraphNumber
			accumulator.texts = append(accumulator.texts, paragraph.text)
			accumulator.runes += utf8.RuneCountInString(paragraph.text)
		case "tbl":
			accumulator.flush(&blocks)
			rows, tableErr := readDocxTable(decoder)
			if tableErr != nil {
				return ExtractedDocument{}, tableErr
			}
			tableNumber++
			if len(rows) > 0 {
				lines := make([]string, 0, len(rows))
				for _, row := range rows {
					lines = append(lines, strings.Join(row, " | "))
				}
				text := strings.Join(lines, "\n")
				blocks = append(blocks, Block{
					Ordinal:     len(blocks),
					Text:        text,
					HeadingPath: append([]string(nil), headings...),
					SectionType: ClassifySection(headings, text),
					Locator:     fmt.Sprintf("表格 %d 行 1-%d", tableNumber, len(rows)),
				})
			}
		}
	}
	accumulator.flush(&blocks)
	warnings := []string{}
	if len(blocks) == 0 {
		warnings = append(warnings, "DOCX 未提取到可索引文本")
	}
	var embeddedModifiedAt *time.Time
	if shouldReadEmbeddedModified(filePath) {
		embeddedModifiedAt = zipCoreModified(archive)
	}
	return ExtractedDocument{
		Title:              strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath)),
		Blocks:             blocks,
		EmbeddedModifiedAt: embeddedModifiedAt,
		Warnings:           warnings,
		NeedsOCR:           len(blocks) == 0,
		BytesRead:          int64(documentEntry.CompressedSize64),
		DateEvidence:       collectBlockDateEvidence(blocks),
	}, nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
