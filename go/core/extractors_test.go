package core

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/giraffesyo/pdf/pdftest"
	"github.com/xuri/excelize/v2"
)

type recordingFallback struct {
	called bool
	full   bool
	draft  *Draft
	err    error
}

func TestPureGoPDFExtractsPageTextAndFlagsImageOnlyDocuments(t *testing.T) {
	root := t.TempDir()
	textPDF := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
		pdftest.Stream("", "BT /F1 12 Tf 72 720 Td (Wheel reward configuration) Tj ET"),
		pdftest.Helvetica(),
	)
	textPath := filepath.Join(root, "text.pdf")
	if err := os.WriteFile(textPath, textPDF, 0o644); err != nil {
		t.Fatal(err)
	}
	document, err := extractPDF(context.Background(), textPath)
	if err != nil {
		t.Fatal(err)
	}
	if document.NeedsOCR || len(document.Blocks) != 1 || document.Blocks[0].Locator != "第 1 页" || !strings.Contains(document.Blocks[0].Text, "Wheel reward configuration") {
		t.Fatalf("PDF text extraction failed: %#v", document)
	}

	imageOnly := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /XObject << /Im1 5 0 R >> >>"),
		pdftest.Stream("", "q /Im1 Do Q"),
		pdftest.Stream("/Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8", "\x00"),
	)
	imagePath := filepath.Join(root, "scan.pdf")
	if err := os.WriteFile(imagePath, imageOnly, 0o644); err != nil {
		t.Fatal(err)
	}
	scan, err := extractPDF(context.Background(), imagePath)
	if err != nil {
		t.Fatal(err)
	}
	if !scan.NeedsOCR || len(scan.Blocks) != 0 {
		t.Fatalf("image-only PDF must be explicitly marked needsOcr: %#v", scan)
	}

	blankPath := filepath.Join(root, "blank.pdf")
	blank := pdftest.Build(1, pdftest.Catalog(2), pdftest.Pages(3), pdftest.Page(2, 4, "<< >>"), pdftest.Stream("", "q Q"))
	if err := os.WriteFile(blankPath, blank, 0o644); err != nil {
		t.Fatal(err)
	}
	blankDocument, err := extractPDF(context.Background(), blankPath)
	if err != nil {
		t.Fatal(err)
	}
	if blankDocument.NeedsOCR || len(blankDocument.Warnings) == 0 || !strings.Contains(strings.Join(blankDocument.Warnings, "\n"), "空白或矢量轮廓") {
		t.Fatalf("blank/vector PDF must not claim OCR applicability: %#v", blankDocument)
	}
}

func TestPDFDateRejectsInvalidTimeAndTrailingData(t *testing.T) {
	for _, value := range []string{"D:20260902126000Z", "D:20260902120060Z", "D:20260902120000+2460", "D:20260902120000Zjunk"} {
		if parsed := parsePDFDate(value); parsed != nil {
			t.Fatalf("invalid PDF date %q parsed as %s", value, parsed)
		}
	}
	for _, value := range []string{"D:20260902120000Z", "D:20260902120000+08'00'", "20260902"} {
		if parsed := parsePDFDate(value); parsed == nil {
			t.Fatalf("valid PDF date %q was rejected", value)
		}
	}
}

func TestPureGoExcelizeCompatibilityPreservesValuesFormulaAndDates(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "compat.xlsx")
	workbook := excelize.NewFile()
	defer workbook.Close()
	if err := workbook.SetSheetName("Sheet1", "版本"); err != nil {
		t.Fatal(err)
	}
	for cell, value := range map[string]any{"A1": "版本日期", "B1": "名称", "A2": "2026-09-02", "B2": "轮盘抽奖", "C1": "奖励"} {
		if err := workbook.SetCellValue("版本", cell, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := workbook.SetCellFormula("版本", "C2", `CONCAT("钻","石")`); err != nil {
		t.Fatal(err)
	}
	if err := workbook.SaveAs(filePath); err != nil {
		t.Fatal(err)
	}
	document, err := extractSpreadsheetCompatibility(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Blocks) == 0 || !strings.Contains(document.Blocks[0].Text, "B=轮盘抽奖") || !strings.Contains(document.Blocks[0].Text, "formula=CONCAT") || len(document.DateEvidence) == 0 {
		t.Fatalf("Excelize compatibility lost structure: %#v", document)
	}
}

func TestMislabelledOOXMLWithXLSExtensionUsesContentSniffing(t *testing.T) {
	root := t.TempDir()
	xlsxPath := filepath.Join(root, "source.xlsx")
	filePath := filepath.Join(root, "mislabelled.xls")
	workbook := excelize.NewFile()
	if err := workbook.SetCellValue("Sheet1", "A1", "轮盘奖池"); err != nil {
		t.Fatal(err)
	}
	if err := workbook.SaveAs(xlsxPath); err != nil {
		t.Fatal(err)
	}
	if err := workbook.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(xlsxPath, filePath); err != nil {
		t.Fatal(err)
	}
	document, fallback, err := extractByExtension(Candidate{AbsolutePath: filePath, Extension: ".xls"})
	if err != nil {
		t.Fatal(err)
	}
	if !fallback || len(document.Blocks) == 0 || !strings.Contains(document.Blocks[0].Text, "轮盘奖池") {
		t.Fatalf("mislabelled OOXML was not recovered: fallback=%v document=%#v", fallback, document)
	}
}

func (fallback *recordingFallback) Extract(_ context.Context, _ Candidate, _ string, full bool) (*Draft, error) {
	fallback.called = true
	fallback.full = full
	return fallback.draft, fallback.err
}

func parseGoldenCell(t *testing.T, source string, shared []string) sheetCell {
	t.Helper()
	decoder := xml.NewDecoder(strings.NewReader(source))
	token, err := decoder.Token()
	if err != nil {
		t.Fatal(err)
	}
	start, ok := token.(xml.StartElement)
	if !ok {
		t.Fatalf("expected start element, got %T", token)
	}
	cell, err := readSheetCell(decoder, start, shared)
	if err != nil {
		t.Fatal(err)
	}
	return cell
}

func writeArchive(t *testing.T, destination string, entries map[string]string) {
	t.Helper()
	file, err := os.Create(destination)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	for name, content := range entries {
		entry, createErr := archive.Create(name)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := entry.Write([]byte(content)); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExtractDocxPreservesHeadingsTablesAndModifiedDate(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "玩法.docx")
	writeArchive(t, filePath, map[string]string{
		"word/document.xml": `<?xml version="1.0"?><w:document xmlns:w="w"><w:body>
<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>玩法流程</w:t></w:r></w:p>
<w:p><w:r><w:t>进入活动后完成挑战并获得奖励。</w:t></w:r></w:p>
<w:tbl><w:tr><w:tc><w:p><w:r><w:t>字段</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>值</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
</w:body></w:document>`,
		"docProps/core.xml": `<cp:coreProperties xmlns:cp="cp" xmlns:dcterms="dcterms"><dcterms:modified>2026-08-30T10:11:12Z</dcterms:modified></cp:coreProperties>`,
	})
	document, err := extractDocx(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if document.EmbeddedModifiedAt == nil || len(document.Blocks) != 2 {
		t.Fatalf("unexpected document: %#v", document)
	}
	if !strings.Contains(document.Blocks[0].Locator, "段落") || document.Blocks[0].HeadingPath[0] != "玩法流程" {
		t.Fatalf("unexpected paragraph: %#v", document.Blocks[0])
	}
	if document.Blocks[1].Locator != "表格 1 行 1-1" || !strings.Contains(document.Blocks[1].Text, "字段 | 值") {
		t.Fatalf("unexpected table: %#v", document.Blocks[1])
	}
}

func TestExtractXlsxIgnoresDimensionAndKeepsRanges(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "newLottery.xlsx")
	writeArchive(t, filePath, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="r"><sheets><sheet name="newLottery" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst><si><t>字段</t></si><si><t>值</t></si><si><t>奖池ID</t></si><si><t>1001</t></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet><dimension ref="A1:XFD1048576"/><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>
<row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2" t="s"><v>3</v></c></row>
</sheetData></worksheet>`,
		"docProps/core.xml": `<coreProperties><modified>2026-08-31T00:00:00Z</modified></coreProperties>`,
	})
	document, err := extractXlsx(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Blocks) != 1 {
		t.Fatalf("unexpected blocks: %#v", document.Blocks)
	}
	block := document.Blocks[0]
	if block.Locator != "newLottery!A1:B2" || !strings.Contains(block.Text, "字段 | A=字段 | B=值") ||
		!strings.Contains(block.Text, "行 2 | A=奖池ID | B=1001") || strings.Contains(block.Text, "raw=0") {
		t.Fatalf("unexpected block: %#v", block)
	}
}

func TestSheetDateCollectorUsesStructuredVersionEvidence(t *testing.T) {
	t.Parallel()
	want := func(year int, month time.Month, day int) int64 {
		return time.Date(year, month, day, 0, 0, 0, 0, time.UTC).UnixMilli()
	}
	assertEvidence := func(t *testing.T, evidence []DateEvidence, kind string, timestampMS int64, locator string) {
		t.Helper()
		for _, item := range evidence {
			if item.Kind == kind && item.TimestampMS == timestampMS && strings.Contains(item.Locator, locator) {
				return
			}
		}
		t.Fatalf("missing %s at %d in %#v", kind, timestampMS, evidence)
	}
	assertNoEvidence := func(t *testing.T, evidence []DateEvidence, kind string, timestampMS int64) {
		t.Helper()
		for _, item := range evidence {
			if item.Kind == kind && item.TimestampMS == timestampMS {
				t.Fatalf("unexpected %s at %d in %#v", kind, timestampMS, evidence)
			}
		}
	}

	t.Run("revision table", func(t *testing.T) {
		collector := newSheetDateCollector("修订记录")
		collector.observe(sheetRow{Number: 1, Cells: []sheetCell{
			{Address: "A1", Column: 0, Text: "修订号", CachedValue: "修订号"},
			{Address: "B1", Column: 1, Text: "修订日期", CachedValue: "修订日期"},
			{Address: "C1", Column: 2, Text: "修订内容", CachedValue: "修订内容"},
		}})
		collector.observe(sheetRow{Number: 2, Cells: []sheetCell{{Address: "B2", Column: 1, Text: "2026-01-02", CachedValue: "2026-01-02"}}})
		collector.observe(sheetRow{Number: 3, Cells: []sheetCell{{Address: "B3", Column: 1, Text: "2026-02-03", CachedValue: "2026-02-03"}}})
		assertEvidence(t, collector.finish(), "version_field", want(2026, time.February, 3), "B3")
	})

	for _, header := range []string{"版本", "version"} {
		t.Run("exact "+header+" field", func(t *testing.T) {
			collector := newSheetDateCollector("weekly")
			collector.observe(sheetRow{Number: 1, Cells: []sheetCell{{Address: "A1", Column: 0, Text: header, CachedValue: header}}})
			collector.observe(sheetRow{Number: 2, Cells: []sheetCell{{Address: "A2", Column: 0, Text: "20261007", CachedValue: "20261007"}}})
			assertEvidence(t, collector.finish(), "version_field", want(2026, time.October, 7), "A2")
		})
	}

	t.Run("near-match field is not a version date", func(t *testing.T) {
		collector := newSheetDateCollector("smoke")
		collector.observe(sheetRow{Number: 1, Cells: []sheetCell{{Address: "A1", Column: 0, Text: "版本精灵", CachedValue: "版本精灵"}}})
		collector.observe(sheetRow{Number: 2, Cells: []sheetCell{{Address: "A2", Column: 0, Text: "20261231", CachedValue: "20261231"}}})
		assertNoEvidence(t, collector.finish(), "version_field", want(2026, time.December, 31))
	})

	t.Run("horizontal axis excludes formula-only tail", func(t *testing.T) {
		collector := newSheetDateCollector("roadmap")
		collector.observe(sheetRow{Number: 1, Cells: []sheetCell{
			{Address: "A1", Column: 0, Text: "版本", CachedValue: "版本"},
			{Address: "B1", Column: 1, Text: "2026-01-01", CachedValue: "2026-01-01"},
			{Address: "C1", Column: 2, Text: "2026-01-08", CachedValue: "2026-01-08", Formula: "B1+7"},
		}})
		collector.observe(sheetRow{Number: 2, Cells: []sheetCell{
			{Address: "B2", Column: 1, Text: "正式版本内容", CachedValue: "正式版本内容"},
			{Address: "C2", Column: 2, Formula: "B2"},
		}})
		evidence := collector.finish()
		assertEvidence(t, evidence, "version_axis", want(2026, time.January, 1), "B1")
		assertNoEvidence(t, evidence, "version_axis", want(2026, time.January, 8))
	})

	t.Run("recruitment version ignores adjacent entry date", func(t *testing.T) {
		collector := newSheetDateCollector("体验服招募记录")
		collector.observe(sheetRow{Number: 1, Cells: []sheetCell{
			{Address: "A1", Column: 0, Text: "期数", CachedValue: "期数"},
			{Address: "B1", Column: 1, Text: "招募版本", CachedValue: "招募版本"},
			{Address: "C1", Column: 2, Text: "玩家可进入时间", CachedValue: "玩家可进入时间"},
		}})
		collector.observe(sheetRow{Number: 2, Cells: []sheetCell{
			{Address: "B2", Column: 1, Text: "20241127", CachedValue: "20241127"},
			{Address: "C2", Column: 2, Text: "预期20241204开始", CachedValue: "预期20241204开始"},
		}})
		evidence := collector.finish()
		assertEvidence(t, evidence, "version_field", want(2024, time.November, 27), "B2")
		assertNoEvidence(t, evidence, "version_field", want(2024, time.December, 4))
	})

	t.Run("dated test-content sheet", func(t *testing.T) {
		collector := newSheetDateCollector("精灵测试内容20240626")
		assertEvidence(t, collector.finish(), "dated_sheet", want(2024, time.June, 26), "精灵测试内容")
	})

	t.Run("cross-sheet axes keep only corroborated dates", func(t *testing.T) {
		evidence := []DateEvidence{
			{TimestampMS: want(2026, time.January, 1), Strength: "strong", Kind: "version_axis", Locator: "主排期!B1"},
			{TimestampMS: want(2026, time.January, 8), Strength: "strong", Kind: "version_axis", Locator: "主排期!C1"},
			{TimestampMS: want(2026, time.January, 1), Strength: "strong", Kind: "version_axis", Locator: "复用排期!B1"},
		}
		reconciled := reconcileWorkbookDateEvidence(evidence)
		assertEvidence(t, reconciled, "version_axis", want(2026, time.January, 1), "主排期")
		assertNoEvidence(t, reconciled, "version_axis", want(2026, time.January, 8))
	})
}

func TestSheetCellKeepsRawCachedFieldAndFormulaBoundaries(t *testing.T) {
	dateCell := parseGoldenCell(t, `<c r="A2" t="d"><v>2026-09-01T00:00:00Z</v></c>`, nil)
	percentCell := parseGoldenCell(t, `<c r="B2"><v>0.25</v></c>`, nil)
	idCell := parseGoldenCell(t, `<c r="C2" t="inlineStr"><is><t>00123</t></is></c>`, nil)
	formulaCell := parseGoldenCell(t, `<c r="D2"><f>SUM(B2, 0.75)</f><v>1</v></c>`, nil)
	formulaOnlyCell := parseGoldenCell(t, `<c r="E2"><f>NOW()</f></c>`, nil)

	if dateCell.RawValue != "2026-09-01T00:00:00Z" || dateCell.CachedValue != dateCell.RawValue {
		t.Fatalf("date boundary lost: %#v", dateCell)
	}
	if percentCell.RawValue != "0.25" || percentCell.CachedValue != "0.25" {
		t.Fatalf("percent boundary lost: %#v", percentCell)
	}
	if idCell.RawValue != "" || idCell.CachedValue != "00123" {
		t.Fatalf("ID boundary lost: %#v", idCell)
	}
	if formulaCell.RawValue != "1" || formulaCell.CachedValue != "1" || formulaCell.Text != "1" || formulaCell.Formula != "SUM(B2, 0.75)" {
		t.Fatalf("formula/cached boundary lost: %#v", formulaCell)
	}
	if formulaOnlyCell.CachedValue != "" || formulaOnlyCell.Text != "" || formulaOnlyCell.Formula != "NOW()" {
		t.Fatalf("formula-only boundary lost: %#v", formulaOnlyCell)
	}

	rows := []sheetRow{{Number: 2, Cells: []sheetCell{dateCell, percentCell, idCell, formulaCell, formulaOnlyCell}}}
	headers := map[int]string{0: "日期", 1: "比例", 2: "ID", 3: "缓存结果", 4: "无缓存公式"}
	applySheetFieldNames(rows, headers)
	for _, cell := range rows[0].Cells {
		if cell.FieldName != headers[cell.Column] {
			t.Fatalf("field name not retained: %#v", cell)
		}
	}
	block, ok := sheetBlock("边界", rows, headers, 0)
	if !ok || !strings.Contains(block.Text, "字段 | A=日期 | B=比例 | C=ID | D=缓存结果 | E=无缓存公式") ||
		!strings.Contains(block.Text, "行 2 | A=2026-09-01T00:00:00Z") ||
		!strings.Contains(block.Text, "B=0.25") || !strings.Contains(block.Text, "C=00123") ||
		!strings.Contains(block.Text, "D=1 {formula=SUM(B2, 0.75)}") ||
		!strings.Contains(block.Text, "E=[无缓存值] {formula=NOW()}") ||
		strings.Contains(block.Text, "[日期]") ||
		strings.Count(block.Text, "formula=SUM(B2, 0.75)") != 1 || strings.Count(block.Text, "formula=NOW()") != 1 {
		t.Fatalf("cell address/field/cached/formula evidence lost: %#v", block)
	}
}

func TestSpreadsheetChunksRepeatHeaderOnceAndNarrowRowLocator(t *testing.T) {
	lines := []string{"字段 | A=ID | B=类型 | C=奖励ID | D=说明"}
	for row := 1; row <= 420; row++ {
		lines = append(lines, fmt.Sprintf("行 %d | A=%d | B=奖励 | C=reward_%d | D=%s", row, row, row, strings.Repeat("描述", 24)))
	}
	chunks := ChunkBlocks([]Block{{
		Text: strings.Join(lines, "\n"), HeadingPath: []string{"newPrizePool"},
		SectionType: "config", Locator: "newPrizePool!A1:D420",
	}})
	if len(chunks) < 2 {
		t.Fatalf("fixture must create multiple chunks: %d", len(chunks))
	}
	previousLast := 0
	seenRows := map[int]int{}
	for _, chunk := range chunks {
		if !strings.HasPrefix(chunk.Text, "字段 | A=ID") || strings.Count(chunk.Text, "字段 | A=ID") != 1 {
			t.Fatalf("chunk lost header: %s", chunk.Text[:min(len(chunk.Text), 120)])
		}
		if utf8.RuneCountInString(chunk.Text) > chunkTargetRunes {
			t.Fatalf("chunk exceeds target boundary: runes=%d locator=%s", utf8.RuneCountInString(chunk.Text), chunk.Locator)
		}
		match := spreadsheetLocatorPattern.FindStringSubmatch(chunk.Locator)
		if len(match) != 4 {
			t.Fatalf("chunk locator is not a sheet range: %s", chunk.Locator)
		}
		startRow, _ := strconv.Atoi(strings.TrimLeft(match[2], "ABCDEFGHIJKLMNOPQRSTUVWXYZ"))
		lastRow, _ := strconv.Atoi(strings.TrimLeft(match[3], "ABCDEFGHIJKLMNOPQRSTUVWXYZ"))
		if startRow != previousLast+1 || lastRow < startRow {
			t.Fatalf("chunk ranges must be ordered and non-overlapping: previous=%d current=%s", previousLast, chunk.Locator)
		}
		chunkRows := []int{}
		for _, line := range strings.Split(chunk.Text, "\n")[1:] {
			rowMatch := spreadsheetRowPattern.FindStringSubmatch(line)
			if len(rowMatch) != 2 {
				t.Fatalf("chunk contains unlocatable output row: %q", line)
			}
			rowNumber, err := strconv.Atoi(rowMatch[1])
			if err != nil {
				t.Fatal(err)
			}
			chunkRows = append(chunkRows, rowNumber)
			seenRows[rowNumber]++
		}
		if len(chunkRows) == 0 || chunkRows[0] != startRow || chunkRows[len(chunkRows)-1] != lastRow || len(chunkRows) != lastRow-startRow+1 {
			t.Fatalf("locator does not cover exactly the output rows: locator=%s rows=%v", chunk.Locator, chunkRows)
		}
		previousLast = lastRow
	}
	if previousLast != 420 {
		t.Fatalf("last row lost: %d", previousLast)
	}
	for row := 1; row <= 420; row++ {
		if seenRows[row] != 1 {
			t.Fatalf("row %d must appear exactly once, got %d", row, seenRows[row])
		}
	}
}

func TestXlsxNativeZipAndStructureFailuresStayInsidePureGoCompatibilityPath(t *testing.T) {
	root := t.TempDir()
	invalidZip := filepath.Join(root, "mislabeled.xlsx")
	if err := os.WriteFile(invalidZip, []byte("not a zip workbook"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, filePath := range []string{invalidZip, filepath.Join(root, "missing-relationships.xlsm")} {
		if strings.HasSuffix(filePath, ".xlsm") {
			writeArchive(t, filePath, map[string]string{
				"xl/workbook.xml": `<workbook xmlns:r="r"><sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`,
			})
		}
		candidate := Candidate{AbsolutePath: filePath, Extension: strings.ToLower(filepath.Ext(filePath))}
		_, needsFallback, err := extractByExtension(candidate)
		if err == nil || !needsFallback || !strings.Contains(err.Error(), "纯 Go compatibility fallback") {
			t.Fatalf("%s must report the native and pure-Go compatibility failures: needsFallback=%v err=%v", filePath, needsFallback, err)
		}
	}
}

func TestProcessCandidateNeverCallsHostFallbackForTypedXlsxFailure(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "legacy-biff-mislabeled.xlsx")
	if err := os.WriteFile(filePath, []byte("not a native OOXML zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{
		SourceID: "plans", SourceLabel: "策划案", SourceKind: "design", SourceIdentity: "test",
		RootPath: root, AbsolutePath: filePath, RelativePath: filepath.Base(filePath), Extension: ".xlsx",
		SizeBytes: info.Size(), FilesystemMtimeMS: info.ModTime().UnixMilli(),
	}
	fallback := &recordingFallback{draft: &Draft{Title: "不应使用的 host fallback"}}
	result := ProcessCandidate(context.Background(), candidate, nil, false, fallback)
	if fallback.called || !result.Fallback || result.Draft != nil || result.Issue == nil {
		t.Fatalf("unexpected fallback result: fallback=%#v result=%#v", fallback, result)
	}
	if !strings.Contains(result.Issue.Message, "Go 原生 OOXML") || !strings.Contains(result.Issue.Message, "纯 Go compatibility fallback") {
		t.Fatalf("both pure-Go diagnostics must remain transparent: %#v", result.Issue)
	}
}
