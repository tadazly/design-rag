package core

import (
	"archive/zip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

type xmindTopic struct {
	ID       string                  `json:"id"`
	Title    string                  `json:"title"`
	Notes    xmindNotes              `json:"notes"`
	Children map[string][]xmindTopic `json:"children"`
}

type xmindNotes struct {
	Plain struct {
		Content string `json:"content"`
	} `json:"plain"`
}

type xmindSheet struct {
	Title     string      `json:"title"`
	RootTopic *xmindTopic `json:"rootTopic"`
}

func walkXmindTopic(topic xmindTopic, sheet string, parents []string, blocks *[]Block) {
	title := strings.TrimSpace(topic.Title)
	if title == "" {
		title = "未命名主题"
	}
	headings := append(append([]string(nil), parents...), title)
	note := strings.TrimSpace(topic.Notes.Plain.Content)
	text := title
	if note != "" {
		text += "\n" + note
	}
	*blocks = append(*blocks, Block{
		Ordinal:     len(*blocks),
		Text:        text,
		HeadingPath: headings,
		SectionType: ClassifySection(headings, text),
		Locator:     fmt.Sprintf("XMind/%s/%s", sheet, strings.Join(headings, "/")),
	})
	for _, children := range topic.Children {
		for _, child := range children {
			walkXmindTopic(child, sheet, headings, blocks)
		}
	}
}

func extractXmind(filePath string) (ExtractedDocument, error) {
	archive, err := zip.OpenReader(filePath)
	if err != nil {
		return ExtractedDocument{}, err
	}
	defer archive.Close()
	blocks := []Block{}
	var bytesRead int64
	if contentJSON := zipFile(archive, "content.json"); contentJSON != nil {
		bytesRead += int64(contentJSON.CompressedSize64)
		data, readErr := readZipFile(contentJSON, maxExpandedEntryBytes)
		if readErr != nil {
			return ExtractedDocument{}, readErr
		}
		var sheets []xmindSheet
		if decodeErr := json.Unmarshal(data, &sheets); decodeErr != nil {
			return ExtractedDocument{}, decodeErr
		}
		for _, sheet := range sheets {
			if sheet.RootTopic != nil {
				name := strings.TrimSpace(sheet.Title)
				if name == "" {
					name = "工作表"
				}
				walkXmindTopic(*sheet.RootTopic, name, nil, &blocks)
			}
		}
	} else if contentXML := zipFile(archive, "content.xml"); contentXML != nil {
		bytesRead += int64(contentXML.CompressedSize64)
		stream, openErr := contentXML.Open()
		if openErr != nil {
			return ExtractedDocument{}, openErr
		}
		decoder := xml.NewDecoder(io.LimitReader(stream, maxExpandedEntryBytes))
		for {
			token, tokenErr := decoder.Token()
			if tokenErr == io.EOF {
				break
			}
			if tokenErr != nil {
				stream.Close()
				return ExtractedDocument{}, tokenErr
			}
			start, ok := token.(xml.StartElement)
			if !ok || !strings.EqualFold(start.Name.Local, "title") {
				continue
			}
			var title string
			if decodeErr := decoder.DecodeElement(&title, &start); decodeErr != nil {
				stream.Close()
				return ExtractedDocument{}, decodeErr
			}
			title = strings.TrimSpace(title)
			if title != "" {
				blocks = append(blocks, Block{Ordinal: len(blocks), Text: title, HeadingPath: []string{title}, SectionType: ClassifySection([]string{title}, title), Locator: fmt.Sprintf("XMind 主题 %d", len(blocks)+1)})
			}
		}
		stream.Close()
	}
	warnings := []string{}
	if len(blocks) == 0 {
		warnings = append(warnings, "XMind 未提取到主题")
	}
	return ExtractedDocument{Title: strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath)), Blocks: blocks, Warnings: warnings, BytesRead: bytesRead}, nil
}
