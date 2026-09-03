package core

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

const CitationPrefix = "DRAG:"

type ExcerptSlice struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type SpreadsheetCitationScope struct {
	Version     int
	Locator     string
	Columns     []string
	HeaderSlice *ExcerptSlice
	RowSlice    *ExcerptSlice
}

type ExcerptProjection struct {
	Text      string
	Locator   string
	Scope     *SpreadsheetCitationScope
	TextSlice *ExcerptSlice
}

type compactSpreadsheetCitationScopeV2 struct {
	RowStartDelta int
	RowSpan       int
	ColumnDeltas  []int
	HeaderSlice   *ExcerptSlice
	RowSlice      *ExcerptSlice
}

type citationReference struct {
	ChunkID     string
	ScopeV1     *SpreadsheetCitationScope
	PayloadV1   string
	ScopeV2     *compactSpreadsheetCitationScopeV2
	PayloadV2   []byte
	TextSlice   *ExcerptSlice
	PayloadText []byte
	Digest      string
}

var (
	searchSpreadsheetLocator = regexp.MustCompile(`^(.*)!([A-Z]+)(\d+):([A-Z]+)(\d+)$`)
	searchSpreadsheetRowLine = regexp.MustCompile(`^行\s+(\d+)\s*\|\s*(.*)$`)
	spreadsheetSegment       = regexp.MustCompile(`^([A-Z]+)(?:\d+)?(?:\[[^\]]*\])?=`)
	compactChunkID           = regexp.MustCompile(`(?i)^chunk_([0-9a-f]{16})_([0-9a-f]{8})_(\d+)$`)
)

func utf16Units(value string) []uint16 { return utf16.Encode([]rune(value)) }

func utf16Length(value string) int { return len(utf16Units(value)) }

func utf16Slice(value string, start, end int) (string, error) {
	units := utf16Units(value)
	if start < 0 || end < start || end > len(units) {
		return "", fmt.Errorf("引用范围无效或已损坏：文本切片越界")
	}
	// Keep slices valid Unicode even when a UTF-16 budget lands inside a
	// surrogate pair. Both generation and replay apply the same inward clamp.
	if start > 0 && start < len(units) && units[start] >= 0xDC00 && units[start] <= 0xDFFF && units[start-1] >= 0xD800 && units[start-1] <= 0xDBFF {
		start++
	}
	if end > start && end < len(units) && units[end-1] >= 0xD800 && units[end-1] <= 0xDBFF && units[end] >= 0xDC00 && units[end] <= 0xDFFF {
		end--
	}
	if end < start {
		return "", nil
	}
	return string(utf16.Decode(units[start:end])), nil
}

func utf16IndexFold(value, term string) int {
	haystack := utf16Units(strings.ToLower(value))
	needle := utf16Units(strings.ToLower(term))
	if len(needle) == 0 || len(needle) > len(haystack) {
		return -1
	}
	for index := 0; index+len(needle) <= len(haystack); index++ {
		matched := true
		for offset := range needle {
			if haystack[index+offset] != needle[offset] {
				matched = false
				break
			}
		}
		if matched {
			return index
		}
	}
	return -1
}

func columnNumber(value string) int {
	result := 0
	for _, character := range value {
		result = result*26 + int(character-'A') + 1
	}
	return result
}

func columnName(value int) (string, error) {
	if value < 1 {
		return "", fmt.Errorf("引用范围无效或已损坏：列编号非法")
	}
	result := ""
	for value > 0 {
		value--
		result = string(rune('A'+value%26)) + result
		value /= 26
	}
	return result, nil
}

func spreadsheetSegmentColumn(value string) string {
	match := spreadsheetSegment.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) == 0 {
		return ""
	}
	return match[1]
}

func renderExcerptSlice(value string, slice ExcerptSlice) (string, error) {
	length := utf16Length(value)
	part, err := utf16Slice(value, slice.Start, slice.End)
	if err != nil {
		return "", err
	}
	prefix := ""
	suffix := ""
	if slice.Start > 0 {
		prefix = "…"
	}
	if slice.End < length {
		suffix = "…"
	}
	return prefix + strings.TrimSpace(part) + suffix, nil
}

func excerptAroundTermsWithSlice(value string, terms []string, maxLength int) (string, ExcerptSlice, error) {
	limit := max(1, maxLength)
	length := utf16Length(value)
	if length <= limit {
		return value, ExcerptSlice{Start: 0, End: length}, nil
	}
	position := -1
	for _, term := range terms {
		if found := utf16IndexFold(value, term); found >= 0 {
			position = found
			break
		}
	}
	if position < 0 {
		slice := ExcerptSlice{Start: 0, End: min(length, max(0, limit-1))}
		text, err := renderExcerptSlice(value, slice)
		return text, slice, err
	}
	bodyBudget := max(1, limit-2)
	start := max(0, min(length-bodyBudget, position-int(float64(bodyBudget)*0.3)))
	suffixBudget := 0
	if start+bodyBudget < length {
		suffixBudget = 1
	}
	available := max(1, limit-boolInt(start > 0)-suffixBudget)
	end := min(length, start+available)
	slice := ExcerptSlice{Start: start, End: end}
	text, err := renderExcerptSlice(value, slice)
	if err != nil {
		return "", slice, err
	}
	if utf16Length(text) > limit {
		text, err = utf16Slice(text, 0, limit)
	}
	return text, slice, err
}

func excerptAroundTerms(value string, terms []string, maxLength int) string {
	text, _, err := excerptAroundTermsWithSlice(value, terms, maxLength)
	if err != nil {
		return value
	}
	return text
}

func genericExcerptProjection(text, locator string, terms []string, maxLength int) (ExcerptProjection, error) {
	projected, slice, err := excerptAroundTermsWithSlice(text, uniqueNormalizedTerms(terms), maxLength)
	if err != nil {
		return ExcerptProjection{}, err
	}
	projection := ExcerptProjection{Text: projected, Locator: locator}
	if slice.Start > 0 || slice.End < utf16Length(text) {
		projection.TextSlice = &slice
	}
	return projection, nil
}

type parsedSpreadsheetRow struct {
	number int
	cells  string
	line   string
}

func MakeExcerpt(text, locator string, terms []string, maxLength int) (ExcerptProjection, error) {
	if maxLength <= 0 {
		maxLength = 520
	}
	locatorMatch := searchSpreadsheetLocator.FindStringSubmatch(locator)
	lines := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	header := ""
	rows := []parsedSpreadsheetRow{}
	for _, line := range lines {
		if header == "" && strings.HasPrefix(line, "字段 |") {
			header = line
		}
		if match := searchSpreadsheetRowLine.FindStringSubmatch(line); len(match) > 0 {
			number, _ := strconv.Atoi(match[1])
			rows = append(rows, parsedSpreadsheetRow{number: number, cells: match[2], line: line})
		}
	}
	if len(locatorMatch) == 6 && header != "" && len(rows) > 0 {
		baseStartColumn := columnNumber(locatorMatch[2])
		baseEndColumn := columnNumber(locatorMatch[4])
		baseStartRow, _ := strconv.Atoi(locatorMatch[3])
		baseEndRow, _ := strconv.Atoi(locatorMatch[5])
		filteredRows := []parsedSpreadsheetRow{}
		for _, row := range rows {
			if row.number >= baseStartRow && row.number <= baseEndRow {
				filteredRows = append(filteredRows, row)
			}
		}
		if len(filteredRows) == 0 {
			return genericExcerptProjection(text, locator, terms, maxLength)
		}
		normalizedTerms := uniqueNormalizedTerms(terms)
		center := -1
		for _, term := range normalizedTerms {
			for index, row := range filteredRows {
				if strings.Contains(NormalizeText(row.line), term) {
					center = index
					break
				}
			}
			if center >= 0 {
				break
			}
		}
		if center < 0 {
			center = 0
		}
		selectedRows := append([]parsedSpreadsheetRow(nil), filteredRows[max(0, center-2):min(len(filteredRows), center+3)]...)
		headerSegments := splitPipe(strings.TrimPrefix(header, "字段 |"))
		rowSegments := make([][]string, len(selectedRows))
		for index, row := range selectedRows {
			rowSegments[index] = splitPipe(row.cells)
		}
		headerByColumn := map[string]string{}
		relevant := map[string]bool{}
		for _, segment := range headerSegments {
			column := spreadsheetSegmentColumn(segment)
			if column != "" {
				headerByColumn[column] = segment
			}
			for _, term := range normalizedTerms {
				if strings.Contains(NormalizeText(segment), term) && column != "" {
					relevant[column] = true
				}
			}
		}
		for _, segments := range rowSegments {
			for _, segment := range segments {
				column := spreadsheetSegmentColumn(segment)
				for _, term := range normalizedTerms {
					if strings.Contains(NormalizeText(segment), term) && column != "" {
						relevant[column] = true
					}
				}
			}
		}
		ordered := []string{}
		seen := map[string]bool{}
		for _, segment := range append(append([]string{}, headerSegments...), flattenStrings(rowSegments)...) {
			column := spreadsheetSegmentColumn(segment)
			value := columnNumber(column)
			if column != "" && value >= baseStartColumn && value <= baseEndColumn && !seen[column] {
				seen[column] = true
				ordered = append(ordered, column)
			}
		}
		sort.Slice(ordered, func(i, j int) bool { return columnNumber(ordered[i]) < columnNumber(ordered[j]) })
		if len(ordered) == 0 {
			return genericExcerptProjection(text, locator, normalizedTerms, maxLength)
		}
		for _, column := range ordered[:min(2, len(ordered))] {
			relevant[column] = true
		}
		relevantSnapshot := make([]string, 0, len(relevant))
		for column := range relevant {
			relevantSnapshot = append(relevantSnapshot, column)
		}
		for _, column := range relevantSnapshot {
			position := indexOfString(ordered, column)
			if position > 0 {
				relevant[ordered[position-1]] = true
			}
			if position >= 0 && position+1 < len(ordered) {
				relevant[ordered[position+1]] = true
			}
		}
		selectedColumns := []string{}
		for _, column := range ordered {
			if len(relevant) == 0 || relevant[column] {
				selectedColumns = append(selectedColumns, column)
			}
			if len(selectedColumns) >= 12 {
				break
			}
		}
		columnSet := map[string]bool{}
		for _, column := range selectedColumns {
			columnSet[column] = true
		}
		projectedHeader := make([]string, len(selectedColumns))
		for index, column := range selectedColumns {
			projectedHeader[index] = headerByColumn[column]
			if projectedHeader[index] == "" {
				projectedHeader[index] = column + "=未命名字段"
			}
		}
		headerLine := "字段映射（投影） | " + strings.Join(projectedHeader, " | ")
		projectedRows := make([]string, len(selectedRows))
		for index, row := range selectedRows {
			projectedRows[index] = fmt.Sprintf("行 %d | %s", row.number, strings.Join(filterSpreadsheetSegments(rowSegments[index], columnSet), " | "))
		}
		projectedText := strings.Join(append([]string{headerLine}, projectedRows...), "\n")
		var headerSlice, rowSlice *ExcerptSlice
		if utf16Length(projectedText) > maxLength {
			selectedRows = []parsedSpreadsheetRow{filteredRows[center]}
			centerSegments := splitPipe(selectedRows[0].cells)
			centerLine := fmt.Sprintf("行 %d | %s", selectedRows[0].number, strings.Join(filterSpreadsheetSegments(centerSegments, columnSet), " | "))
			headerBudget := max(1, min(maxLength*35/100, maxLength-2))
			clippedHeader, headerRange, err := excerptAroundTermsWithSlice(headerLine, normalizedTerms, headerBudget)
			if err != nil {
				return ExcerptProjection{}, err
			}
			rowBudget := max(1, maxLength-utf16Length(clippedHeader)-1)
			clippedRow, rowRange, err := excerptAroundTermsWithSlice(centerLine, normalizedTerms, rowBudget)
			if err != nil {
				return ExcerptProjection{}, err
			}
			headerSlice, rowSlice = &headerRange, &rowRange
			projectedText = clippedHeader + "\n" + clippedRow
			if utf16Length(projectedText) > maxLength {
				projectedText, err = utf16Slice(projectedText, 0, maxLength)
				if err != nil {
					return ExcerptProjection{}, err
				}
			}
		}
		firstRow := selectedRows[0].number
		lastRow := selectedRows[0].number
		for _, row := range selectedRows[1:] {
			firstRow = min(firstRow, row.number)
			lastRow = max(lastRow, row.number)
		}
		projectedLocator := fmt.Sprintf("%s!%s%d:%s%d", locatorMatch[1], selectedColumns[0], firstRow, selectedColumns[len(selectedColumns)-1], lastRow)
		return ExcerptProjection{
			Text: projectedText, Locator: projectedLocator,
			Scope: &SpreadsheetCitationScope{Version: 1, Locator: projectedLocator, Columns: selectedColumns, HeaderSlice: headerSlice, RowSlice: rowSlice},
		}, nil
	}
	return genericExcerptProjection(text, locator, terms, maxLength)
}

func splitPipe(value string) []string {
	result := []string{}
	for _, segment := range strings.Split(value, "|") {
		if segment = strings.TrimSpace(segment); segment != "" {
			result = append(result, segment)
		}
	}
	return result
}

func flattenStrings(values [][]string) []string {
	result := []string{}
	for _, value := range values {
		result = append(result, value...)
	}
	return result
}

func indexOfString(values []string, wanted string) int {
	for index, value := range values {
		if value == wanted {
			return index
		}
	}
	return -1
}

func filterSpreadsheetSegments(segments []string, columns map[string]bool) []string {
	result := []string{}
	for _, segment := range segments {
		column := spreadsheetSegmentColumn(segment)
		if column == "" || columns[column] {
			result = append(result, segment)
		}
	}
	return result
}

func MakeSourceLink(absolutePath, locator string) CitationSourceLink {
	fileName := filepath.Base(absolutePath)
	escapedLabel := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(fileName, `\`, `\\`), "[", `\[`), "]", `\]`)
	linkTarget := strings.ReplaceAll(strings.ReplaceAll(absolutePath, `\`, "/"), ">", "%3E")
	escapedLocator := strings.ReplaceAll(locator, "`", "'")
	return CitationSourceLink{FileName: fileName, Label: fileName + " · " + locator, AbsolutePath: absolutePath, Locator: locator, Markdown: fmt.Sprintf("[%s](<%s>) · `%s`", escapedLabel, linkTarget, escapedLocator)}
}

func renderSpreadsheetCitationScope(text, baseLocator string, scope SpreadsheetCitationScope) (string, error) {
	base := searchSpreadsheetLocator.FindStringSubmatch(baseLocator)
	projected := searchSpreadsheetLocator.FindStringSubmatch(scope.Locator)
	if len(base) != 6 || len(projected) != 6 || base[1] != projected[1] {
		return "", fmt.Errorf("引用范围无效或已损坏：sheet 不匹配")
	}
	baseStartColumn, baseEndColumn := columnNumber(base[2]), columnNumber(base[4])
	projectedStartColumn, projectedEndColumn := columnNumber(projected[2]), columnNumber(projected[4])
	baseStartRow, _ := strconv.Atoi(base[3])
	baseEndRow, _ := strconv.Atoi(base[5])
	projectedStartRow, _ := strconv.Atoi(projected[3])
	projectedEndRow, _ := strconv.Atoi(projected[5])
	if projectedStartColumn < baseStartColumn || projectedEndColumn > baseEndColumn || projectedStartColumn > projectedEndColumn || projectedStartRow < baseStartRow || projectedEndRow > baseEndRow || projectedStartRow > projectedEndRow || len(scope.Columns) == 0 || len(scope.Columns) > 12 {
		return "", fmt.Errorf("引用范围无效或已损坏：投影超出索引 chunk")
	}
	seen := map[string]bool{}
	for index, column := range scope.Columns {
		value := columnNumber(column)
		if seen[column] || !regexp.MustCompile(`^[A-Z]+$`).MatchString(column) || value < projectedStartColumn || value > projectedEndColumn || (index > 0 && value <= columnNumber(scope.Columns[index-1])) {
			return "", fmt.Errorf("引用范围无效或已损坏：投影列非法")
		}
		seen[column] = true
	}
	if scope.Columns[0] != projected[2] || scope.Columns[len(scope.Columns)-1] != projected[4] {
		return "", fmt.Errorf("引用范围无效或已损坏：投影列非法")
	}
	if (scope.HeaderSlice == nil) != (scope.RowSlice == nil) {
		return "", fmt.Errorf("引用范围无效或已损坏：文本切片不完整")
	}
	lines := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	header := ""
	rows := []parsedSpreadsheetRow{}
	for _, line := range lines {
		if header == "" && strings.HasPrefix(line, "字段 |") {
			header = line
		}
		if match := searchSpreadsheetRowLine.FindStringSubmatch(line); len(match) > 0 {
			number, _ := strconv.Atoi(match[1])
			if number >= projectedStartRow && number <= projectedEndRow {
				rows = append(rows, parsedSpreadsheetRow{number: number, cells: match[2]})
			}
		}
	}
	if header == "" || len(rows) == 0 {
		return "", fmt.Errorf("引用范围无效或已损坏：投影原文不存在")
	}
	headerByColumn := map[string]string{}
	for _, segment := range splitPipe(strings.TrimPrefix(header, "字段 |")) {
		if column := spreadsheetSegmentColumn(segment); column != "" {
			headerByColumn[column] = segment
		}
	}
	columnSet := map[string]bool{}
	projectedHeader := make([]string, len(scope.Columns))
	for index, column := range scope.Columns {
		columnSet[column] = true
		projectedHeader[index] = headerByColumn[column]
		if projectedHeader[index] == "" {
			projectedHeader[index] = column + "=未命名字段"
		}
	}
	headerLine := "字段映射（投影） | " + strings.Join(projectedHeader, " | ")
	rowLines := make([]string, len(rows))
	for index, row := range rows {
		rowLines[index] = fmt.Sprintf("行 %d | %s", row.number, strings.Join(filterSpreadsheetSegments(splitPipe(row.cells), columnSet), " | "))
	}
	if scope.HeaderSlice != nil && scope.RowSlice != nil {
		if len(rowLines) != 1 {
			return "", fmt.Errorf("引用范围无效或已损坏：切片必须定位单行")
		}
		headerText, err := renderExcerptSlice(headerLine, *scope.HeaderSlice)
		if err != nil {
			return "", err
		}
		rowText, err := renderExcerptSlice(rowLines[0], *scope.RowSlice)
		if err != nil {
			return "", err
		}
		return headerText + "\n" + rowText, nil
	}
	return strings.Join(append([]string{headerLine}, rowLines...), "\n"), nil
}

type citationScopeV1Wire struct {
	Version int      `json:"v"`
	Locator string   `json:"l"`
	Columns []string `json:"c"`
	Header  []int    `json:"h,omitempty"`
	Row     []int    `json:"r,omitempty"`
}

func serializeSpreadsheetCitationScopeV1(scope SpreadsheetCitationScope) (string, error) {
	wire := citationScopeV1Wire{Version: scope.Version, Locator: scope.Locator, Columns: scope.Columns}
	if scope.HeaderSlice != nil {
		wire.Header = []int{scope.HeaderSlice.Start, scope.HeaderSlice.End}
	}
	if scope.RowSlice != nil {
		wire.Row = []int{scope.RowSlice.Start, scope.RowSlice.End}
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func citationDigest(prefix, chunkID, contentHash string, payload []byte) string {
	hash := sha256.New()
	hash.Write([]byte(prefix))
	if chunkID != "" {
		hash.Write([]byte(chunkID))
		hash.Write([]byte{0})
	}
	hash.Write([]byte(contentHash))
	hash.Write([]byte{0})
	hash.Write(payload)
	return base64.RawURLEncoding.EncodeToString(hash.Sum(nil))[:22]
}

func scopedCitationIDV1(row LexicalCandidateRow, scope SpreadsheetCitationScope) (string, error) {
	payload, err := serializeSpreadsheetCitationScopeV1(scope)
	if err != nil {
		return "", err
	}
	digest := citationDigest("drag-scoped-citation-v1\x00", row.ChunkID, row.ContentHash, []byte(payload))
	return CitationPrefix + row.ChunkID + "~" + payload + "." + digest, nil
}

func appendUnsignedVarint(target []byte, value int) ([]byte, error) {
	if value < 0 || value > 2_147_483_647 {
		return nil, fmt.Errorf("引用范围无效或已损坏：短引用整数越界")
	}
	remaining := value
	for {
		valueByte := byte(remaining % 128)
		remaining /= 128
		if remaining > 0 {
			valueByte |= 0x80
		}
		target = append(target, valueByte)
		if remaining == 0 {
			return target, nil
		}
	}
}

func readUnsignedVarint(payload []byte, offset *int) (int, error) {
	result, multiplier := 0, 1
	for index := 0; index < 5; index++ {
		if *offset >= len(payload) {
			return 0, fmt.Errorf("引用范围无效或已损坏：短引用被截断")
		}
		value := payload[*offset]
		*offset++
		result += int(value&0x7f) * multiplier
		if value&0x80 == 0 {
			if result > 2_147_483_647 {
				return 0, fmt.Errorf("引用范围无效或已损坏：短引用整数越界")
			}
			return result, nil
		}
		multiplier *= 128
	}
	return 0, fmt.Errorf("引用范围无效或已损坏：短引用整数过长")
}

func encodeSpreadsheetCitationScopeV2(row LexicalCandidateRow, scope SpreadsheetCitationScope) ([]byte, error) {
	chunk := compactChunkID.FindStringSubmatch(row.ChunkID)
	base := searchSpreadsheetLocator.FindStringSubmatch(row.Locator)
	projected := searchSpreadsheetLocator.FindStringSubmatch(scope.Locator)
	if len(chunk) != 4 || len(base) != 6 || len(projected) != 6 || base[1] != projected[1] {
		return nil, nil
	}
	seen := map[string]bool{}
	columns := make([]int, len(scope.Columns))
	for index, column := range scope.Columns {
		if seen[column] {
			return nil, fmt.Errorf("引用范围无效或已损坏：生成器拒绝重复投影列")
		}
		seen[column] = true
		columns[index] = columnNumber(column)
		if index > 0 && columns[index] <= columns[index-1] {
			return nil, fmt.Errorf("引用范围无效或已损坏：生成器拒绝无序投影列")
		}
	}
	if len(columns) == 0 || (scope.HeaderSlice == nil) != (scope.RowSlice == nil) {
		return nil, fmt.Errorf("引用范围无效或已损坏：生成器拒绝不完整切片")
	}
	chunkHash, _ := hex.DecodeString(chunk[1])
	documentSuffix, _ := hex.DecodeString(chunk[2])
	bytes := append(append([]byte{}, chunkHash...), documentSuffix...)
	ordinal, _ := strconv.Atoi(chunk[3])
	var err error
	if bytes, err = appendUnsignedVarint(bytes, ordinal); err != nil {
		return nil, err
	}
	hasSlices := scope.HeaderSlice != nil
	if hasSlices {
		bytes = append(bytes, 1)
	} else {
		bytes = append(bytes, 0)
	}
	baseStartRow, _ := strconv.Atoi(base[3])
	projectedStartRow, _ := strconv.Atoi(projected[3])
	projectedEndRow, _ := strconv.Atoi(projected[5])
	baseStartColumn := columnNumber(base[2])
	values := []int{projectedStartRow - baseStartRow, projectedEndRow - projectedStartRow, len(columns)}
	for index, value := range columns {
		if index == 0 {
			values = append(values, value-baseStartColumn)
		} else {
			values = append(values, value-columns[index-1])
		}
	}
	if hasSlices {
		values = append(values, scope.HeaderSlice.Start, scope.HeaderSlice.End-scope.HeaderSlice.Start, scope.RowSlice.Start, scope.RowSlice.End-scope.RowSlice.Start)
	}
	for _, value := range values {
		if bytes, err = appendUnsignedVarint(bytes, value); err != nil {
			return nil, err
		}
	}
	return bytes, nil
}

func scopedCitationID(row LexicalCandidateRow, scope SpreadsheetCitationScope) (string, error) {
	payload, err := encodeSpreadsheetCitationScopeV2(row, scope)
	if err != nil {
		return "", err
	}
	if payload == nil {
		return scopedCitationIDV1(row, scope)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	digest := citationDigest("drag-scoped-citation-v2\x00", "", row.ContentHash, payload)
	return CitationPrefix + "2." + encoded + "." + digest, nil
}

func scopedTextCitationID(row LexicalCandidateRow, slice ExcerptSlice) (string, error) {
	chunk := compactChunkID.FindStringSubmatch(row.ChunkID)
	if len(chunk) != 4 || slice.Start < 0 || slice.End <= slice.Start || slice.End > utf16Length(row.Text) {
		return "", fmt.Errorf("引用范围无效或已损坏：文本切片无法编码")
	}
	chunkHash, _ := hex.DecodeString(chunk[1])
	documentSuffix, _ := hex.DecodeString(chunk[2])
	payload := append(append([]byte{}, chunkHash...), documentSuffix...)
	ordinal, _ := strconv.Atoi(chunk[3])
	var err error
	for _, value := range []int{ordinal, slice.Start, slice.End - slice.Start} {
		payload, err = appendUnsignedVarint(payload, value)
		if err != nil {
			return "", err
		}
	}
	digest := citationDigest("drag-scoped-text-v1\x00", "", row.ContentHash, payload)
	return CitationPrefix + "3." + base64.RawURLEncoding.EncodeToString(payload) + "." + digest, nil
}

func parseSlice(values []int) (*ExcerptSlice, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) != 2 {
		return nil, fmt.Errorf("引用范围无效或已损坏：scope 切片错误")
	}
	return &ExcerptSlice{Start: values[0], End: values[1]}, nil
}

func parseCitationReference(citationID string) (citationReference, error) {
	value := strings.TrimPrefix(citationID, CitationPrefix)
	if strings.HasPrefix(value, "3.") {
		parts := strings.Split(value, ".")
		if len(parts) != 3 || parts[1] == "" || parts[2] == "" || len(parts[1]) > 256 {
			return citationReference{}, fmt.Errorf("引用范围无效或已损坏：文本 scoped citation 格式错误")
		}
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil || len(payload) < 15 || base64.RawURLEncoding.EncodeToString(payload) != parts[1] {
			return citationReference{}, fmt.Errorf("引用范围无效或已损坏：文本 scoped citation 无法解码")
		}
		offset := 12
		ordinal, err := readUnsignedVarint(payload, &offset)
		if err != nil {
			return citationReference{}, err
		}
		start, err := readUnsignedVarint(payload, &offset)
		if err != nil {
			return citationReference{}, err
		}
		length, err := readUnsignedVarint(payload, &offset)
		if err != nil || length < 1 || offset != len(payload) {
			return citationReference{}, fmt.Errorf("引用范围无效或已损坏：文本 scoped citation 切片非法")
		}
		return citationReference{ChunkID: fmt.Sprintf("chunk_%s_%s_%d", hex.EncodeToString(payload[:8]), hex.EncodeToString(payload[8:12]), ordinal), TextSlice: &ExcerptSlice{Start: start, End: start + length}, PayloadText: payload, Digest: parts[2]}, nil
	}
	if strings.HasPrefix(value, "2.") {
		parts := strings.Split(value, ".")
		if len(parts) != 3 || parts[1] == "" || parts[2] == "" || len(parts[1]) > 512 {
			return citationReference{}, fmt.Errorf("引用范围无效或已损坏：短 scoped citation 格式错误")
		}
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil || len(payload) < 17 || base64.RawURLEncoding.EncodeToString(payload) != parts[1] {
			return citationReference{}, fmt.Errorf("引用范围无效或已损坏：短 scoped citation 无法解码")
		}
		offset := 12
		ordinal, err := readUnsignedVarint(payload, &offset)
		if err != nil {
			return citationReference{}, err
		}
		if offset >= len(payload) || payload[offset]&^byte(1) != 0 {
			return citationReference{}, fmt.Errorf("引用范围无效或已损坏：短引用 flags 非法")
		}
		flags := payload[offset]
		offset++
		rowStartDelta, err := readUnsignedVarint(payload, &offset)
		if err != nil {
			return citationReference{}, err
		}
		rowSpan, err := readUnsignedVarint(payload, &offset)
		if err != nil {
			return citationReference{}, err
		}
		columnCount, err := readUnsignedVarint(payload, &offset)
		if err != nil || columnCount < 1 || columnCount > 12 {
			return citationReference{}, fmt.Errorf("引用范围无效或已损坏：短引用列数非法")
		}
		columnDeltas := make([]int, columnCount)
		for index := range columnDeltas {
			columnDeltas[index], err = readUnsignedVarint(payload, &offset)
			if err != nil {
				return citationReference{}, err
			}
			if index > 0 && columnDeltas[index] < 1 {
				return citationReference{}, fmt.Errorf("引用范围无效或已损坏：短引用投影列重复或无序")
			}
		}
		compact := &compactSpreadsheetCitationScopeV2{RowStartDelta: rowStartDelta, RowSpan: rowSpan, ColumnDeltas: columnDeltas}
		if flags&1 != 0 {
			headerStart, err := readUnsignedVarint(payload, &offset)
			if err != nil {
				return citationReference{}, err
			}
			headerLength, err := readUnsignedVarint(payload, &offset)
			if err != nil {
				return citationReference{}, err
			}
			rowStart, err := readUnsignedVarint(payload, &offset)
			if err != nil {
				return citationReference{}, err
			}
			rowLength, err := readUnsignedVarint(payload, &offset)
			if err != nil {
				return citationReference{}, err
			}
			compact.HeaderSlice = &ExcerptSlice{Start: headerStart, End: headerStart + headerLength}
			compact.RowSlice = &ExcerptSlice{Start: rowStart, End: rowStart + rowLength}
		}
		if offset != len(payload) {
			return citationReference{}, fmt.Errorf("引用范围无效或已损坏：短引用存在尾随数据")
		}
		return citationReference{ChunkID: fmt.Sprintf("chunk_%s_%s_%d", hex.EncodeToString(payload[:8]), hex.EncodeToString(payload[8:12]), ordinal), ScopeV2: compact, PayloadV2: payload, Digest: parts[2]}, nil
	}
	separator := strings.Index(value, "~")
	if separator < 0 {
		return citationReference{ChunkID: value}, nil
	}
	chunkID := value[:separator]
	token := value[separator+1:]
	digestSeparator := strings.LastIndex(token, ".")
	if chunkID == "" || digestSeparator <= 0 || len(token) > 4096 {
		return citationReference{}, fmt.Errorf("引用范围无效或已损坏：scoped citation 格式错误")
	}
	payload := token[:digestSeparator]
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return citationReference{}, fmt.Errorf("引用范围无效或已损坏：scoped citation 无法解码")
	}
	var wire citationScopeV1Wire
	if err := json.Unmarshal(raw, &wire); err != nil || wire.Version != 1 || wire.Locator == "" || wire.Columns == nil {
		return citationReference{}, fmt.Errorf("引用范围无效或已损坏：scope 字段错误")
	}
	headerSlice, err := parseSlice(wire.Header)
	if err != nil {
		return citationReference{}, err
	}
	rowSlice, err := parseSlice(wire.Row)
	if err != nil {
		return citationReference{}, err
	}
	return citationReference{ChunkID: chunkID, ScopeV1: &SpreadsheetCitationScope{Version: 1, Locator: wire.Locator, Columns: wire.Columns, HeaderSlice: headerSlice, RowSlice: rowSlice}, PayloadV1: payload, Digest: token[digestSeparator+1:]}, nil
}

func decodeSpreadsheetCitationScopeV2(baseLocator string, compact compactSpreadsheetCitationScopeV2) (SpreadsheetCitationScope, error) {
	base := searchSpreadsheetLocator.FindStringSubmatch(baseLocator)
	if len(base) != 6 {
		return SpreadsheetCitationScope{}, fmt.Errorf("引用范围无效或已损坏：短引用底层 locator 非表格范围")
	}
	baseStartColumn := columnNumber(base[2])
	current := baseStartColumn
	columns := make([]string, len(compact.ColumnDeltas))
	for index, delta := range compact.ColumnDeltas {
		if index == 0 {
			current = baseStartColumn + delta
		} else {
			current += delta
		}
		column, err := columnName(current)
		if err != nil {
			return SpreadsheetCitationScope{}, err
		}
		columns[index] = column
	}
	baseStartRow, _ := strconv.Atoi(base[3])
	startRow := baseStartRow + compact.RowStartDelta
	endRow := startRow + compact.RowSpan
	locator := fmt.Sprintf("%s!%s%d:%s%d", base[1], columns[0], startRow, columns[len(columns)-1], endRow)
	return SpreadsheetCitationScope{Version: 1, Locator: locator, Columns: columns, HeaderSlice: compact.HeaderSlice, RowSlice: compact.RowSlice}, nil
}

func MakeCitation(row LexicalCandidateRow, revision int64, projection *ExcerptProjection, override string) (Citation, error) {
	locator := row.Locator
	var scope *SpreadsheetCitationScope
	if projection != nil {
		locator = projection.Locator
		scope = projection.Scope
	}
	citationID := override
	if citationID == "" {
		if scope != nil {
			var err error
			citationID, err = scopedCitationID(row, *scope)
			if err != nil {
				return Citation{}, err
			}
		} else if projection != nil && projection.TextSlice != nil {
			var err error
			citationID, err = scopedTextCitationID(row, *projection.TextSlice)
			if err != nil {
				return Citation{}, err
			}
		} else {
			citationID = CitationPrefix + row.ChunkID
		}
	}
	headings := []string{}
	_ = json.Unmarshal([]byte(row.HeadingPathJSON), &headings)
	displaySection := row.SectionType
	if len(headings) > 0 {
		displaySection = headings[len(headings)-1]
	}
	return Citation{
		CitationID: citationID, Display: row.Title + " · " + displaySection + " · " + locator,
		SourceID: row.SourceID, SourceLabel: row.SourceLabel, SourceKind: row.SourceKind,
		AbsolutePath: row.AbsolutePath, RelativePath: row.RelativePath, DocumentID: row.ID, ChunkID: row.ChunkID,
		Locator: locator, HeadingPath: headings, IndexedContentHash: row.ContentHash, IndexRevision: revision,
		Stale: row.Stale, SourceLink: MakeSourceLink(row.AbsolutePath, locator),
	}, nil
}
