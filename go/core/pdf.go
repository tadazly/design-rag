package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	pdf "github.com/giraffesyo/pdf"
)

var pdfDatePattern = regexp.MustCompile(`^(?:D:)?(\d{4})(\d{2})(\d{2})(?:(\d{2})(\d{2})(\d{2}))?(?:([Zz])|([+-])(\d{2})'?(\d{2})?'?)?$`)

func parsePDFDate(value string) *time.Time {
	match := pdfDatePattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) == 0 {
		return nil
	}
	parts := make([]int, 6)
	for index := range parts {
		if match[index+1] == "" {
			continue
		}
		parsed, err := strconv.Atoi(match[index+1])
		if err != nil {
			return nil
		}
		parts[index] = parsed
	}
	if parts[3] > 23 || parts[4] > 59 || parts[5] > 59 {
		return nil
	}
	location := time.UTC
	if len(match) > 8 && match[8] != "" {
		hours, _ := strconv.Atoi(match[9])
		minutes, _ := strconv.Atoi(match[10])
		if hours > 23 || minutes > 59 {
			return nil
		}
		offset := (hours*60 + minutes) * 60
		if match[8] == "-" {
			offset = -offset
		}
		location = time.FixedZone("PDF", offset)
	}
	parsed := time.Date(parts[0], time.Month(parts[1]), parts[2], parts[3], parts[4], parts[5], 0, location)
	if parsed.Year() != parts[0] || int(parsed.Month()) != parts[1] || parsed.Day() != parts[2] {
		return nil
	}
	valueUTC := parsed.UTC()
	return &valueUTC
}

func extractPDF(ctx context.Context, filePath string) (ExtractedDocument, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return ExtractedDocument{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ExtractedDocument{}, err
	}
	document, err := pdf.ExtractWithOptions(ctx, file, info.Size(), pdf.Options{
		IncludeMetadata: true,
		Concurrency:     0,
	})
	if err != nil {
		return ExtractedDocument{}, fmt.Errorf("Go PDF 解析失败: %w", err)
	}
	blocks := make([]Block, 0, len(document.Pages))
	imageOnlyPages := []int{}
	blankOrVectorPages := []int{}
	for pageIndex, page := range document.Pages {
		pageNumber := page.Number
		if pageNumber <= 0 {
			pageNumber = pageIndex + 1
		}
		text := strings.TrimSpace(page.Text())
		if text == "" {
			if page.ImageCount > 0 {
				imageOnlyPages = append(imageOnlyPages, pageNumber)
			} else {
				blankOrVectorPages = append(blankOrVectorPages, pageNumber)
			}
			continue
		}
		locator := fmt.Sprintf("第 %d 页", pageNumber)
		blocks = append(blocks, Block{
			Ordinal:     len(blocks),
			Text:        text,
			HeadingPath: []string{locator},
			SectionType: ClassifySection(nil, text),
			Locator:     locator,
		})
	}
	warnings := make([]string, 0, len(document.Warnings)+1)
	for _, warning := range document.Warnings {
		warnings = append(warnings, "PDF 兼容解析警告: "+warning.Error())
	}
	needsOCR := len(imageOnlyPages) > 0
	if len(imageOnlyPages) > 0 {
		warnings = append(warnings, fmt.Sprintf("PDF 图像页无文本层，需要 OCR：%v", imageOnlyPages))
	}
	if len(blankOrVectorPages) > 0 {
		warnings = append(warnings, fmt.Sprintf("PDF 页无可提取文本且未绘制图像（空白或矢量轮廓）：%v", blankOrVectorPages))
	}
	title := strings.TrimSpace(document.Metadata.Title)
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	}
	return ExtractedDocument{
		Title:              title,
		Blocks:             blocks,
		EmbeddedModifiedAt: parsePDFDate(document.Metadata.ModifiedDate),
		Warnings:           warnings,
		NeedsOCR:           needsOCR,
		BytesRead:          info.Size(),
	}, nil
}
