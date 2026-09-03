package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealCorpusDateParity is opt-in because the source corpus is not part of
// the repository. It covers the exact A/B outliers without making normal CI
// depend on a developer-specific drive layout.
func TestRealCorpusDateParity(t *testing.T) {
	root := os.Getenv("DRAG_DATE_PARITY_ROOT")
	if root == "" {
		t.Skip("set DRAG_DATE_PARITY_ROOT to run the read-only real-corpus date parity gate")
	}
	tests := []struct {
		relative string
		want     string
		source   string
	}{
		{"新版本规划.xlsx", "2027-01-27T00:00:00Z", "version_log"},
		{"每周新精灵信息汇总.xlsx", "2026-10-07T00:00:00Z", "version_log"},
		{"体验服规划.xlsx", "2024-11-27T00:00:00Z", "version_log"},
		{filepath.Join("文案内容", "剧情工作", "主线", "剧情规划.xlsx"), "2022-02-09T00:00:00Z", "version_log"},
		{"版本规划.xlsx", "2019-12-18T00:00:00Z", "version_log"},
		{"2024精灵冒烟测试表xlsx.xlsx", "2023-11-29T00:00:00Z", "version_log"},
		{"精灵冒烟测试表xlsx.xlsx", "2024-06-26T00:00:00Z", "version_log"},
		{filepath.Join("2022年度", "成就界面优化_designer-a.docx"), "2022-08-10T00:00:00Z", "version_log"},
		{filepath.Join("新手2.0", "ys_【废弃】【调整】超NO助手.docx"), "2023-05-23T00:00:00Z", "version_log"},
		{filepath.Join("2026年度", "2026.节日版本示例", "designer-a_便利计划-资源储备功能_202560204.docx"), "2026-02-04T00:00:00Z", "version_log"},
		{filepath.Join("2026年度", "2026.节日版本示例", "designer-a_便利计划-周常奖励补领_202560204.docx"), "2026-02-04T00:00:00Z", "version_log"},
		{filepath.Join("美术原画插图需求", "2025年度", "2025决战洪荒插图需求.xlsx"), "2025-12-16T03:41:57Z", "embedded_modified"},
		{filepath.Join("文案内容", "剧情工作", "主线", "异能星之战（中）.docx"), "2021-06-15T00:00:00Z", "version_log"},
		{filepath.Join("文案内容", "剧情工作", "主线", "异能星之战（上）.docx"), "2021-06-11T00:00:00Z", "version_log"},
	}
	for _, test := range tests {
		t.Run(test.relative, func(t *testing.T) {
			absolutePath := filepath.Join(root, test.relative)
			info, err := os.Stat(absolutePath)
			if err != nil {
				t.Fatal(err)
			}
			candidate := Candidate{
				SourceKind:        "design",
				AbsolutePath:      absolutePath,
				RelativePath:      test.relative,
				Extension:         strings.ToLower(filepath.Ext(absolutePath)),
				FilesystemMtimeMS: info.ModTime().UnixMilli(),
			}
			document, needsFallback, extractErr := extractByExtension(candidate)
			if extractErr != nil {
				t.Fatal(extractErr)
			}
			if needsFallback {
				t.Fatal("date parity fixture unexpectedly requires TypeScript fallback")
			}
			want, parseErr := time.Parse(time.RFC3339, test.want)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			got := ResolveEffectiveDate(candidate, document)
			if got.DateSource != test.source || got.EffectiveUpdatedAtMS != want.UnixMilli() {
				t.Fatalf("date = %#v, want %s from %s", got, test.want, test.source)
			}
		})
	}
}
