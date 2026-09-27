package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestGoMakeExcerptWidensSpreadsheetWindowWithinBudget(t *testing.T) {
	letters := strings.Split("A B C D E F G H I J K L M N O P Q R S T", " ")
	header := []string{}
	for index, letter := range letters {
		header = append(header, fmt.Sprintf("%s=字段%d", letter, index+1))
	}
	lines := []string{"字段 | " + strings.Join(header, " | ")}
	for row := 1; row <= 30; row++ {
		cells := []string{}
		for _, letter := range letters {
			value := fmt.Sprintf("值%d", row)
			if letter == "P" && row == 12 {
				value = "目标词"
			}
			cells = append(cells, letter+"="+value)
		}
		lines = append(lines, fmt.Sprintf("行 %d | %s", row, strings.Join(cells, " | ")))
	}
	text := strings.Join(lines, "\n")
	countRows := func(value string) int {
		count := 0
		for _, line := range strings.Split(value, "\n") {
			if strings.HasPrefix(line, "行 ") {
				count++
			}
		}
		return count
	}

	compact, err := MakeExcerpt(text, "Sheet1!A1:T30", []string{"目标词"}, defaultExcerptLength)
	if err != nil {
		t.Fatal(err)
	}
	if rows := countRows(compact.Text); rows > 5 || !strings.Contains(compact.Text, "P=目标词") {
		t.Fatalf("default excerpt must keep the 5-row window around the hit: rows=%d\n%s", rows, compact.Text)
	}

	wide, err := MakeExcerpt(text, "Sheet1!A1:T30", []string{"目标词"}, 2400)
	if err != nil {
		t.Fatal(err)
	}
	if rows := countRows(wide.Text); rows <= 10 || utf16Length(wide.Text) > 2400 || !strings.Contains(wide.Text, "P=目标词") {
		t.Fatalf("wide excerpt must fill the budget around the hit: rows=%d length=%d\n%s", rows, utf16Length(wide.Text), wide.Text)
	}
	if wide.Scope == nil || len(wide.Scope.Columns) > maxProjectedColumns || indexOfString(wide.Scope.Columns, "P") < 0 {
		t.Fatalf("wide excerpt must keep the hit column within the citation column limit: %#v", wide.Scope)
	}
	rendered, err := renderSpreadsheetCitationScope(text, "Sheet1!A1:T30", *wide.Scope)
	if err != nil || rendered != wide.Text {
		t.Fatalf("wide excerpt must replay from its citation scope: err=%v\n%s\n---\n%s", err, rendered, wide.Text)
	}
}

func TestGoMakeExcerptWideWindowKeepsTheDefaultColumnsAndRows(t *testing.T) {
	letters := strings.Split("A B C D E F G H I J K L M N O P Q R S T", " ")
	rowsOf := func(value string) map[string]bool {
		result := map[string]bool{}
		for _, line := range strings.Split(value, "\n") {
			if strings.HasPrefix(line, "行 ") {
				result[strings.SplitN(line, " |", 2)[0]] = true
			}
		}
		return result
	}
	for _, testCase := range []struct {
		name  string
		cell  func(row, index int, letter string) string
		terms []string
		facts []string
	}{
		// 较远的第 8 行在 A–L 列命中另一个查询词，放宽窗口时这些列都会成为补列候选。
		{"distant hits in other columns", func(row, index int, letter string) string {
			switch {
			case row == 8 && index < 12:
				return "参考词"
			case row == 12 && letter == "P":
				return "目标词"
			}
			return "x"
		}, []string{"目标词", "参考词"}, []string{"P=目标词", "参考词"}},
		// D–I 列是很长的说明文字，补上它们会超出预算：只能不补，不能丢掉默认摘录已有的行。
		{"long extra columns", func(row, index int, letter string) string {
			switch {
			case row == 12 && letter == "P":
				return "目标词"
			case row == 11 && letter == "P":
				return "必需配表"
			case index >= 3 && index <= 8:
				return strings.Repeat("说明", 40)
			}
			return "x"
		}, []string{"目标词"}, []string{"P=目标词", "P=必需配表"}},
	} {
		header := []string{}
		for _, letter := range letters {
			header = append(header, letter+"=字段")
		}
		lines := []string{"字段 | " + strings.Join(header, " | ")}
		for row := 1; row <= 25; row++ {
			cells := []string{}
			for index, letter := range letters {
				cells = append(cells, letter+"="+testCase.cell(row, index, letter))
			}
			lines = append(lines, fmt.Sprintf("行 %d | %s", row, strings.Join(cells, " | ")))
		}
		text := strings.Join(lines, "\n")
		compact, err := MakeExcerpt(text, "Sheet1!A1:T25", testCase.terms, defaultExcerptLength)
		if err != nil {
			t.Fatal(err)
		}
		wide, err := MakeExcerpt(text, "Sheet1!A1:T25", testCase.terms, 2400)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(compact.Text, testCase.facts[0]) {
			t.Fatalf("%s: bad fixture, the default excerpt misses %s:\n%s", testCase.name, testCase.facts[0], compact.Text)
		}
		for _, fact := range testCase.facts {
			if !strings.Contains(wide.Text, fact) {
				t.Fatalf("%s: a larger budget must keep %s:\n%s\n---\n%s", testCase.name, fact, compact.Text, wide.Text)
			}
		}
		for _, column := range compact.Scope.Columns {
			if indexOfString(wide.Scope.Columns, column) < 0 {
				t.Fatalf("%s: wide columns %v must include the default columns %v", testCase.name, wide.Scope.Columns, compact.Scope.Columns)
			}
		}
		wideRows := rowsOf(wide.Text)
		for row := range rowsOf(compact.Text) {
			if !wideRows[row] {
				t.Fatalf("%s: wide rows must include the default row %s:\n%s", testCase.name, row, wide.Text)
			}
		}
		if utf16Length(wide.Text) > 2400 || len(wide.Scope.Columns) > maxProjectedColumns {
			t.Fatalf("%s: wide excerpt exceeds its budget: length=%d columns=%v", testCase.name, utf16Length(wide.Text), wide.Scope.Columns)
		}
		if rendered, err := renderSpreadsheetCitationScope(text, "Sheet1!A1:T25", *wide.Scope); err != nil || rendered != wide.Text {
			t.Fatalf("%s: wide excerpt must replay from its citation scope: err=%v", testCase.name, err)
		}
	}
}

func TestGoMakeExcerptWideWindowStopsAtTheBudget(t *testing.T) {
	lines := []string{"字段 | A=名称 | B=说明"}
	for row := 1; row <= 60; row++ {
		value := strings.Repeat("长说明", 16)
		if row == 30 {
			value = "目标词" + value
		}
		lines = append(lines, fmt.Sprintf("行 %d | A=名称%d | B=%s", row, row, value))
	}
	text := strings.Join(lines, "\n")
	compact, err := MakeExcerpt(text, "Sheet1!A1:B60", []string{"目标词"}, defaultExcerptLength)
	if err != nil {
		t.Fatal(err)
	}
	wide, err := MakeExcerpt(text, "Sheet1!A1:B60", []string{"目标词"}, 2400)
	if err != nil {
		t.Fatal(err)
	}
	// 60 行放不进 2400 字：行窗口要在预算处停下，仍包含默认摘录的 5 行，而不是超出后退回单行。
	rows := strings.Count(wide.Text, "\n行 ")
	if strings.Count(compact.Text, "\n行 ") != 5 || rows <= 5 || rows >= 60 || utf16Length(wide.Text) > 2400 || !strings.Contains(wide.Text, "行 28 |") || !strings.Contains(wide.Text, "行 32 |") {
		t.Fatalf("wide window must grow up to the budget: rows=%d length=%d\n%s", rows, utf16Length(wide.Text), wide.Text)
	}
}

func TestGoTargetedRetrieveReadsWholeSpreadsheetSection(t *testing.T) {
	tables := []string{"costPack", "rewardPool", "taskConfig", "shopItem", "statistic", "activityTime", "rankReward", "mailTemplate"}
	rows := []string{"模块,内容,备注", "概述,晨星挑战活动介绍,无", "玩法,每日挑战三次,无", "奖励,排行奖励,无", "配表实现,本次涉及以下配表,见下"}
	for _, table := range tables {
		rows = append(rows, "配表,"+table+" 配置,新增")
	}
	service := newGoSearchService(t, []goSearchSource{{"plans", "design", map[string]string{"晨星挑战_20260801.csv": strings.Join(rows, "\n") + "\n"}}})
	request := RetrievalRequest{SearchRequest: SearchRequest{Query: "晨星挑战 配表实现"}, MaxDocuments: 1, MaxChunksPerDocument: 4, MaxChars: 24_000}

	general, err := service.Search.Retrieve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(general.Evidence) == 0 || strings.Contains(general.Evidence[0].Content, "mailTemplate") {
		t.Fatalf("general retrieval must keep the default excerpt window: %#v", general.Evidence)
	}

	request.DocumentIDs = []string{general.Search.Hits[0].DocumentID}
	targeted, err := service.Search.Retrieve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(targeted.Evidence) == 0 {
		t.Fatalf("targeted retrieval returned no evidence: %#v", targeted)
	}
	for _, table := range tables {
		if !strings.Contains(targeted.Evidence[0].Content, table) {
			t.Fatalf("targeted retrieval must read the whole section, missing %s:\n%s", table, targeted.Evidence[0].Content)
		}
	}
	read, err := service.Search.ReadCitation(context.Background(), targeted.Evidence[0].CitationID, &targeted.IndexRevision)
	if err != nil || read.Content != targeted.Evidence[0].Content {
		t.Fatalf("targeted citation must replay: err=%v read=%q evidence=%q", err, read.Content, targeted.Evidence[0].Content)
	}

	request.MaxChars = 2_000
	request.MaxChunksPerDocument = 4
	small, err := service.Search.Retrieve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(small.Evidence) == 0 || utf16Length(small.Evidence[0].Content) > defaultExcerptLength {
		t.Fatalf("a small budget must keep default-length excerpts: %#v", small.Evidence)
	}
}
