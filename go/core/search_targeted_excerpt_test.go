package core

import (
	"context"
	"fmt"
	"math"
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
	tables := []string{"costBundle", "prizeBundle", "taskConfig", "shopGoods", "statLog", "eventWindow", "rankReward", "mailTemplate"}
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

func TestGoTargetedRetrievePrefersChunksCoveringSeveralConcepts(t *testing.T) {
	// 路径含“版本”，每个分块都按路径命中该词；“星核探索背包”一节的短段落又都按标题命中“星核探索”，
	// 得分略高于正文同时写到“放弃探索”“玩法”“版本”的规则段落。
	paragraphs := []string{"# 晨星试炼", "## 星核探索背包"}
	for _, line := range []string{"背包上限 12 格。", "星核可叠加。", "每格显示数量。", "长按查看详情。", "拖动可整理。", "整理按品质排序。", "满格时提示。", "背包随试炼重置。", "图标沿用旧版。", "支持一键整理。", "新获得的星核标红。", "背包入口在右下角。"} {
		paragraphs = append(paragraphs, line)
	}
	paragraphs = append(paragraphs, "## 结算规则", "试炼失败时进入结算界面，已获得的奖励照常发放。", "## 界面提示", "放弃探索后本局星核不会触发结算，此玩法与旧版本相同。")
	service := newGoSearchService(t, []goSearchSource{{"plans", "design", map[string]string{"版本资料/晨星试炼_20260801.md": strings.Join(paragraphs, "\n\n") + "\n"}}})
	request := RetrievalRequest{SearchRequest: SearchRequest{Query: "星核探索 失败结算 放弃探索 其他版本玩法"}, MaxDocuments: 1, MaxChunksPerDocument: 4, MaxChars: 24_000}

	general, err := service.Search.Retrieve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(general.Search.Hits) == 0 {
		t.Fatalf("general retrieval found nothing: %#v", general)
	}
	excerpts := general.Search.Hits[0].Excerpts
	for index := 1; index < len(excerpts); index++ {
		if excerpts[index].Score > excerpts[index-1].Score {
			t.Fatalf("general retrieval must keep excerpts in score order: %v then %v", excerpts[index-1].Score, excerpts[index].Score)
		}
	}

	request.DocumentIDs = []string{general.Search.Hits[0].DocumentID}
	targeted, err := service.Search.Retrieve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(targeted.Search.Hits) == 0 {
		t.Fatalf("targeted retrieval found nothing: %#v", targeted)
	}
	hit := targeted.Search.Hits[0]
	best, rule, selected := 0.0, -1, []string{}
	for index, excerpt := range hit.Excerpts {
		best = math.Max(best, excerpt.Score)
		selected = append(selected, fmt.Sprintf("%.3f %s", excerpt.Score, excerpt.Text))
		if strings.Contains(excerpt.Text, "不会触发结算") {
			rule = index
		}
	}
	if rule < 0 {
		t.Fatalf("targeted retrieval must select the chunk covering several concepts:\n%s", strings.Join(selected, "\n"))
	}
	if hit.Excerpts[rule].Score >= best {
		t.Fatalf("bad fixture, the rule chunk already has the best score:\n%s", strings.Join(selected, "\n"))
	}
	if hit.Relevance != best {
		t.Fatalf("document relevance must stay the best chunk score: relevance=%v best=%v", hit.Relevance, best)
	}
	found := false
	for _, evidence := range targeted.Evidence {
		found = found || strings.Contains(evidence.Content, "不会触发结算")
	}
	if !found {
		t.Fatalf("the rule chunk must reach the evidence bundle: %#v", targeted.Evidence)
	}
}

func TestGoOwnTextBreadthCountsOnlyAdditionalConcepts(t *testing.T) {
	concepts := []queryConcept{
		{primary: "星核", weight: 0.5},
		{primary: "玩法", alternates: []string{"规则"}, weight: 0.3},
		{primary: "晨星888", weight: 0.2, identity: &documentIdentityGroup{Phrase: "晨星888", Terms: []string{"晨星", "888"}}},
	}
	for _, testCase := range []struct {
		text string
		want float64
	}{
		{"星核可叠加", 0},
		{"星核的玩法", 0.3},
		{"星核的规则", 0.15},
		{"晨星第888期的星核", 0.2},
		{"晨星的星核", 0},
		{"", 0},
	} {
		if got := ownTextBreadth(testCase.text, concepts); math.Abs(got-testCase.want) > 1e-9 {
			t.Fatalf("ownTextBreadth(%q) = %v, want %v", testCase.text, got, testCase.want)
		}
	}
}

func TestGoTargetedRetrieveKeepsClearlyLeadingChunk(t *testing.T) {
	// “玩法流程”一节的段落按标题命中两个概念，得分明显领先；“界面说明”一节的段落正文写到多个概念，
	// 覆盖广度更高但得分低得多。领先的段落不能被广度挤出分块名额。
	paragraphs := []string{"# 晨星试炼", "## 玩法流程", "进入试炼后先选择地图。", "## 界面说明"}
	for _, line := range []string{"界面一展示玩法入口、地图选择、战斗准备与结算奖励。", "界面二是玩法说明页，含地图介绍、战斗规则和结算规则。", "界面三汇总玩法记录：地图进度、战斗次数、结算评价。", "界面四提示玩法开放时间，并说明地图、战斗与结算的入口。", "界面五展示玩法排行，按地图、战斗和结算成绩分列。"} {
		paragraphs = append(paragraphs, line)
	}
	service := newGoSearchService(t, []goSearchSource{{"plans", "design", map[string]string{"晨星试炼_20260801.md": strings.Join(paragraphs, "\n\n") + "\n"}}})
	request := RetrievalRequest{SearchRequest: SearchRequest{Query: "晨星试炼 玩法流程 地图 战斗 结算"}, MaxDocuments: 1, MaxChunksPerDocument: 4, MaxChars: 24_000}
	general, err := service.Search.Retrieve(context.Background(), request)
	if err != nil || len(general.Search.Hits) == 0 {
		t.Fatalf("general retrieval failed: err=%v %#v", err, general)
	}
	request.DocumentIDs = []string{general.Search.Hits[0].DocumentID}
	targeted, err := service.Search.Retrieve(context.Background(), request)
	if err != nil || len(targeted.Search.Hits) == 0 {
		t.Fatalf("targeted retrieval failed: err=%v %#v", err, targeted)
	}
	selected, found := []string{}, false
	for _, excerpt := range targeted.Search.Hits[0].Excerpts {
		selected = append(selected, fmt.Sprintf("%.3f %s", excerpt.Score, excerpt.Text))
		found = found || strings.Contains(excerpt.Text, "先选择地图")
	}
	if !found {
		t.Fatalf("a clearly leading chunk must stay selected:\n%s", strings.Join(selected, "\n"))
	}
}

func TestGoTargetedExcerptOrderLocksLeadersAndBreaksNearTies(t *testing.T) {
	concepts := []queryConcept{{primary: "玩法", weight: 0.25}, {primary: "地图", weight: 0.25}, {primary: "战斗", weight: 0.25}, {primary: "结算", weight: 0.25}}
	title := "晨星试炼"
	candidate := func(ordinal int, score float64, text string) scoredCandidate {
		return scoredCandidate{row: LexicalCandidateRow{ChunkID: fmt.Sprintf("c%d", ordinal), Ordinal: ordinal, Title: title, RelativePath: title + ".md", Text: text}, score: score}
	}
	fieldsOf := normalizeCandidateFields
	top := func(ordered []scoredCandidate, limit int) map[int]bool {
		result := map[int]bool{}
		for _, item := range ordered[:min(limit, len(ordered))] {
			result[item.row.Ordinal] = true
		}
		return result
	}

	// 第 0 块比第 4 名高出 0.03 以上，正文只写到一个概念，仍按得分入选。
	leading := []scoredCandidate{candidate(0, 0.62, "先选择地图")}
	for ordinal := 1; ordinal <= 5; ordinal++ {
		leading = append(leading, candidate(ordinal, 0.49, "玩法 地图 战斗 结算"))
	}
	if !top(targetedExcerptOrder(leading, 4, concepts, fieldsOf), 4)[0] {
		t.Fatal("a chunk leading the boundary by more than the margin must stay selected")
	}

	// 标题带来的近似并列：正文写到多个概念、只低 0.006 的第 6 块应入选。
	tied := []scoredCandidate{}
	for ordinal := 0; ordinal <= 5; ordinal++ {
		tied = append(tied, candidate(ordinal, 0.396, "短段落"))
	}
	tied = append(tied, candidate(6, 0.390, "玩法 结算 战斗"))
	if !top(targetedExcerptOrder(tied, 4, concepts, fieldsOf), 4)[6] {
		t.Fatal("a near-tied chunk covering several concepts must win a slot")
	}

	// 文档标题已写到的概念不计入广度：标题是“晨星战斗”时，正文写到“战斗 结算”只算覆盖了一个方面，
	// 正文写到“玩法 结算”的第 7 块入选，第 6 块不入选。
	title = "晨星战斗"
	named := []scoredCandidate{}
	for ordinal := 0; ordinal <= 5; ordinal++ {
		named = append(named, candidate(ordinal, 0.396, "短段落"))
	}
	named = append(named, candidate(6, 0.390, "战斗 结算"), candidate(7, 0.390, "玩法 结算"))
	if selected := top(targetedExcerptOrder(named, 4, concepts, fieldsOf), 4); !selected[7] || selected[6] {
		t.Fatalf("concepts already in the document title must not count as breadth: %v", selected)
	}

	// 候选不超过名额时全部入选，顺序不变。
	few := tied[:3]
	if ordered := targetedExcerptOrder(few, 4, concepts, fieldsOf); len(ordered) != 3 || ordered[0].row.Ordinal != 0 || ordered[2].row.Ordinal != 2 {
		t.Fatalf("candidates within the limit must keep their order: %#v", ordered)
	}
}
