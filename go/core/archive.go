package core

import (
	"archive/zip"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"
)

const maxExpandedEntryBytes int64 = 768 * 1024 * 1024

var modifiedXMLPattern = regexp.MustCompile(`(?is)<([a-z0-9_-]+:)?modified[^>]*>\s*([^<]+?)\s*</([a-z0-9_-]+:)?modified>`)

func zipFile(reader *zip.ReadCloser, name string) *zip.File {
	wanted := strings.ToLower(strings.ReplaceAll(path.Clean(name), "\\", "/"))
	for _, file := range reader.File {
		actual := strings.ToLower(strings.ReplaceAll(path.Clean(file.Name), "\\", "/"))
		if actual == wanted {
			return file
		}
	}
	return nil
}

func readZipFile(file *zip.File, limit int64) ([]byte, error) {
	if file == nil {
		return nil, fmt.Errorf("archive entry 不存在")
	}
	if limit <= 0 || limit > maxExpandedEntryBytes {
		limit = maxExpandedEntryBytes
	}
	stream, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	data, err := io.ReadAll(io.LimitReader(stream, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("archive entry 解压后超过安全上限 %d bytes", limit)
	}
	return data, nil
}

func zipCoreModified(reader *zip.ReadCloser) *time.Time {
	data, err := readZipFile(zipFile(reader, "docProps/core.xml"), 4*1024*1024)
	if err != nil {
		return nil
	}
	match := modifiedXMLPattern.FindSubmatch(data)
	if len(match) < 3 {
		return nil
	}
	value := strings.TrimSpace(string(match[2]))
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if parsed, parseErr := time.Parse(layout, value); parseErr == nil {
			result := parsed.UTC()
			return &result
		}
	}
	return nil
}
