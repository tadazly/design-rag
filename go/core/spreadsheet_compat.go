package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	legacyxls "github.com/nkiri/xls"
	"github.com/xuri/excelize/v2"
)

const compatibilityCellLimit = 1_000_000

var oleCompoundMagic = []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}

func isOLECompoundFile(filePath string) bool {
	file, err := os.Open(filePath)
	if err != nil {
		return false
	}
	defer file.Close()
	buffer := make([]byte, len(oleCompoundMagic))
	if _, err = file.Read(buffer); err != nil {
		return false
	}
	return string(buffer) == string(oleCompoundMagic)
}

type compatibilitySheetBuilder struct {
	name          string
	blocks        []Block
	rows          []sheetRow
	headers       map[int]string
	dateCollector *sheetDateCollector
	groupSize     int
	cellCount     int
}

func newCompatibilitySheetBuilder(name string, ordinal int) *compatibilitySheetBuilder {
	groupSize := 192
	if strings.Contains(name, "属性") {
		groupSize = 144
	}
	return &compatibilitySheetBuilder{
		name:          name,
		blocks:        make([]Block, 0, 8),
		rows:          make([]sheetRow, 0, groupSize),
		headers:       map[int]string{},
		dateCollector: newSheetDateCollector(name),
		groupSize:     groupSize,
	}
}

func (builder *compatibilitySheetBuilder) addRow(row sheetRow, startOrdinal int) error {
	if len(row.Cells) == 0 {
		return nil
	}
	sort.Slice(row.Cells, func(i, j int) bool { return row.Cells[i].Column < row.Cells[j].Column })
	if len(builder.headers) == 0 && len(row.Cells) >= 2 {
		for _, cell := range row.Cells {
			if len([]rune(cell.Text)) <= 80 {
				builder.headers[cell.Column] = cell.Text
			}
		}
	}
	builder.cellCount += len(row.Cells)
	if builder.cellCount > compatibilityCellLimit {
		return fmt.Errorf("工作簿超过 %d 个非空单元格安全上限", compatibilityCellLimit)
	}
	builder.dateCollector.observe(row)
	builder.rows = append(builder.rows, row)
	if len(builder.rows) >= builder.groupSize {
		builder.flush(startOrdinal)
	}
	return nil
}

func (builder *compatibilitySheetBuilder) flush(startOrdinal int) {
	if block, ok := sheetBlock(builder.name, builder.rows, builder.headers, startOrdinal+len(builder.blocks)); ok {
		builder.blocks = append(builder.blocks, block)
	}
	builder.rows = builder.rows[:0]
}

func cleanCompatibilityCell(value string) string {
	return strings.TrimSpace(spacePattern.ReplaceAllString(value, " "))
}

func extractLegacyXLS(filePath string) (document ExtractedDocument, returnedErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			returnedErr = fmt.Errorf("Go BIFF 解析器异常: %v", recovered)
		}
	}()
	workbook, err := legacyxls.Open(filePath)
	if err != nil {
		return ExtractedDocument{}, fmt.Errorf("Go BIFF 解析失败: %w", err)
	}
	blocks := []Block{}
	dateEvidence := []DateEvidence{}
	warnings := []string{}
	workbookCellCount := 0
	formulaSeen := false
	formulaTotal, formulaCached, formulaDecoded, formulaDegraded, formulaEmpty, formulaStringLiteral := 0, 0, 0, 0, 0, 0
	for sheetIndex := 0; sheetIndex < workbook.SheetCount(); sheetIndex++ {
		sheet := workbook.Sheet(sheetIndex)
		if sheet == nil {
			continue
		}
		builder := newCompatibilitySheetBuilder(sheet.Name(), len(blocks))
		for rowIndex := 0; rowIndex < sheet.RowCount(); rowIndex++ {
			legacyRow := sheet.Row(rowIndex)
			if legacyRow == nil {
				continue
			}
			row := sheetRow{Number: rowIndex + 1}
			for column := 0; column < legacyRow.CellCount(); column++ {
				legacyCell := legacyRow.Cell(column)
				if legacyCell == nil {
					continue
				}
				value := cleanCompatibilityCell(legacyCell.Value())
				formula := ""
				if legacyCell.Type == legacyxls.CellTypeFormula {
					formulaSeen = true
					formulaTotal++
					formula = strings.TrimSpace(legacyCell.Formula())
					if value != "" {
						formulaCached++
					}
					if formula == "" {
						formulaEmpty++
					} else {
						formulaDecoded++
						upperFormula := strings.ToUpper(formula)
						if strings.Contains(upperFormula, "_UNK") || strings.Contains(upperFormula, "_NAME_") || strings.Contains(upperFormula, "_NAMEX_") || strings.Contains(upperFormula, "UNKNOWN") || strings.Contains(upperFormula, "BIFF_TOKEN_HEX") {
							formulaDegraded++
						}
						if strings.Contains(formula, "\"") {
							formulaStringLiteral++
						}
					}
				}
				if value == "" && formula == "" {
					continue
				}
				row.Cells = append(row.Cells, sheetCell{
					Address:     fmt.Sprintf("%s%d", encodeColumn(column), rowIndex+1),
					Column:      column,
					CachedValue: value,
					Formula:     formula,
					Text:        value,
				})
			}
			workbookCellCount += len(row.Cells)
			if workbookCellCount > compatibilityCellLimit {
				return ExtractedDocument{}, fmt.Errorf("工作簿超过 %d 个非空单元格安全上限", compatibilityCellLimit)
			}
			if err := builder.addRow(row, len(blocks)); err != nil {
				return ExtractedDocument{}, err
			}
		}
		builder.flush(len(blocks))
		blocks = append(blocks, builder.blocks...)
		dateEvidence = append(dateEvidence, builder.dateCollector.finish()...)
	}
	if formulaSeen {
		warnings = append(warnings,
			"旧版 BIFF 公式保留缓存值与可解码公式文本，不执行或重算公式",
			fmt.Sprintf("BIFF 公式质量: total=%d cached=%d uncached=%d decoded=%d degraded=%d empty=%d stringLiteral=%d", formulaTotal, formulaCached, formulaTotal-formulaCached, formulaDecoded, formulaDegraded, formulaEmpty, formulaStringLiteral),
		)
	}
	if len(blocks) == 0 {
		warnings = append(warnings, "工作簿未发现实际非空单元格")
	}
	info, _ := os.Stat(filePath)
	var bytesRead int64
	if info != nil {
		bytesRead = info.Size()
	}
	return ExtractedDocument{
		Title:        strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath)),
		Blocks:       blocks,
		Warnings:     warnings,
		BytesRead:    bytesRead,
		DateEvidence: reconcileWorkbookDateEvidence(dateEvidence),
	}, nil
}

func extractExcelizeCompatibility(filePath string) (ExtractedDocument, error) {
	workbook, err := excelize.OpenFile(filePath, excelize.Options{
		UnzipSizeLimit:    256 * 1024 * 1024,
		UnzipXMLSizeLimit: 32 * 1024 * 1024,
	})
	if err != nil {
		return ExtractedDocument{}, fmt.Errorf("Excelize OOXML compatibility 解析失败: %w", err)
	}
	defer workbook.Close()
	blocks := []Block{}
	dateEvidence := []DateEvidence{}
	workbookCellCount := 0
	for _, sheetName := range workbook.GetSheetList() {
		rows, err := workbook.Rows(sheetName)
		if err != nil {
			return ExtractedDocument{}, fmt.Errorf("Excelize 工作表 %q 读取失败: %w", sheetName, err)
		}
		builder := newCompatibilitySheetBuilder(sheetName, len(blocks))
		rowNumber := 0
		for rows.Next() {
			rowNumber++
			values, columnsErr := rows.Columns()
			if columnsErr != nil {
				_ = rows.Close()
				return ExtractedDocument{}, fmt.Errorf("Excelize 工作表 %q 第 %d 行读取失败: %w", sheetName, rowNumber, columnsErr)
			}
			row := sheetRow{Number: rowNumber}
			for column, formatted := range values {
				address, addressErr := excelize.CoordinatesToCellName(column+1, rowNumber)
				if addressErr != nil {
					continue
				}
				formula, _ := workbook.GetCellFormula(sheetName, address)
				raw, _ := workbook.GetCellValue(sheetName, address, excelize.Options{RawCellValue: true})
				formatted = cleanCompatibilityCell(formatted)
				formula = strings.TrimSpace(formula)
				if formatted == "" && formula == "" {
					continue
				}
				row.Cells = append(row.Cells, sheetCell{
					Address:     address,
					Column:      column,
					RawValue:    cleanCompatibilityCell(raw),
					CachedValue: formatted,
					Formula:     formula,
					Text:        formatted,
				})
			}
			workbookCellCount += len(row.Cells)
			if workbookCellCount > compatibilityCellLimit {
				_ = rows.Close()
				return ExtractedDocument{}, fmt.Errorf("工作簿超过 %d 个非空单元格安全上限", compatibilityCellLimit)
			}
			if err := builder.addRow(row, len(blocks)); err != nil {
				_ = rows.Close()
				return ExtractedDocument{}, err
			}
		}
		if err := rows.Error(); err != nil {
			_ = rows.Close()
			return ExtractedDocument{}, fmt.Errorf("Excelize 工作表 %q 迭代失败: %w", sheetName, err)
		}
		_ = rows.Close()
		builder.flush(len(blocks))
		blocks = append(blocks, builder.blocks...)
		dateEvidence = append(dateEvidence, builder.dateCollector.finish()...)
	}
	warnings := []string{"原生 OOXML 结构解析失败；已由纯 Go Excelize compatibility backend 恢复"}
	if strings.EqualFold(filepath.Ext(filePath), ".xlsm") {
		warnings = append(warnings, "工作簿可能包含 VBA 项目；索引器仅只读解析，不执行宏")
	}
	if len(blocks) == 0 {
		warnings = append(warnings, "工作簿未发现实际非空单元格")
	}
	info, _ := os.Stat(filePath)
	var bytesRead int64
	if info != nil {
		bytesRead = info.Size()
	}
	return ExtractedDocument{
		Title:        strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath)),
		Blocks:       blocks,
		Warnings:     warnings,
		BytesRead:    bytesRead,
		DateEvidence: reconcileWorkbookDateEvidence(dateEvidence),
	}, nil
}

func extractSpreadsheetCompatibility(filePath string) (ExtractedDocument, error) {
	if isOLECompoundFile(filePath) {
		return extractLegacyXLS(filePath)
	}
	return extractExcelizeCompatibility(filePath)
}
