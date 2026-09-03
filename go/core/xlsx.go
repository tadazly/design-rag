package core

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type workbookSheet struct {
	Name string
	RID  string
	Path string
}

type sheetCell struct {
	Address     string
	Column      int
	FieldName   string
	RawValue    string
	CachedValue string
	Formula     string
	Text        string
}

type sheetRow struct {
	Number int
	Cells  []sheetCell
}

var (
	structuredSheetDateField = regexp.MustCompile(`(?i)^(修订|版本|修改|更新|变更)\s*(日期|时间)$|^(版本|version|招募版本|复用版本)$`)
	datedSheetNamePattern    = regexp.MustCompile(`(?i)(版本|修订|更新|迭代|测试内容)`)
)

type sheetDateAxis struct {
	row        int
	candidates map[int][]time.Time
	content    map[int]bool
}

type sheetDateCollector struct {
	sheetName   string
	dateColumns map[int]int
	axes        []*sheetDateAxis
	evidence    []DateEvidence
}

func newSheetDateCollector(sheetName string) *sheetDateCollector {
	collector := &sheetDateCollector{sheetName: sheetName, dateColumns: map[int]int{}, evidence: make([]DateEvidence, 0, 8)}
	if datedSheetNamePattern.MatchString(sheetName) {
		collector.add(findDates(sheetName), "dated_sheet", sheetName)
	}
	return collector
}

func (collector *sheetDateCollector) add(values []time.Time, kind, locator string) {
	for _, value := range values {
		duplicate := false
		for _, existing := range collector.evidence {
			if existing.TimestampMS == value.UnixMilli() && existing.Kind == kind && existing.Locator == locator {
				duplicate = true
				break
			}
		}
		if !duplicate {
			collector.evidence = append(collector.evidence, DateEvidence{TimestampMS: value.UnixMilli(), Strength: "strong", Kind: kind, Locator: locator})
		}
	}
}

func sheetCellDateValues(cell sheetCell, allowSerial bool) []time.Time {
	found := map[int64]time.Time{}
	for _, value := range append(findDates(cell.Text), findShortYearDates(cell.Text)...) {
		found[value.UnixMilli()] = value
	}
	if allowSerial {
		raw := strings.TrimSpace(cell.RawValue)
		if raw == "" {
			raw = strings.TrimSpace(cell.CachedValue)
		}
		if len(raw) == 5 {
			if serial, err := strconv.Atoi(raw); err == nil {
				if value, ok := excelSerialDate(serial); ok {
					found[value.UnixMilli()] = value
				}
			}
		}
	}
	return dateValues(found)
}

func (collector *sheetDateCollector) observe(row sheetRow) {
	for _, axis := range collector.axes {
		if row.Number <= axis.row {
			continue
		}
		for _, cell := range row.Cells {
			if _, tracked := axis.candidates[cell.Column]; tracked && strings.TrimSpace(cell.CachedValue) != "" {
				axis.content[cell.Column] = true
			}
		}
	}
	for column, headerRow := range collector.dateColumns {
		if row.Number <= headerRow {
			continue
		}
		for _, cell := range row.Cells {
			if cell.Column == column {
				collector.add(sheetCellDateValues(cell, true), "version_field", fmt.Sprintf("%s!%s", collector.sheetName, cell.Address))
			}
		}
	}

	semantic := false
	for _, cell := range row.Cells {
		if structuredSheetDateField.MatchString(strings.TrimSpace(cell.Text)) {
			collector.dateColumns[cell.Column] = row.Number
			semantic = true
		}
	}
	if !semantic {
		return
	}
	candidates := map[int][]time.Time{}
	for _, cell := range row.Cells {
		if values := sheetCellDateValues(cell, true); len(values) > 0 {
			candidates[cell.Column] = values
		}
	}
	if len(candidates) >= 2 {
		collector.axes = append(collector.axes, &sheetDateAxis{row: row.Number, candidates: candidates, content: map[int]bool{}})
	}
}

func (collector *sheetDateCollector) finish() []DateEvidence {
	for _, axis := range collector.axes {
		for column, values := range axis.candidates {
			if axis.content[column] {
				collector.add(values, "version_axis", fmt.Sprintf("%s!%s%d", collector.sheetName, encodeColumn(column), axis.row))
			}
		}
	}
	return collector.evidence
}

func reconcileWorkbookDateEvidence(evidence []DateEvidence) []DateEvidence {
	axisSheets := map[string]struct{}{}
	axisSheetsByDate := map[int64]map[string]struct{}{}
	for _, item := range evidence {
		if item.Kind != "version_axis" {
			continue
		}
		separator := strings.LastIndex(item.Locator, "!")
		if separator <= 0 {
			continue
		}
		sheetName := item.Locator[:separator]
		axisSheets[sheetName] = struct{}{}
		if axisSheetsByDate[item.TimestampMS] == nil {
			axisSheetsByDate[item.TimestampMS] = map[string]struct{}{}
		}
		axisSheetsByDate[item.TimestampMS][sheetName] = struct{}{}
	}
	if len(axisSheets) < 2 {
		return evidence
	}
	corroborated := map[int64]struct{}{}
	for timestampMS, sheets := range axisSheetsByDate {
		if len(sheets) >= 2 {
			corroborated[timestampMS] = struct{}{}
		}
	}
	if len(corroborated) == 0 {
		return evidence
	}
	result := make([]DateEvidence, 0, len(evidence))
	for _, item := range evidence {
		if item.Kind != "version_axis" {
			result = append(result, item)
			continue
		}
		if _, ok := corroborated[item.TimestampMS]; ok {
			result = append(result, item)
		}
	}
	return result
}

type xlsxNativeError struct {
	Stage string
	Err   error
}

func (failure *xlsxNativeError) Error() string {
	return fmt.Sprintf("Go 原生 OOXML %s失败：%v", failure.Stage, failure.Err)
}

func (failure *xlsxNativeError) Unwrap() error {
	return failure.Err
}

func wrapXlsxNativeError(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &xlsxNativeError{Stage: stage, Err: err}
}

func isXlsxNativeError(err error) bool {
	var failure *xlsxNativeError
	return errors.As(err, &failure)
}

func parseWorkbookSheets(file *zip.File) ([]workbookSheet, error) {
	if file == nil {
		return nil, fmt.Errorf("工作簿缺少 xl/workbook.xml")
	}
	stream, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	decoder := xml.NewDecoder(io.LimitReader(stream, 64*1024*1024))
	sheets := []workbookSheet{}
	for {
		token, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			return nil, tokenErr
		}
		start, ok := token.(xml.StartElement)
		if !ok || !strings.EqualFold(start.Name.Local, "sheet") {
			continue
		}
		sheets = append(sheets, workbookSheet{Name: attrLocal(start, "name"), RID: attrLocal(start, "id")})
	}
	return sheets, nil
}

func parseWorkbookRelationships(file *zip.File) (map[string]string, error) {
	result := map[string]string{}
	if file == nil {
		return result, fmt.Errorf("工作簿缺少 xl/_rels/workbook.xml.rels")
	}
	stream, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	decoder := xml.NewDecoder(io.LimitReader(stream, 16*1024*1024))
	for {
		token, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			return nil, tokenErr
		}
		start, ok := token.(xml.StartElement)
		if !ok || !strings.EqualFold(start.Name.Local, "Relationship") {
			continue
		}
		id := attrLocal(start, "Id")
		target := strings.ReplaceAll(attrLocal(start, "Target"), "\\", "/")
		if id == "" || target == "" || strings.Contains(strings.ToLower(attrLocal(start, "TargetMode")), "external") {
			continue
		}
		if strings.HasPrefix(target, "/") {
			target = strings.TrimPrefix(target, "/")
		} else {
			target = path.Join("xl", target)
		}
		result[id] = path.Clean(target)
	}
	return result, nil
}

func parseSharedStrings(file *zip.File) ([]string, error) {
	if file == nil {
		return nil, nil
	}
	stream, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	decoder := xml.NewDecoder(io.LimitReader(stream, maxExpandedEntryBytes))
	stringsTable := make([]string, 0, min(int(file.UncompressedSize64/16), 1_000_000))
	for {
		token, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			return nil, tokenErr
		}
		start, ok := token.(xml.StartElement)
		if !ok || !strings.EqualFold(start.Name.Local, "si") {
			continue
		}
		var builder strings.Builder
		depth := 1
		for depth > 0 {
			inner, innerErr := decoder.Token()
			if innerErr != nil {
				return nil, innerErr
			}
			switch value := inner.(type) {
			case xml.StartElement:
				depth++
				if strings.EqualFold(value.Name.Local, "t") {
					var content string
					if decodeErr := decoder.DecodeElement(&content, &value); decodeErr != nil {
						return nil, decodeErr
					}
					depth--
					builder.WriteString(content)
				}
			case xml.EndElement:
				depth--
			}
		}
		stringsTable = append(stringsTable, builder.String())
		if len(stringsTable) >= 2_000_000 {
			return nil, fmt.Errorf("sharedStrings 超过 2000000 项安全上限")
		}
	}
	return stringsTable, nil
}

func columnIndex(address string) int {
	column := 0
	for _, value := range strings.ToUpper(address) {
		if value < 'A' || value > 'Z' {
			break
		}
		column = column*26 + int(value-'A'+1)
	}
	return max(0, column-1)
}

func encodeColumn(column int) string {
	column++
	var buffer [16]byte
	index := len(buffer)
	for column > 0 {
		column--
		index--
		buffer[index] = byte('A' + column%26)
		column /= 26
	}
	return string(buffer[index:])
}

func readSheetCell(decoder *xml.Decoder, start xml.StartElement, shared []string) (sheetCell, error) {
	cell := sheetCell{Address: attrLocal(start, "r")}
	cell.Column = columnIndex(cell.Address)
	cellType := attrLocal(start, "t")
	var rawValue, inlineValue, formula string
	depth := 1
	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			return cell, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			depth++
			switch strings.ToLower(value.Name.Local) {
			case "v":
				if decodeErr := decoder.DecodeElement(&rawValue, &value); decodeErr != nil {
					return cell, decodeErr
				}
				depth--
			case "f":
				if decodeErr := decoder.DecodeElement(&formula, &value); decodeErr != nil {
					return cell, decodeErr
				}
				depth--
			case "t":
				var content string
				if decodeErr := decoder.DecodeElement(&content, &value); decodeErr != nil {
					return cell, decodeErr
				}
				depth--
				inlineValue += content
			}
		case xml.EndElement:
			depth--
		}
	}
	rawValue = strings.TrimSpace(rawValue)
	cachedValue := strings.TrimSpace(inlineValue)
	if cachedValue == "" {
		cachedValue = rawValue
	}
	if cellType == "s" {
		if index, parseErr := strconv.Atoi(rawValue); parseErr == nil && index >= 0 && index < len(shared) {
			cachedValue = shared[index]
			// The raw value of a shared-string cell is only an OOXML dictionary
			// offset, not business evidence. Keeping it duplicates an internal
			// integer beside every resolved string in the retrieval corpus.
			rawValue = ""
		}
	}
	cachedValue = strings.TrimSpace(spacePattern.ReplaceAllString(cachedValue, " "))
	cell.RawValue = rawValue
	cell.CachedValue = cachedValue
	cell.Formula = strings.TrimSpace(formula)
	// Text remains the cached/display value used by the current lexical projection.
	// Formula is retained separately so it never replaces a valid cached value or gets
	// duplicated into every row's FTS text.
	cell.Text = cachedValue
	return cell, nil
}

func applySheetFieldNames(rows []sheetRow, headers map[int]string) {
	for rowIndex := range rows {
		for cellIndex := range rows[rowIndex].Cells {
			rows[rowIndex].Cells[cellIndex].FieldName = headers[rows[rowIndex].Cells[cellIndex].Column]
		}
	}
}

func keepInlineVersionFieldName(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "版本", "修订", "修改", "更新", "变更", "version", "revision":
		return true
	default:
		return versionDateHeaderPattern.MatchString(normalized)
	}
}

func sheetBlock(sheetName string, rows []sheetRow, headers map[int]string, ordinal int) (Block, bool) {
	if len(rows) == 0 {
		return Block{}, false
	}
	lines := make([]string, 0, len(rows))
	minColumn := int(^uint(0) >> 1)
	maxColumn := 0
	actualCells := 0
	orderedHeaderColumns := make([]int, 0, len(headers))
	for column := range headers {
		orderedHeaderColumns = append(orderedHeaderColumns, column)
	}
	sort.Ints(orderedHeaderColumns)
	if len(orderedHeaderColumns) > 0 {
		headerValues := make([]string, 0, len(orderedHeaderColumns))
		for _, column := range orderedHeaderColumns {
			headerValues = append(headerValues, fmt.Sprintf("%s=%s", encodeColumn(column), headers[column]))
		}
		lines = append(lines, "字段 | "+strings.Join(headerValues, " | "))
	}
	applySheetFieldNames(rows, headers)
	for _, row := range rows {
		cells := make([]string, 0, len(row.Cells))
		for _, cell := range row.Cells {
			if cell.Text == "" && cell.Formula == "" {
				continue
			}
			// The row prefix is emitted once (`行 33`) and the column is emitted
			// once per value (`A=...`). Together with the chunk locator this still
			// identifies A33 exactly; repeating 33 beside every cell adds hundreds
			// of megabytes to large workbooks without adding location information.
			label := encodeColumn(cell.Column)
			// Every chunk already carries the complete column-to-field mapping.
			// worksheets. Keep only version/date labels inline because the business
			// date resolver intentionally uses those row-level markers.
			// Repeating long field names beside every cell roughly doubles large
			// worksheets. Keep only version/date labels inline because the business
			// date resolver intentionally uses those row-level markers.
			// worksheets. Keep only version/date labels inline because the business
			// date resolver intentionally uses those row-level markers.
			if cell.FieldName != "" && cell.FieldName != cell.Text && keepInlineVersionFieldName(cell.FieldName) {
				label += "[" + cell.FieldName + "]"
			}
			cached := cell.Text
			if cached == "" {
				cached = "[无缓存值]"
			}
			value := fmt.Sprintf("%s=%s", label, cached)
			details := []string{}
			if cell.RawValue != "" && cell.RawValue != cell.CachedValue {
				details = append(details, "raw="+cell.RawValue)
			}
			if cell.Formula != "" {
				details = append(details, "formula="+cell.Formula)
			}
			if len(details) > 0 {
				value += " {" + strings.Join(details, "; ") + "}"
			}
			cells = append(cells, value)
			minColumn = min(minColumn, cell.Column)
			maxColumn = max(maxColumn, cell.Column)
			actualCells++
		}
		if len(cells) > 0 {
			lines = append(lines, fmt.Sprintf("行 %d | %s", row.Number, strings.Join(cells, " | ")))
		}
	}
	if len(lines) == 0 || actualCells == 0 {
		return Block{}, false
	}
	firstRow := rows[0].Number
	lastRow := rows[len(rows)-1].Number
	rangeValue := fmt.Sprintf("%s%d:%s%d", encodeColumn(minColumn), firstRow, encodeColumn(maxColumn), lastRow)
	text := strings.Join(lines, "\n")
	return Block{
		Ordinal:     ordinal,
		Text:        text,
		HeadingPath: []string{sheetName},
		SectionType: ClassifySection([]string{sheetName}, text),
		Locator:     fmt.Sprintf("%s!%s", sheetName, rangeValue),
	}, true
}

func parseWorksheet(file *zip.File, sheetName string, shared []string, startOrdinal int) ([]Block, []DateEvidence, error) {
	if file == nil {
		return nil, nil, fmt.Errorf("工作表 %s 对应 XML 不存在", sheetName)
	}
	stream, err := file.Open()
	if err != nil {
		return nil, nil, err
	}
	defer stream.Close()
	decoder := xml.NewDecoder(io.LimitReader(stream, maxExpandedEntryBytes))
	blocks := []Block{}
	rows := make([]sheetRow, 0, 24)
	dateCollector := newSheetDateCollector(sheetName)
	headers := map[int]string{}
	groupSize := 192
	if strings.Contains(sheetName, "属性") {
		groupSize = 144
	}
	rowSequence := 0
	flush := func() {
		if block, ok := sheetBlock(sheetName, rows, headers, startOrdinal+len(blocks)); ok {
			blocks = append(blocks, block)
		}
		rows = rows[:0]
	}
	for {
		token, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			return nil, nil, tokenErr
		}
		start, ok := token.(xml.StartElement)
		if !ok || !strings.EqualFold(start.Name.Local, "row") {
			continue
		}
		rowSequence++
		rowNumber := rowSequence
		if parsed, parseErr := strconv.Atoi(attrLocal(start, "r")); parseErr == nil && parsed > 0 {
			rowNumber = parsed
		}
		row := sheetRow{Number: rowNumber}
		depth := 1
		for depth > 0 {
			inner, innerErr := decoder.Token()
			if innerErr != nil {
				return nil, nil, innerErr
			}
			switch value := inner.(type) {
			case xml.StartElement:
				depth++
				if strings.EqualFold(value.Name.Local, "c") {
					cell, cellErr := readSheetCell(decoder, value, shared)
					if cellErr != nil {
						return nil, nil, cellErr
					}
					depth--
					if cell.CachedValue != "" || cell.Formula != "" {
						row.Cells = append(row.Cells, cell)
					}
				}
			case xml.EndElement:
				depth--
			}
		}
		if len(row.Cells) == 0 {
			continue
		}
		sort.Slice(row.Cells, func(i, j int) bool { return row.Cells[i].Column < row.Cells[j].Column })
		if len(headers) == 0 && len(row.Cells) >= 2 {
			for _, cell := range row.Cells {
				if len([]rune(cell.Text)) <= 80 {
					headers[cell.Column] = cell.Text
				}
			}
		}
		dateCollector.observe(row)
		rows = append(rows, row)
		if len(rows) >= groupSize {
			flush()
		}
	}
	flush()
	return blocks, dateCollector.finish(), nil
}

func extractXlsx(filePath string) (ExtractedDocument, error) {
	archive, err := zip.OpenReader(filePath)
	if err != nil {
		return ExtractedDocument{}, wrapXlsxNativeError("ZIP 打开", err)
	}
	defer archive.Close()
	sheets, err := parseWorkbookSheets(zipFile(archive, "xl/workbook.xml"))
	if err != nil {
		return ExtractedDocument{}, wrapXlsxNativeError("工作簿结构解析", err)
	}
	relationships, err := parseWorkbookRelationships(zipFile(archive, "xl/_rels/workbook.xml.rels"))
	if err != nil {
		return ExtractedDocument{}, wrapXlsxNativeError("工作簿关系解析", err)
	}
	shared, err := parseSharedStrings(zipFile(archive, "xl/sharedStrings.xml"))
	if err != nil {
		return ExtractedDocument{}, wrapXlsxNativeError("共享字符串解析", err)
	}
	blocks := []Block{}
	dateEvidence := make([]DateEvidence, 0, 16)
	var bytesRead int64
	for _, name := range []string{"xl/workbook.xml", "xl/_rels/workbook.xml.rels", "xl/sharedStrings.xml"} {
		if entry := zipFile(archive, name); entry != nil {
			bytesRead += int64(entry.CompressedSize64)
		}
	}
	for index := range sheets {
		sheets[index].Path = relationships[sheets[index].RID]
		if sheets[index].Path == "" {
			return ExtractedDocument{}, wrapXlsxNativeError(
				fmt.Sprintf("工作表 %q 关系解析", sheets[index].Name),
				fmt.Errorf("关系 %q 缺少内部 worksheet 目标", sheets[index].RID),
			)
		}
		sheetEntry := zipFile(archive, sheets[index].Path)
		if sheetEntry != nil {
			bytesRead += int64(sheetEntry.CompressedSize64)
		}
		sheetBlocks, sheetEvidence, sheetErr := parseWorksheet(sheetEntry, sheets[index].Name, shared, len(blocks))
		if sheetErr != nil {
			return ExtractedDocument{}, wrapXlsxNativeError(fmt.Sprintf("工作表 %q 解析", sheets[index].Name), sheetErr)
		}
		blocks = append(blocks, sheetBlocks...)
		dateEvidence = append(dateEvidence, sheetEvidence...)
	}
	warnings := []string{}
	for _, file := range archive.File {
		if strings.HasSuffix(strings.ToLower(file.Name), "vbaproject.bin") {
			warnings = append(warnings, "工作簿包含 VBA 项目；索引器仅只读解析，不执行宏")
			break
		}
	}
	if len(blocks) == 0 {
		warnings = append(warnings, "工作簿未发现实际非空单元格")
	}
	var embeddedModifiedAt *time.Time
	if shouldReadEmbeddedModified(filePath) {
		embeddedModifiedAt = zipCoreModified(archive)
		if entry := zipFile(archive, "docProps/core.xml"); entry != nil {
			bytesRead += int64(entry.CompressedSize64)
		}
	}
	return ExtractedDocument{
		Title:              strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath)),
		Blocks:             blocks,
		EmbeddedModifiedAt: embeddedModifiedAt,
		Warnings:           warnings,
		BytesRead:          bytesRead,
		DateEvidence:       reconcileWorkbookDateEvidence(dateEvidence),
	}, nil
}
