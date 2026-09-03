package core

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func isoDates(values []time.Time) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.Format("2006-01-02")
	}
	return result
}

func TestFindDatesMatchesTypeScriptMultipleDateRules(t *testing.T) {
	t.Parallel()
	input := "2026.01.02_2026.03.04 / 20260805_20260901 / 20260805"
	want := []string{"2026-01-02", "2026-03-04", "2026-08-05", "2026-09-01"}
	if got := isoDates(findDates(input)); !reflect.DeepEqual(got, want) {
		t.Fatalf("findDates(%q) = %#v, want %#v", input, got, want)
	}
}

func TestFindDatesMatchesTypeScriptRangeAndDateUTCNormalization(t *testing.T) {
	t.Parallel()
	futureYear := time.Now().UTC().Year() + 2
	input := fmt.Sprintf("19991231 20261301 20260832 %04d0101", futureYear)
	if got := findDates(input); len(got) != 0 {
		t.Fatalf("invalid or out-of-range dates were accepted: %#v", isoDates(got))
	}

	if got := findDates("20260100 20260231"); len(got) != 0 {
		t.Fatalf("calendar overflows must not be normalized into authoritative dates: %#v", isoDates(got))
	}
}

func TestResolveEffectiveDateUsesAuthoritativeSourcePriority(t *testing.T) {
	t.Parallel()
	document := ExtractedDocument{Blocks: []Block{{
		SectionType: "version_history",
		Text:        "更新 20261231 进入正式版本",
	}}}
	filename := Candidate{
		SourceKind:        "design",
		AbsolutePath:      filepath.Join("root", "20260901", "活动_20260819.xlsx"),
		FilesystemMtimeMS: 1,
	}
	if got := ResolveEffectiveDate(filename, document); got.DateSource != "filename" || got.EffectiveUpdatedAtMS != time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("filename date = %#v", got)
	}

	versionCandidate := Candidate{
		SourceKind:        "design",
		AbsolutePath:      filepath.Join("root", "20260901", "活动.xlsx"),
		FilesystemMtimeMS: 1,
	}
	if got := ResolveEffectiveDate(versionCandidate, document); got.DateSource != "version_log" || got.EffectiveUpdatedAtMS != time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("version date = %#v", got)
	}

	pathCandidate := Candidate{AbsolutePath: filepath.Join("root", "20260901", "活动.xlsx"), FilesystemMtimeMS: 1}
	if got := ResolveEffectiveDate(pathCandidate, ExtractedDocument{}); got.DateSource != "path" || got.EffectiveUpdatedAtMS != time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("path date = %#v", got)
	}
}

func TestResolveEffectiveDateFiltersVersionLogByRelevantLine(t *testing.T) {
	t.Parallel()
	candidate := Candidate{
		SourceKind:        "design",
		AbsolutePath:      filepath.Join("root", "activity.md"),
		FilesystemMtimeMS: 1,
	}
	document := ExtractedDocument{Blocks: []Block{
		{
			SectionType: "version_history",
			HeadingPath: []string{"版本修改记录"},
			Text:        "版本 20260102 初版\n更新 20260311_20260805 复用\n活动开放日期 20261231",
		},
		{
			SectionType: "gameplay",
			HeadingPath: []string{"玩法"},
			Text:        "版本 20270101 不属于版本日志块",
		},
	}}
	want := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC).UnixMilli()
	if got := ResolveEffectiveDate(candidate, document); got.DateSource != "version_log" || got.EffectiveUpdatedAtMS != want {
		t.Fatalf("version log date = %#v, want %d", got, want)
	}
}

func TestResolveEffectiveDateReadsSplitRevisionTableAndPrefersItOverCoverDate(t *testing.T) {
	t.Parallel()
	candidate := Candidate{SourceKind: "design", AbsolutePath: filepath.Join("root", "activity.docx"), FilesystemMtimeMS: 1}
	document := ExtractedDocument{Blocks: []Block{
		{SectionType: "version_history", Text: "Version：V1.00 20210630"},
		{SectionType: "version_history", Text: "修订号 | 修订日期 | 修订内容 | 修订人\n | 20210615 | 初稿 | Deathclock"},
	}}
	want := time.Date(2021, 6, 15, 0, 0, 0, 0, time.UTC).UnixMilli()
	if got := ResolveEffectiveDate(candidate, document); got.DateSource != "version_log" || got.EffectiveUpdatedAtMS != want {
		t.Fatalf("split revision table date = %#v, want %d", got, want)
	}
}

func TestResolveEffectiveDateTreatsCoverVersionAsWeakerThanIterationPath(t *testing.T) {
	t.Parallel()
	candidate := Candidate{SourceKind: "design", AbsolutePath: filepath.Join("root", "2020.08.19", "reuse.docx"), FilesystemMtimeMS: 1}
	document := ExtractedDocument{Blocks: []Block{{SectionType: "version_history", Text: "Version：V1.00 20200805"}}}
	want := time.Date(2020, 8, 19, 0, 0, 0, 0, time.UTC).UnixMilli()
	if got := ResolveEffectiveDate(candidate, document); got.DateSource != "path" || got.EffectiveUpdatedAtMS != want {
		t.Fatalf("cover/path priority = %#v, want %d from path", got, want)
	}
}

func TestResolveEffectiveDateKeepsRevisionTableStateAcrossBlocks(t *testing.T) {
	t.Parallel()
	candidate := Candidate{SourceKind: "design", AbsolutePath: filepath.Join("root", "activity.docx"), FilesystemMtimeMS: 1}
	document := ExtractedDocument{Blocks: []Block{
		{SectionType: "version_history", Text: "修订号 | 修订日期 | 修订内容\n | 20170620 | 初稿"},
		{SectionType: "version_history", HeadingPath: []string{"执行方案"}, Text: "面板沿用 2017年12月13日版本"},
	}}
	want := time.Date(2017, 12, 13, 0, 0, 0, 0, time.UTC).UnixMilli()
	if got := ResolveEffectiveDate(candidate, document); got.DateSource != "version_log" || got.EffectiveUpdatedAtMS != want {
		t.Fatalf("cross-block revision table = %#v, want %d", got, want)
	}
}

func TestResolveEffectiveDateScansLargeVersionTableBeyondFormerSampleLimit(t *testing.T) {
	t.Parallel()
	candidate := Candidate{SourceKind: "design", AbsolutePath: filepath.Join("root", "weekly.xlsx"), FilesystemMtimeMS: 1}
	document := ExtractedDocument{Blocks: []Block{{
		SectionType: "version_history",
		Text: "字段 | A=版本\n行 1 | A[版本]=20240528\n" +
			strings.Repeat("普通说明填充。\n", 20_000) +
			"行 1645 | A[版本]=20261007",
	}}}
	want := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC).UnixMilli()
	if got := ResolveEffectiveDate(candidate, document); got.DateSource != "version_log" || got.EffectiveUpdatedAtMS != want {
		t.Fatalf("large version table date = %#v, want %d", got, want)
	}
}

func TestResolveEffectiveDateConvertsExcelSerialsOnlyUnderDateHeader(t *testing.T) {
	t.Parallel()
	candidate := Candidate{SourceKind: "design", AbsolutePath: filepath.Join("root", "roadmap.xlsx"), FilesystemMtimeMS: 1}
	document := ExtractedDocument{Blocks: []Block{{
		SectionType: "version_history",
		Text:        "字段 | A=版本日期 | B=43887 | C=46414 | D=71275",
	}}}
	want := time.Date(2027, 1, 27, 0, 0, 0, 0, time.UTC).UnixMilli()
	if got := ResolveEffectiveDate(candidate, document); got.DateSource != "version_log" || got.EffectiveUpdatedAtMS != want {
		t.Fatalf("Excel serial version date = %#v, want %d", got, want)
	}
}

func TestResolveEffectiveDateRejectsVersionKeywordInOrdinaryRequirement(t *testing.T) {
	t.Parallel()
	embedded := time.Date(2025, 12, 16, 3, 41, 57, 0, time.UTC)
	candidate := Candidate{SourceKind: "design", AbsolutePath: filepath.Join("root", "art.xlsx"), FilesystemMtimeMS: 1}
	document := ExtractedDocument{
		Blocks: []Block{{
			SectionType: "version_history",
			Text:        "首次交付验收时间：2026.1.21，通过验收不晚于：2026.1.28，游戏内用于版本宣传或皮肤售卖。",
		}},
		EmbeddedModifiedAt: &embedded,
	}
	if got := ResolveEffectiveDate(candidate, document); got.DateSource != "embedded_modified" || got.EffectiveUpdatedAtMS != embedded.UnixMilli() {
		t.Fatalf("ordinary requirement must not become a version log date: %#v", got)
	}
}

func TestResolveEffectiveDateLeadingVersionDateGolden(t *testing.T) {
	t.Parallel()
	embedded := time.Date(2025, 12, 16, 3, 41, 57, 0, time.UTC)
	candidate := Candidate{SourceKind: "design", AbsolutePath: filepath.Join("root", "activity.md"), FilesystemMtimeMS: 1}
	tests := []struct {
		name       string
		text       string
		wantSource string
		wantMS     int64
	}{
		{
			name:       "date immediately follows version marker",
			text:       "版本 2026-08-20：调整奖励产出。",
			wantSource: "version_log",
			wantMS:     time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC).UnixMilli(),
		},
		{
			name:       "marketing phrase is not a version record",
			text:       "版本宣传排期预计于 2026-08-20 启动。",
			wantSource: "embedded_modified",
			wantMS:     embedded.UnixMilli(),
		},
		{
			name:       "invalid leading date is not rescued by later campaign date",
			text:       "版本 2026-02-31：宣传档期 2026-08-20。",
			wantSource: "embedded_modified",
			wantMS:     embedded.UnixMilli(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := ExtractedDocument{
				Blocks:             []Block{{SectionType: "version_history", Text: test.text}},
				EmbeddedModifiedAt: &embedded,
			}
			got := ResolveEffectiveDate(candidate, document)
			if got.DateSource != test.wantSource || got.EffectiveUpdatedAtMS != test.wantMS {
				t.Fatalf("date = %#v, want %s at %d", got, test.wantSource, test.wantMS)
			}
		})
	}
}
