package core

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var spreadsheetLocatorPattern = regexp.MustCompile(`^(.*)!([A-Z]+[0-9]+):([A-Z]+[0-9]+)$`)
var spreadsheetRowPattern = regexp.MustCompile(`^行\s+([0-9]+)\s*\|`)

const (
	chunkTargetRunes  = 16000
	chunkOverlapRunes = 320
)

type chunkPart struct {
	text    string
	locator string
}

func splitSpreadsheetBlock(text, locator string, target int) []chunkPart {
	locatorMatch := spreadsheetLocatorPattern.FindStringSubmatch(locator)
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(locatorMatch) != 4 || len(lines) < 2 || !strings.HasPrefix(strings.TrimSpace(lines[0]), "字段 |") {
		return nil
	}
	header := strings.TrimSpace(lines[0])
	rowLines := make([]string, 0, len(lines)-1)
	rowNumbers := make([]int, 0, len(lines)-1)
	for _, rawLine := range lines[1:] {
		line := strings.TrimSpace(rawLine)
		match := spreadsheetRowPattern.FindStringSubmatch(line)
		if len(match) != 2 {
			continue
		}
		rowNumber, err := strconv.Atoi(match[1])
		if err != nil || rowNumber <= 0 {
			continue
		}
		rowLines = append(rowLines, line)
		rowNumbers = append(rowNumbers, rowNumber)
	}
	if len(rowLines) == 0 {
		return nil
	}
	startCell := locatorMatch[2]
	endCell := locatorMatch[3]
	startColumn := strings.TrimRight(startCell, "0123456789")
	endColumn := strings.TrimRight(endCell, "0123456789")
	parts := []chunkPart{}
	for start := 0; start < len(rowLines); {
		end := start
		runes := utf8.RuneCountInString(header)
		for end < len(rowLines) {
			next := utf8.RuneCountInString(rowLines[end]) + 1
			if end > start && runes+next > target {
				break
			}
			runes += next
			end++
		}
		if end == start {
			end++
		}
		partLines := append([]string{header}, rowLines[start:end]...)
		parts = append(parts, chunkPart{
			text:    strings.Join(partLines, "\n"),
			locator: fmt.Sprintf("%s!%s%d:%s%d", locatorMatch[1], startColumn, rowNumbers[start], endColumn, rowNumbers[end-1]),
		})
		start = end
	}
	return parts
}

func splitLargeText(text string, target, overlap int) []string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= target {
		if len(runes) == 0 {
			return nil
		}
		return []string{string(runes)}
	}
	parts := make([]string, 0, len(runes)/target+1)
	start := 0
	for start < len(runes) {
		end := min(len(runes), start+target)
		if end < len(runes) {
			lower := max(start+target/2, start)
			for i := end; i > lower; i-- {
				if strings.ContainsRune("。！？!?；;\n", runes[i-1]) {
					end = i
					break
				}
			}
		}
		piece := strings.TrimSpace(string(runes[start:end]))
		if piece != "" {
			parts = append(parts, piece)
		}
		if end >= len(runes) {
			break
		}
		start = max(start+1, end-overlap)
	}
	return parts
}

func ChunkBlocks(blocks []Block) []Chunk {
	chunks := make([]Chunk, 0, len(blocks))
	for _, block := range blocks {
		clean := strings.TrimSpace(strings.ReplaceAll(block.Text, "\r\n", "\n"))
		if clean == "" {
			continue
		}
		section := block.SectionType
		if section == "" {
			section = ClassifySection(block.HeadingPath, clean)
		}
		parts := splitSpreadsheetBlock(clean, block.Locator, chunkTargetRunes)
		if len(parts) == 0 {
			for _, text := range splitLargeText(clean, chunkTargetRunes, chunkOverlapRunes) {
				parts = append(parts, chunkPart{text: text, locator: block.Locator})
			}
		}
		for _, part := range parts {
			if NormalizeText(part.text) == "" || utf8.RuneCountInString(part.text) == 0 {
				continue
			}
			chunks = append(chunks, Chunk{
				Ordinal:     len(chunks),
				Text:        part.text,
				HeadingPath: append([]string(nil), block.HeadingPath...),
				SectionType: section,
				Locator:     part.locator,
				ContentHash: HashString(part.text),
				SearchTerms: BuildBodySearchTerms(part.text),
			})
		}
	}
	return chunks
}

func CoalesceFallbackChunks(chunks []Chunk) []Chunk {
	if len(chunks) < 2 {
		return chunks
	}
	blocks := make([]Block, 0, len(chunks))
	var current *Block
	for _, chunk := range chunks {
		currentLocator := ""
		if current != nil {
			currentLocator = current.Locator
		}
		mergedLocator, locatorCompatible := mergeSpreadsheetLocators(currentLocator, chunk.Locator)
		canMerge := current != nil &&
			current.SectionType == chunk.SectionType &&
			equalStrings(current.HeadingPath, chunk.HeadingPath) &&
			(current.Locator == chunk.Locator || locatorCompatible) &&
			len([]rune(current.Text))+len([]rune(chunk.Text)) <= 32_000
		if !canMerge {
			blocks = append(blocks, Block{
				Ordinal:     len(blocks),
				Text:        chunk.Text,
				HeadingPath: append([]string(nil), chunk.HeadingPath...),
				SectionType: chunk.SectionType,
				Locator:     chunk.Locator,
			})
			current = &blocks[len(blocks)-1]
			continue
		}
		if locatorCompatible {
			current.Locator = mergedLocator
		}
		left := []rune(current.Text)
		right := []rune(chunk.Text)
		overlap := 0
		for size := min(256, min(len(left), len(right))); size > 0; size-- {
			if string(left[len(left)-size:]) == string(right[:size]) {
				overlap = size
				break
			}
		}
		current.Text += string(right[overlap:])
	}
	return ChunkBlocks(blocks)
}

func mergeSpreadsheetLocators(left, right string) (string, bool) {
	leftMatch := spreadsheetLocatorPattern.FindStringSubmatch(left)
	rightMatch := spreadsheetLocatorPattern.FindStringSubmatch(right)
	if len(leftMatch) != 4 || len(rightMatch) != 4 || leftMatch[1] != rightMatch[1] {
		return "", false
	}
	return leftMatch[1] + "!" + leftMatch[2] + ":" + rightMatch[3], true
}
