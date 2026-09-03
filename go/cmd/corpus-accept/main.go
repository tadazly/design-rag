package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/tadazly/design-rag/go/core"
	"golang.org/x/text/unicode/norm"
)

const inventoryAlgorithm = "sha256-source-config-canonical-path-size-mtime-ms-v1"

type inventoryEntry struct {
	SourceID, SourceKind, CanonicalPath string
	SizeBytes, MtimeMS                  int64
}

type inventoryDescriptor struct {
	SourceID, SourceKind, CanonicalRoot string
	IncludeExtensions                   []string
	ExcludeDirectoryNames               []string
	MaxFileBytes                        int64
}

type sourceSummary struct {
	SourceID, SourceKind, CanonicalRoot string
	FileCount                           int
	TotalBytes                          int64
}

type inventory struct {
	Algorithm                   string          `json:"algorithm"`
	Fingerprint                 string          `json:"fingerprint"`
	FileCount                   int             `json:"fileCount"`
	SourceCount                 int             `json:"sourceCount"`
	TotalBytes                  int64           `json:"totalBytes"`
	CapturedAt                  string          `json:"capturedAt"`
	Sources                     []sourceSummary `json:"sources"`
	StableDuringRun             bool            `json:"stableDuringRun"`
	DiscoveredFileCount         int             `json:"discoveredFileCount"`
	MatchesDiscoveredCandidates bool            `json:"matchesDiscoveredCandidates"`
}

type failureAudit struct {
	Expected   int                     `json:"expected"`
	Actual     int                     `json:"actual"`
	Known      []core.IndexIssueRecord `json:"known"`
	Unexpected []core.IndexIssueRecord `json:"unexpected"`
	Missing    []string                `json:"missing"`
}

var knownCorpusFailures = map[string]string{
	"plans:文案内容/剧情工作/支线/伯莱恩.docx":                                  "zip: not a valid zip file",
	"plans:文案内容/剧情工作/主线/新建 microsoft word 文档.docx":                 "zip: not a valid zip file",
	"plans:文案内容/剧情工作/主线/2024/ikar/斯摩亚蒂_百世之仇篇/百世之仇篇剧情整理.xlsx":       "文档未提取到可索引内容",
	"plans:文案内容/剧情工作/主线/2024/ikar/威斯克_万古邪王篇/万古邪王篇剧情整理_截至0911.xlsx": "文档未提取到可索引内容",
	"plans:文案内容/剧情工作/主线/2024/ikar/天蛇太祖_天蛇之乱篇/天蛇之乱篇剧情整理.xlsx":       "文档未提取到可索引内容",
	"plans:文案内容/剧情工作/主线/2024/ikar/咤克斯_千年赫尔卡篇/千年赫尔卡篇.xlsx":          "文档未提取到可索引内容",
	"plans:文案内容/test.txt":         "文档未提取到可索引内容",
	"plans:太空站探索计划/铸魂塔/铸魂塔.xmind": "文档未提取到可索引内容",
	"plans:关卡设定稿/test.txt":        "文档未提取到可索引内容",
}

func auditFailures(issues []core.IndexIssueRecord, roots map[string]string) failureAudit {
	result := failureAudit{Expected: len(knownCorpusFailures), Actual: len(issues), Known: []core.IndexIssueRecord{}, Unexpected: []core.IndexIssueRecord{}, Missing: []string{}}
	found := map[string]bool{}
	for _, issue := range issues {
		root := roots[issue.SourceID]
		relative, err := filepath.Rel(root, issue.Path)
		if err != nil {
			result.Unexpected = append(result.Unexpected, issue)
			continue
		}
		key := strings.ToLower(norm.NFC.String(issue.SourceID + ":" + filepath.ToSlash(relative)))
		expectedMessage, ok := knownCorpusFailures[key]
		if !ok || issue.Code != "extract_failed" || !strings.Contains(strings.ToLower(issue.Message), strings.ToLower(expectedMessage)) {
			result.Unexpected = append(result.Unexpected, issue)
			continue
		}
		found[key] = true
		result.Known = append(result.Known, issue)
	}
	for key := range knownCorpusFailures {
		if !found[key] {
			result.Missing = append(result.Missing, key)
		}
	}
	sort.Strings(result.Missing)
	return result
}

func canonicalPath(value string) string {
	value, _ = filepath.Abs(value)
	value = norm.NFC.String(filepath.ToSlash(value))
	if runtime.GOOS == "windows" {
		value = strings.ToLower(value)
	}
	return value
}

func stable(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = norm.NFC.String(value)
	}
	sort.Strings(result)
	return result
}

func capture(ctx context.Context, sources []core.Source) (inventory, error) {
	discovery := core.DiscoverSources(ctx, core.ConfigForIndex(core.AppConfig{Sources: sources}).Sources)
	descriptors := []inventoryDescriptor{}
	entries := []inventoryEntry{}
	summaries := []sourceSummary{}
	for _, sourceResult := range discovery {
		if sourceResult.Err != nil {
			return inventory{}, sourceResult.Err
		}
		root := sourceResult.Source.RootPath
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
		descriptor := inventoryDescriptor{SourceID: sourceResult.Source.ID, SourceKind: sourceResult.Source.Kind, CanonicalRoot: canonicalPath(root), IncludeExtensions: sourceResult.Source.IncludeExtensions, ExcludeDirectoryNames: sourceResult.Source.ExcludeDirectoryNames, MaxFileBytes: sourceResult.Source.MaxFileBytes}
		descriptors = append(descriptors, descriptor)
		summary := sourceSummary{SourceID: descriptor.SourceID, SourceKind: descriptor.SourceKind, CanonicalRoot: descriptor.CanonicalRoot}
		for _, candidate := range sourceResult.Candidates {
			entry := inventoryEntry{SourceID: candidate.SourceID, SourceKind: candidate.SourceKind, CanonicalPath: canonicalPath(filepath.Join(root, candidate.RelativePath)), SizeBytes: candidate.SizeBytes, MtimeMS: candidate.FilesystemMtimeMS}
			entries = append(entries, entry)
			summary.FileCount++
			summary.TotalBytes += entry.SizeBytes
		}
		summaries = append(summaries, summary)
	}
	sort.Slice(descriptors, func(i, j int) bool {
		return descriptors[i].SourceID+"\x00"+descriptors[i].SourceKind+"\x00"+descriptors[i].CanonicalRoot < descriptors[j].SourceID+"\x00"+descriptors[j].SourceKind+"\x00"+descriptors[j].CanonicalRoot
	})
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].SourceID+"\x00"+entries[i].SourceKind+"\x00"+entries[i].CanonicalPath < entries[j].SourceID+"\x00"+entries[j].SourceKind+"\x00"+entries[j].CanonicalPath
	})
	hash := sha256.New()
	for _, descriptor := range descriptors {
		fmt.Fprintf(hash, "source\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\n", norm.NFC.String(descriptor.SourceID), descriptor.SourceKind, descriptor.CanonicalRoot, strings.Join(stable(descriptor.IncludeExtensions), ","), strings.Join(stable(descriptor.ExcludeDirectoryNames), ","), descriptor.MaxFileBytes)
	}
	var total int64
	for _, entry := range entries {
		fmt.Fprintf(hash, "file\x00%s\x00%s\x00%s\x00%d\x00%d\n", norm.NFC.String(entry.SourceID), entry.SourceKind, entry.CanonicalPath, entry.SizeBytes, entry.MtimeMS)
		total += entry.SizeBytes
	}
	return inventory{Algorithm: inventoryAlgorithm, Fingerprint: hex.EncodeToString(hash.Sum(nil)), FileCount: len(entries), SourceCount: len(descriptors), TotalBytes: total, CapturedAt: time.Now().UTC().Format(time.RFC3339Nano), Sources: summaries}, nil
}

func main() {
	rootFlag := flag.String("root", "", "new acceptance root")
	designRoot := flag.String("design-root", `D:\DesignRag\examples\design-docs`, "design source")
	tableRoot := flag.String("table-root", `D:\DesignRag\examples\config-tables`, "table source")
	concurrency := flag.Int("concurrency", 16, "index workers")
	expectedFormulaTotal := flag.Int64("expected-xls-formulas", 2447, "expected BIFF formula cells in the pinned corpus")
	expectedFormulaUncached := flag.Int64("expected-xls-uncached-formulas", 1154, "expected BIFF formulas without a non-whitespace cached value")
	expectedFormulaStrings := flag.Int64("expected-xls-string-formulas", 1437, "expected BIFF formulas containing string literals")
	expectedXLookup := flag.Int64("expected-xlookup", 1704, "minimum indexed XLOOKUP occurrences")
	expectedTextJoin := flag.Int64("expected-textjoin", 577, "minimum indexed TEXTJOIN occurrences")
	flag.Parse()
	if strings.TrimSpace(*rootFlag) == "" {
		*rootFlag = filepath.Join("tests", ".tmp", "go-plugin-corpus-"+time.Now().UTC().Format("20060102T150405Z"))
	}
	root, err := filepath.Abs(*rootFlag)
	if err != nil {
		fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		fatal(fmt.Errorf("验收根必须尚不存在：%s", root))
	}
	for _, path := range []string{*designRoot, *tableRoot} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			fatal(fmt.Errorf("资料源不可用：%s: %v", path, err))
		}
	}
	design, err := core.CreateSourceConfig("plans", "策划案", "design", *designRoot, true)
	if err != nil {
		fatal(err)
	}
	table, err := core.CreateSourceConfig("tables", "配表", "table", *tableRoot, true)
	if err != nil {
		fatal(err)
	}
	config := core.CreateDefaultConfig()
	config.Sources = []core.Source{design, table}
	config.Indexing.AutomaticScan = false
	config.Indexing.Concurrency = *concurrency
	ctx := context.Background()
	before, err := capture(ctx, config.Sources)
	if err != nil {
		fatal(err)
	}
	store := core.NewConfigStore(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if _, err := store.SaveSnapshot(config); err != nil {
		fatal(err)
	}
	service, err := core.NewRuntimeService(ctx, core.ServiceOptions{ConfigDir: store.ConfigDir, DataDir: store.DataDir})
	if err != nil {
		fatal(err)
	}
	defer service.Close()
	started := time.Now()
	run, runErr := service.Index(ctx, core.IndexOptions{Full: true}, func(summary core.RunSummary) {
		fmt.Fprintf(os.Stderr, "\r%-9s %d/%d indexed · %d unchanged · %d failed", summary.Phase, summary.Indexed, summary.Discovered, summary.Unchanged, summary.Failed)
	})
	fmt.Fprintln(os.Stderr)
	wallMS := time.Since(started).Milliseconds()
	after, captureErr := capture(ctx, config.Sources)
	if captureErr != nil {
		fatal(captureErr)
	}
	before.StableDuringRun = before.Fingerprint == after.Fingerprint && before.FileCount == after.FileCount && before.SourceCount == after.SourceCount
	before.DiscoveredFileCount = run.Discovered
	before.MatchesDiscoveredCandidates = before.FileCount == run.Discovered
	status, statusErr := service.Status()
	integrity, integrityErr := service.Database.IntegrityCheck()
	emptyIdentityCount, emptyIdentityErr := service.Database.EmptySourceIdentityCount()
	consistency, consistencyErr := service.Database.Consistency(ctx)
	formulaAudit, formulaAuditErr := service.Database.BIFFFormulaQuality(ctx)
	issues, issuesErr := service.Database.IssuesForRun(ctx, run.RunID)
	formatStats, formatStatsErr := service.Database.FormatStats()
	failures := auditFailures(issues, map[string]string{"plans": design.RootPath, "tables": table.RootPath})
	gates := map[string]bool{
		"fullIndexComplete":                  runErr == nil && run.Phase == "complete",
		"sourceInventoryStableDuringRun":     before.StableDuringRun,
		"sourceInventoryMatchesDiscovery":    before.MatchesDiscoveredCandidates,
		"sqliteIntegrity":                    integrityErr == nil && len(integrity) == 1 && integrity[0] == "ok",
		"noEmptySourceIdentity":              emptyIdentityErr == nil && emptyIdentityCount == 0,
		"documentsRemainQueryableWithIssues": statusErr == nil && status.DocumentCount > 0 && status.ChunkCount > 0,
		"noStaleOrDeletedDocuments":          statusErr == nil && status.StaleCount == 0 && consistency.DeletedDocuments == 0,
		"allDiscoveredFilesAccountedFor":     consistencyErr == nil && consistency.ActiveDocuments+int64(run.Failed) == int64(run.Discovered),
		"documentAndChunkRowsConsistent":     consistencyErr == nil && consistency.ActiveDocuments == int64(run.Indexed) && consistency.DeclaredChunks == consistency.ChunkRows && consistency.TermRows == consistency.ChunkRows && consistency.TrigramRows > 0 && consistency.OrphanChunks == 0 && consistency.OrphanTermRows == 0 && consistency.OrphanTrigramRows == 0,
		"knownFailureAllowlistExact":         issuesErr == nil && failures.Actual == failures.Expected && len(failures.Unexpected) == 0 && len(failures.Missing) == 0,
		"biffFormulaInventoryExact":          formulaAuditErr == nil && formulaAudit.Total == *expectedFormulaTotal && formulaAudit.Uncached == *expectedFormulaUncached && formulaAudit.StringLiteral == *expectedFormulaStrings,
		"biffFormulaDecodeComplete":          formulaAuditErr == nil && formulaAudit.Decoded == formulaAudit.Total && formulaAudit.Empty == 0 && formulaAudit.Degraded == 0 && formulaAudit.FormulaMarkers == formulaAudit.Total && formulaAudit.DegradedMarkerOccurrences == 0,
		"biffFutureFunctionsSearchable":      formulaAuditErr == nil && formulaAudit.XLookupOccurrences >= *expectedXLookup && formulaAudit.TextJoinOccurrences >= *expectedTextJoin,
		"formatStatsReadable":                formatStatsErr == nil && len(formatStats) > 0,
	}
	report := map[string]any{
		"schema": "drag_go_plugin_corpus_acceptance_v1", "createdAt": time.Now().UTC().Format(time.RFC3339Nano), "version": core.BackendVersion,
		"inputs":          map[string]any{"acceptanceRoot": root, "configPath": store.ConfigPath, "databasePath": service.Database.Path(), "designRoot": design.RootPath, "tableRoot": table.RootPath, "concurrency": *concurrency},
		"machine":         map[string]any{"go": runtime.Version(), "platform": runtime.GOOS, "arch": runtime.GOARCH},
		"sourceInventory": before, "sourceInventoryAfter": after, "gates": gates,
		"run": run, "runError": errorText(runErr), "externalWallMs": wallMS, "status": status, "statusError": errorText(statusErr), "integrity": integrity, "integrityError": errorText(integrityErr), "emptySourceIdentityCount": emptyIdentityCount, "emptySourceIdentityError": errorText(emptyIdentityErr),
		"consistency": consistency, "consistencyError": errorText(consistencyErr), "biffFormulaAudit": formulaAudit, "biffFormulaAuditError": errorText(formulaAuditErr), "failureAudit": failures, "issuesError": errorText(issuesErr), "formatStats": formatStats, "formatStatsError": errorText(formatStatsErr),
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		fatal(err)
	}
	reportPath := filepath.Join(root, "acceptance-report.json")
	raw, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(reportPath, append(raw, '\n'), 0o644); err != nil {
		fatal(err)
	}
	allPassed := true
	for _, passed := range gates {
		allPassed = allPassed && passed
	}
	result := map[string]any{"status": map[bool]string{true: "PASS", false: "FAIL"}[allPassed], "reportPath": reportPath, "gates": gates, "run": run, "externalWallMs": wallMS, "sourceInventory": before, "indexStatus": status}
	output, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(output))
	if !allPassed {
		os.Exit(1)
	}
}

func errorText(err error) any {
	if err == nil {
		return nil
	}
	return err.Error()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
