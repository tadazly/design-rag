package core

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type FallbackProvider interface {
	Extract(ctx context.Context, candidate Candidate, existingContentHash string, full bool) (*Draft, error)
}

type candidateSnapshot struct {
	path      string
	rawHash   string
	bytesRead int64
	source    *os.File
	before    os.FileInfo
}

func (snapshot *candidateSnapshot) close() {
	if snapshot.source != nil {
		_ = snapshot.source.Close()
	}
	if snapshot.path != "" {
		_ = os.Remove(snapshot.path)
	}
}

func openCandidateSnapshot(ctx context.Context, readPath, extension string, candidate Candidate) (*candidateSnapshot, error) {
	source, err := os.Open(readPath)
	if err != nil {
		return nil, err
	}
	fail := func(cause error) (*candidateSnapshot, error) {
		_ = source.Close()
		return nil, cause
	}
	before, err := source.Stat()
	if err != nil {
		return fail(err)
	}
	if !before.Mode().IsRegular() || before.Size() != candidate.SizeBytes || before.ModTime().UnixMilli() != candidate.FilesystemMtimeMS {
		return fail(fmt.Errorf("文件在发现后、读取前发生变化，请在下次增量扫描重试"))
	}
	temporary, err := os.CreateTemp("", "design-rag-snapshot-*"+extension)
	if err != nil {
		return fail(err)
	}
	temporaryPath := temporary.Name()
	cleanup := func(cause error) (*candidateSnapshot, error) {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
		_ = source.Close()
		return nil, cause
	}
	hash := sha256.New()
	buffer := make([]byte, 4*1024*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return cleanup(err)
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			_, _ = hash.Write(buffer[:count])
			if _, err := temporary.Write(buffer[:count]); err != nil {
				return cleanup(err)
			}
			total += int64(count)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return cleanup(readErr)
		}
	}
	if err := temporary.Close(); err != nil {
		return cleanup(err)
	}
	after, err := source.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime().UnixMilli() != after.ModTime().UnixMilli() {
		if err == nil {
			err = fmt.Errorf("文件在快照期间发生变化，请在下次增量扫描重试")
		}
		_ = os.Remove(temporaryPath)
		return fail(err)
	}
	return &candidateSnapshot{path: temporaryPath, rawHash: fmt.Sprintf("%x", hash.Sum(nil)), bytesRead: total, source: source, before: before}, nil
}

func verifySnapshotSource(readPath string, snapshot *candidateSnapshot) error {
	handleInfo, err := snapshot.source.Stat()
	if err != nil || !os.SameFile(snapshot.before, handleInfo) || snapshot.before.Size() != handleInfo.Size() || snapshot.before.ModTime().UnixMilli() != handleInfo.ModTime().UnixMilli() {
		return fmt.Errorf("文件在提取期间发生变化，请在下次增量扫描重试")
	}
	linkInfo, err := os.Lstat(readPath)
	if err != nil || linkInfo.Mode()&os.ModeSymlink != 0 || !linkInfo.Mode().IsRegular() {
		return fmt.Errorf("文件在提取期间发生身份变化，请在下次增量扫描重试")
	}
	if _, err := resolveSameFilePath(readPath, linkInfo); err != nil {
		return fmt.Errorf("文件在提取期间发生身份变化，请在下次增量扫描重试")
	}
	pathInfo, err := os.Stat(readPath)
	if err != nil || !os.SameFile(snapshot.before, pathInfo) {
		return fmt.Errorf("文件在提取期间发生身份变化，请在下次增量扫描重试")
	}
	return nil
}

func resolveSameFilePath(filePath string, expected os.FileInfo) (string, error) {
	resolved, err := filepath.EvalSymlinks(filePath)
	if err != nil {
		return "", err
	}
	resolvedInfo, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !os.SameFile(expected, resolvedInfo) {
		return "", fmt.Errorf("解析后的路径不再指向同一文件")
	}
	return resolved, nil
}

func extractByExtension(candidate Candidate) (ExtractedDocument, bool, error) {
	return extractByExtensionContext(context.Background(), candidate)
}

func extractByExtensionContext(ctx context.Context, candidate Candidate) (ExtractedDocument, bool, error) {
	switch candidate.Extension {
	case ".docx":
		document, err := extractDocx(candidate.AbsolutePath)
		return document, false, err
	case ".xlsx", ".xlsm":
		document, err := extractXlsx(candidate.AbsolutePath)
		if err == nil {
			return document, false, nil
		}
		if !isXlsxNativeError(err) {
			return document, false, err
		}
		compatibilityDocument, compatibilityErr := extractSpreadsheetCompatibility(candidate.AbsolutePath)
		if compatibilityErr != nil {
			return ExtractedDocument{}, true, fmt.Errorf("%v；纯 Go compatibility fallback 失败：%w", err, compatibilityErr)
		}
		compatibilityDocument.Warnings = append(compatibilityDocument.Warnings, err.Error())
		return compatibilityDocument, true, nil
	case ".xmind":
		document, err := extractXmind(candidate.AbsolutePath)
		return document, false, err
	case ".csv":
		document, err := extractCSV(candidate.AbsolutePath)
		return document, false, err
	case ".md", ".markdown", ".txt", ".html", ".htm", ".json", ".yaml", ".yml":
		document, err := extractText(candidate.AbsolutePath)
		return document, false, err
	case ".xls":
		// Content-sniff both OLE BIFF and OOXML ZIP workbooks. Some historical
		// files carry the wrong extension and the legacy Node reader accepted them.
		document, err := extractSpreadsheetCompatibility(candidate.AbsolutePath)
		return document, true, err
	case ".pdf":
		document, err := extractPDF(ctx, candidate.AbsolutePath)
		return document, true, err
	case ".doc":
		return ExtractedDocument{}, false, fmt.Errorf("旧版 OLE .doc 尚不支持；请先转换为 DOCX")
	default:
		return ExtractedDocument{}, false, fmt.Errorf("不支持的文档格式：%s", candidate.Extension)
	}
}

func semanticHash(chunks []Chunk) string {
	hash := sha256.New()
	for _, chunk := range chunks {
		_, _ = io.WriteString(hash, chunk.SectionType)
		_, _ = io.WriteString(hash, "\x00")
		_, _ = io.WriteString(hash, strings.Join(chunk.HeadingPath, "\x1f"))
		_, _ = io.WriteString(hash, "\x00")
		_, _ = io.WriteString(hash, chunk.Locator)
		_, _ = io.WriteString(hash, "\x00")
		_, _ = io.WriteString(hash, chunk.Text)
		_, _ = io.WriteString(hash, "\n")
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func goArchiveFormat(extension string) bool {
	return extension == ".docx" || extension == ".xlsx" || extension == ".xlsm" || extension == ".xmind"
}

func ProcessCandidate(ctx context.Context, candidate Candidate, existing *ExistingDocument, full bool, fallback FallbackProvider) (result TaskResult) {
	started := time.Now()
	defer func() { result.ElapsedMS = time.Since(started).Milliseconds() }()
	result = TaskResult{Candidate: candidate, Existing: existing}
	readPath := candidate.ReadPath
	if readPath == "" {
		readPath = candidate.AbsolutePath
	}
	linkInfo, err := os.Lstat(readPath)
	if err != nil || linkInfo.Mode()&os.ModeSymlink != 0 || !linkInfo.Mode().IsRegular() {
		message := "文件不是普通文件或在发现后变为 symlink"
		if err != nil {
			message = err.Error()
		}
		result.Issue = &Issue{SourceID: candidate.SourceID, Path: candidate.AbsolutePath, Code: "extract_failed", Message: message}
		return result
	}
	resolvedReadPath, err := resolveSameFilePath(readPath, linkInfo)
	if err != nil {
		result.Issue = &Issue{SourceID: candidate.SourceID, Path: candidate.AbsolutePath, Code: "extract_failed", Message: err.Error()}
		return result
	}
	resolvedRoot, err := filepath.EvalSymlinks(candidate.RootPath)
	if err != nil || !pathsOverlap(resolvedRoot, resolvedReadPath) {
		result.Issue = &Issue{SourceID: candidate.SourceID, Path: candidate.AbsolutePath, Code: "extract_failed", Message: "文件真实路径越出资料源边界"}
		return result
	}
	snapshot, err := openCandidateSnapshot(ctx, readPath, candidate.Extension, candidate)
	if err != nil {
		result.Issue = &Issue{SourceID: candidate.SourceID, Path: candidate.AbsolutePath, Code: "extract_failed", Message: err.Error()}
		return result
	}
	defer snapshot.close()
	result.BytesRead = snapshot.bytesRead
	hash := snapshot.rawHash
	if !goArchiveFormat(candidate.Extension) && !full && existing != nil && !existing.Stale && existing.ContentHash == hash {
		if err := verifySnapshotSource(readPath, snapshot); err != nil {
			result.Issue = &Issue{SourceID: candidate.SourceID, Path: candidate.AbsolutePath, Code: "extract_failed", Message: err.Error()}
			return result
		}
		result.Unchanged = true
		return result
	}
	_ = fallback // Kept in protocol v2 for compatibility; the Go runtime no longer requests host extraction.
	readCandidate := candidate
	readCandidate.AbsolutePath = snapshot.path
	document, compatibilityUsed, err := extractByExtensionContext(ctx, readCandidate)
	result.Fallback = compatibilityUsed
	if err != nil {
		result.Issue = &Issue{SourceID: candidate.SourceID, Path: candidate.AbsolutePath, Code: IssueCode(err), Message: err.Error()}
		return result
	}
	snapshotTitle := strings.TrimSuffix(filepath.Base(snapshot.path), filepath.Ext(snapshot.path))
	if strings.TrimSpace(document.Title) == "" || document.Title == snapshotTitle {
		document.Title = strings.TrimSuffix(filepath.Base(candidate.AbsolutePath), candidate.Extension)
	}
	result.BytesRead += document.BytesRead
	if err := verifySnapshotSource(readPath, snapshot); err != nil {
		result.Issue = &Issue{SourceID: candidate.SourceID, Path: candidate.AbsolutePath, Code: "extract_failed", Message: err.Error()}
		return result
	}
	chunks := ChunkBlocks(document.Blocks)
	if len(chunks) == 0 && !document.NeedsOCR {
		result.Issue = &Issue{SourceID: candidate.SourceID, Path: candidate.AbsolutePath, Code: "extract_failed", Message: "文档未提取到可索引内容"}
		return result
	}
	if goArchiveFormat(candidate.Extension) {
		hash = semanticHash(chunks)
		if !full && existing != nil && !existing.Stale && existing.ContentHash == hash {
			result.Unchanged = true
			return result
		}
	}
	familyKey, familyConfidence := MakeFamilyKey(document.Title)
	result.Draft = &Draft{
		ID:               "doc_" + HashString(CanonicalPathKey(candidate.AbsolutePath))[:24],
		Candidate:        candidate,
		Title:            document.Title,
		FamilyKey:        familyKey,
		FamilyConfidence: familyConfidence,
		ContentHash:      hash,
		Date:             ResolveEffectiveDate(candidate, document),
		Chunks:           chunks,
		Warnings:         document.Warnings,
		NeedsOCR:         document.NeedsOCR,
	}
	return result
}

func IssueCode(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(strings.ToLower(err.Error()), "不支持") {
		return "unsupported_format"
	}
	return "extract_failed"
}
