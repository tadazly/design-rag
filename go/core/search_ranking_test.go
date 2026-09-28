package core

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestQueryKeywordTermsDropsQuestionWordsAndSegmentsDomainTerms(t *testing.T) {
	for query, want := range map[string][]string{
		"我要新增一个扭蛋机，需要配置哪些表格":           {"扭蛋机", "配置"},
		"我想复用轮盘抽奖活动，有哪些可以复用":           {"复用", "轮盘", "抽奖", "活动"},
		"星河龙888 产出逻辑":                  {"星河龙888", "产出"},
		"alphaLottery betaPool 配置":     {"alphalottery", "betapool", "配置"},
		"晨星守望者的精灵ID是多少":                {"晨星守望者", "精灵"},
		"找到最新的一个 888活动，说明一下里面的玩法和产出逻辑": {"888", "活动", "玩法", "产出"},
	} {
		if got := QueryKeywordTerms(NormalizeText(query)); !reflect.DeepEqual(got, want) {
			t.Errorf("%s keywords=%q want %q", query, got, want)
		}
	}
}

func TestIdentityGroupPrefersStandaloneEntityName(t *testing.T) {
	group := documentIdentityGroup{Phrase: "晨星888", Terms: []string{"晨星", "888"}}
	standalone := "plan_【预开发】晨星·守望者888活动_20251119"
	embedded := "plan_【复用】破晨星·裂空888活动_20260506"
	if !identityGroupAtBoundary(standalone, group) || identityGroupAtBoundary(embedded, group) {
		t.Fatal("identity boundary must distinguish a standalone name from a longer name")
	}
	if identityGroupScore(embedded, group) <= 0 || identityGroupScore(standalone, group) <= identityGroupScore(embedded, group) {
		t.Fatalf("standalone=%v embedded=%v", identityGroupScore(standalone, group), identityGroupScore(embedded, group))
	}
	// 用户只写名称后半段时，嵌在完整名称里的命中仍然有效。
	short := documentIdentityGroup{Phrase: "守望者888", Terms: []string{"守望者", "888"}}
	if identityGroupScore("plan_晨星守望者888活动", short) <= 0 {
		t.Fatal("embedded identity must still match when no standalone name exists")
	}
}

func TestFieldMatchStrengthRanksTableStructureAboveBodyMentions(t *testing.T) {
	header := "字段映射(投影) | a=id | b=name | c=页签名"
	for _, test := range []struct {
		name   string
		fields normalizedCandidateFields
		table  bool
		want   float64
	}{
		{"title equals", normalizedCandidateFields{title: "扭蛋机"}, false, 1},
		{"title contains", normalizedCandidateFields{title: "扭蛋机活动_20260812"}, false, 0.85},
		{"name column cell", normalizedCandidateFields{title: "pet", relativePath: `special\pet.xlsx`, text: header + " 行 2 | a=11 | b=扭蛋机"}, true, 0.85},
		{"table system directory", normalizedCandidateFields{title: "alphalottery", relativePath: `系统配表\扭蛋机\alphalottery.xlsx`, text: header + " 行 2 | a=1 | b=普通"}, true, 0.8},
		{"other exact cell", normalizedCandidateFields{title: "questsummary", relativePath: `客户端\questsummary.xlsx`, text: header + " 行 2 | a=1 | c=扭蛋机"}, true, 0.75},
		{"design directory", normalizedCandidateFields{title: "设定", relativePath: `扭蛋机\设定.docx`}, false, 0.65},
		{"body mention", normalizedCandidateFields{title: "questsummary", relativePath: `客户端\questsummary.xlsx`, text: header + " 行 2 | a=1 | c=扭蛋机返场活动"}, true, 0.3},
		{"missing", normalizedCandidateFields{title: "questsummary", text: header}, true, 0},
	} {
		if got := fieldMatchStrength(test.fields, "扭蛋机", test.table); got != test.want {
			t.Errorf("%s strength=%v want %v", test.name, got, test.want)
		}
	}
}

func TestConceptCandidatesRankRareConceptBeforeNewerCommonMatches(t *testing.T) {
	files := map[string]string{"扭蛋机活动_20250101.md": "# 配表实现\n\n扭蛋机的抽奖表配置。"}
	for index := 1; index <= 9; index++ {
		files[fmt.Sprintf("常规配置%d_2026090%d.md", index, index)] = fmt.Sprintf("# 配置\n\n常规活动%d的配置说明。", index)
	}
	service := newGoSearchService(t, []goSearchSource{{"plans", "design", files}})
	ctx := context.Background()
	concepts, _ := buildQueryConcepts("扭蛋机 配置", true)
	revision, err := service.Database.Revision()
	if err != nil {
		t.Fatal(err)
	}
	matches, err := service.Search.conceptFTSMatches(ctx, revision, concepts)
	if err != nil {
		t.Fatal(err)
	}
	if len(concepts) != 2 || concepts[0].primary != "扭蛋机" || concepts[0].weight <= concepts[1].weight {
		t.Fatalf("rare concept must carry more weight: %#v", concepts)
	}
	rows, err := service.Database.ConceptCandidates(ctx, matches, 3, 8, CandidateSourceFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Title != "扭蛋机活动_20250101" {
		t.Fatalf("older rare-concept document must survive the candidate cutoff: %#v", rows)
	}
}

func TestLexicalCandidatesKeepNewestFirstAcrossTwoPhaseFetch(t *testing.T) {
	files := map[string]string{}
	for index := 1; index <= 9; index++ {
		files[fmt.Sprintf("配置%d_2026080%d.md", index, index)] = fmt.Sprintf("# 配置\n\n轮盘%d配置。", index)
	}
	service := newGoSearchService(t, []goSearchSource{{"plans", "design", files}})
	rows, err := service.Database.LexicalCandidates(context.Background(), EscapeFTSToken("轮盘"), 5, CandidateSourceFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 || rows[0].Title != "配置9_20260809" {
		t.Fatalf("rows=%#v", rows)
	}
	for index := 1; index < len(rows); index++ {
		if rows[index-1].EffectiveUpdatedAtMS < rows[index].EffectiveUpdatedAtMS {
			t.Fatalf("two-phase fetch must keep the ranked order: %s before %s", rows[index-1].Title, rows[index].Title)
		}
	}
}

func TestGoSearchIdentityKeepsLongerNamesButRanksAndFlagsThem(t *testing.T) {
	service := newGoSearchService(t, []goSearchSource{
		{"plans", "design", map[string]string{
			"晨星·守望者888活动_20251119.md":     "# 玩法\n\n晨星·守望者888活动的玩法与产出。",
			"【复用】破晨星·裂空888活动_20260506.md": "# 玩法\n\n复用破晨星·裂空888活动的玩法与产出。",
			"破晨星·裂空888活动_20251224.md":     "# 玩法\n\n破晨星·裂空888活动首版的玩法与产出。",
			"【复用】星河龙888活动_20260901.md":    "# 配表\n\n复用活动需要配置的表。",
		}},
		{"tables", "table", map[string]string{"eventWindow_20260901.md": "# 时间\n\n晨星·守望者888活动开放时间。"}},
	})
	ctx := context.Background()
	titles := func(hits []SearchHit) map[string]SearchHit {
		result := map[string]SearchHit{}
		for _, hit := range hits {
			result[hit.Title] = hit
		}
		return result
	}
	hasAmbiguityWarning := func(warnings []string) bool {
		for _, warning := range warnings {
			// 提示要说明用户写法按字面对应独立名称、默认以它为主回答，并列出更长名称的活动。
			if strings.Contains(warning, "按字面对应独立名称《晨星·守望者888活动_20251119》") && strings.Contains(warning, "《【复用】破晨星·裂空888活动_20260506》") && strings.Contains(warning, "以独立名称的活动为主回答") {
				return true
			}
		}
		return false
	}
	result, err := service.Search.Search(ctx, SearchRequest{Query: "晨星888活动的玩法", Sort: "newest", Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	hits := titles(result.Hits)
	standalone, okStandalone := hits["晨星·守望者888活动_20251119"]
	embedded, okEmbedded := hits["【复用】破晨星·裂空888活动_20260506"]
	if !okStandalone || !okEmbedded {
		t.Fatalf("both the standalone name and the longer name must stay in the results: %v", hits)
	}
	if standalone.Relevance <= embedded.Relevance {
		t.Fatalf("standalone relevance %.3f must exceed longer-name relevance %.3f", standalone.Relevance, embedded.Relevance)
	}
	if !hasAmbiguityWarning(result.Warnings) {
		t.Fatalf("search must flag the ambiguous activity identity: %q", result.Warnings)
	}
	tableIntent, err := service.Search.Search(ctx, SearchRequest{Query: "复用晨星888，需要配哪些表", Sort: "newest", Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	foundTable := false
	for _, hit := range tableIntent.Hits {
		if hit.Title == "【复用】星河龙888活动_20260901" {
			t.Fatal("table-intent search must keep design hits on the requested activity identity")
		}
		foundTable = foundTable || hit.SourceKind == "table"
	}
	if !foundTable {
		t.Fatalf("table-intent search must not filter tables by activity identity: %#v", tableIntent.Hits)
	}
	if _, ok := titles(tableIntent.Hits)["【复用】破晨星·裂空888活动_20260506"]; !ok {
		t.Fatalf("table-intent search must keep the longer-name reuse plan as a candidate: %#v", tableIntent.Hits)
	}
	// 更长名称的两份策划都比独立名称的策划新；即使策划名额只有 2 个，独立名称的策划和更长名称里最新的策划
	// 也都须进入证据包，覆盖两种理解。
	for query, maxDocuments := range map[string]int{"复用晨星888": 2, "我要复用晨星888，需要配哪些表": 8} {
		bundle, err := service.Search.Retrieve(ctx, RetrievalRequest{SearchRequest: SearchRequest{Query: query}, MaxDocuments: maxDocuments})
		if err != nil {
			t.Fatal(err)
		}
		selected := titles(bundle.Search.Hits)
		if _, ok := selected["晨星·守望者888活动_20251119"]; !ok {
			t.Fatalf("%s: retrieve must keep the standalone activity plan: %#v", query, bundle.Search.Hits)
		}
		if _, ok := selected["【复用】破晨星·裂空888活动_20260506"]; !ok {
			t.Fatalf("%s: retrieve must also carry the newest longer-name plan: %#v", query, bundle.Search.Hits)
		}
		if !hasAmbiguityWarning(bundle.Search.Warnings) {
			t.Fatalf("%s: retrieve must carry the ambiguity warning: %q", query, bundle.Search.Warnings)
		}
	}
	// 配表优先的小证据包里没有两份策划的名额，不能为了覆盖两种理解挤掉配表。
	small, err := service.Search.Retrieve(ctx, RetrievalRequest{SearchRequest: SearchRequest{Query: "我要复用晨星888，需要配哪些表"}, MaxDocuments: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := titles(small.Search.Hits)["eventWindow_20260901"]; !ok {
		t.Fatalf("a small table-first bundle must keep its table evidence: %#v", small.Search.Hits)
	}
}

func TestGoIdentityAdmitsReruns(t *testing.T) {
	service := newGoSearchService(t, []goSearchSource{
		{"plans", "design", map[string]string{
			"晨星·守望者888活动_20251119.md":     "# 玩法\n\n晨星·守望者888活动的玩法与产出。",
			"晨星守望者限时返场_20260603.md":       "# 概述\n\n晨星守望者888活动返场相关界面调整和奖励调整。",
			"晨星推送礼包_20260722.md":          "# 礼包\n\n晨星推送礼包888元档位配置。",
			"晨星新活动_20260701.md":           "# 概述\n\n玩法参考：晨星守望者888活动，新增星辰挑战。",
			"晨星守望者周年庆_20260901.md":        "# 概述\n\n玩法参考：晨星守望者888活动；本活动为独立新玩法，不是其返场或复用。",
			"【复用】破晨星·裂空888活动_20260506.md": "# 玩法\n\n复用破晨星·裂空888活动的玩法与产出。",
		}},
		{"tables", "table", map[string]string{"eventWindow_20260901.md": "# 时间\n\n晨星·守望者888活动开放时间。"}},
	})
	ctx := context.Background()
	titles := func(hits []SearchHit) map[string]bool {
		result := map[string]bool{}
		for _, hit := range hits {
			result[hit.Title] = true
		}
		return result
	}
	// 返场稿标题省略编号，正文写明与身份策划标题相同的完整活动名；礼包正文的“888”只是档位，活动名也不同。
	result, err := service.Search.Search(ctx, SearchRequest{Query: "复用晨星888", Sort: "newest", Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	hits := titles(result.Hits)
	if !hits["晨星守望者限时返场_20260603"] {
		t.Fatalf("search must admit the rerun plan that names the activity in its body: %v", hits)
	}
	if hits["晨星推送礼包_20260722"] {
		t.Fatalf("a different name followed by a price tier must not pass the identity gate: %v", hits)
	}
	// 新活动标题只写实体名、正文只是引用该活动，不能被当作它的返场稿。
	if hits["晨星新活动_20260701"] {
		t.Fatalf("a new activity that only references the activity in its body must not pass the identity gate: %v", hits)
	}
	// 标题写出完整名称、正文只是参考该活动的文档保留为普通候选，但不算返场稿：没有提示，也不占返场稿名额。
	if !hits["晨星守望者周年庆_20260901"] {
		t.Fatalf("an activity that names the full activity in its title and body must stay an ordinary candidate: %v", hits)
	}
	// 只看标题无法把返场稿和原案联系起来：结果须附正文原文提示它可能属于哪个活动，而不是直接断言。
	hasRerunNote := func(warnings []string) bool {
		found := false
		for _, warning := range warnings {
			if strings.Contains(warning, "晨星新活动") || strings.Contains(warning, "周年庆") {
				return false
			}
			found = found || strings.Contains(warning, "《晨星守望者限时返场_20260603》") && strings.Contains(warning, "晨星守望者888活动返场") &&
				strings.Contains(warning, "可能是《晨星·守望者888活动_20251119》所属活动的返场或复用稿")
		}
		return found
	}
	if !hasRerunNote(result.Warnings) {
		t.Fatalf("search must quote the body text and name the likely activity of the rerun plan: %q", result.Warnings)
	}
	// 策划名额按“独立名称策划 → 同一活动的返场稿 → 更长名称的活动”依次分配。
	for _, testCase := range []struct {
		query        string
		maxDocuments int
		want         []string
		absent       []string
	}{
		{"复用晨星888", 2, []string{"晨星·守望者888活动_20251119", "晨星守望者限时返场_20260603"}, []string{"【复用】破晨星·裂空888活动_20260506", "晨星守望者周年庆_20260901"}},
		{"复用晨星888", 3, []string{"晨星·守望者888活动_20251119", "晨星守望者限时返场_20260603", "【复用】破晨星·裂空888活动_20260506"}, nil},
		{"我要复用晨星888，需要配哪些表", 8, []string{"晨星·守望者888活动_20251119", "晨星守望者限时返场_20260603", "eventWindow_20260901"}, nil},
	} {
		bundle, err := service.Search.Retrieve(ctx, RetrievalRequest{SearchRequest: SearchRequest{Query: testCase.query}, MaxDocuments: testCase.maxDocuments})
		if err != nil {
			t.Fatal(err)
		}
		selected := titles(bundle.Search.Hits)
		for _, title := range testCase.want {
			if !selected[title] {
				t.Fatalf("%s (max %d): missing %s in %v", testCase.query, testCase.maxDocuments, title, selected)
			}
		}
		for _, title := range testCase.absent {
			if selected[title] {
				t.Fatalf("%s (max %d): %s must yield its slot to the rerun plan: %v", testCase.query, testCase.maxDocuments, title, selected)
			}
		}
		if !hasRerunNote(bundle.Search.Warnings) {
			t.Fatalf("%s (max %d): retrieve must carry the rerun note: %q", testCase.query, testCase.maxDocuments, bundle.Search.Warnings)
		}
	}
}

func TestGoIdentityRerunSlotGoesToNewestDeclaredRerun(t *testing.T) {
	tables := map[string]string{}
	for index := 1; index <= 4; index++ {
		tables[fmt.Sprintf("activityConfig%d_2026090%d.md", index, index)] = fmt.Sprintf("# 配置\n\n晨星·守望者888活动第%d项配置。", index)
	}
	service := newGoSearchService(t, []goSearchSource{
		{"plans", "design", map[string]string{
			"晨星·守望者888活动_20251119.md":     "# 玩法\n\n晨星·守望者888活动的玩法与产出。",
			"晨星守望者限时返场_20260603.md":       "# 概述\n\n晨星守望者888活动返场相关界面调整和奖励调整。",
			"晨星守望者六月调整_20260705.md":       "# 概述\n\n本次为晨星守望者888活动返场。原玩法完全不变，仅调整上架日期。",
			"晨星守望者复刻_20260805.md":         "# 概述\n\n晨星守望者888活动复刻，沿用返场配置并调整奖励。",
			"晨星守望者周年庆复刻_20260910.md":      "# 概述\n\n本文复刻晨星守望者周年庆活动。玩法参考：晨星守望者888活动；本活动是周年庆复刻，不是888的返场或复用。",
			"晨星守望者新春版_20260915.md":        "# 概述\n\n本活动不是晨星守望者888活动返场，玩法全新。",
			"【复用】破晨星·裂空888活动_20260506.md": "# 玩法\n\n复用破晨星·裂空888活动的玩法与产出。",
		}},
		{"tables", "table", tables},
	})
	ctx := context.Background()
	result, err := service.Search.Search(ctx, SearchRequest{Query: "复用晨星888", Sort: "newest", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	declared, mentioned := map[string]bool{}, map[string]bool{}
	for _, hit := range result.Hits {
		declared[hit.Title] = hit.declaredRerun
		mentioned[hit.Title] = hit.bodyIdentity
	}
	// 正文声明本文档是该活动的返场、复刻，且标题也写明返场或复刻的，才是返场稿；
	// 只有正文声明（《六月调整》）、标题写“复刻”但复刻的是周年庆，或正文否定是返场的文档，保留为普通候选。
	for _, title := range []string{"晨星守望者限时返场_20260603", "晨星守望者复刻_20260805"} {
		if !declared[title] {
			t.Fatalf("%s must be a declared rerun: %v", title, hitTitles(result.Hits))
		}
	}
	for _, title := range []string{"晨星守望者六月调整_20260705", "晨星守望者周年庆复刻_20260910", "晨星守望者新春版_20260915"} {
		if !mentioned[title] || declared[title] {
			t.Fatalf("%s must stay an ordinary candidate without the rerun slot: %v", title, hitTitles(result.Hits))
		}
	}
	// 同一活动名只提示最新的一份返场稿。
	notes := strings.Join(result.Warnings, "\n")
	if !strings.Contains(notes, "《晨星守望者复刻_20260805》") || strings.Contains(notes, "六月调整") || strings.Contains(notes, "限时返场") || strings.Contains(notes, "周年庆") || strings.Contains(notes, "新春版") {
		t.Fatalf("the rerun note must name only the newest declared rerun: %q", result.Warnings)
	}
	// 名额从小到大：独立名称策划优先，第二个名额给最新的返场稿，配表优先时仍保留配表。
	for _, testCase := range []struct {
		query        string
		maxDocuments int
		want         []string
		absent       []string
		minTables    int
	}{
		{"复用晨星888", 1, []string{"晨星·守望者888活动_20251119"}, []string{"晨星守望者复刻_20260805", "晨星守望者新春版_20260915"}, 0},
		{"复用晨星888", 2, []string{"晨星·守望者888活动_20251119", "晨星守望者复刻_20260805"}, []string{"晨星守望者周年庆复刻_20260910", "晨星守望者新春版_20260915"}, 0},
		{"复用晨星888", 8, []string{"晨星·守望者888活动_20251119", "晨星守望者限时返场_20260603", "晨星守望者六月调整_20260705", "晨星守望者复刻_20260805"}, nil, 0},
		{"我要复用晨星888，需要配哪些表", 4, []string{"晨星·守望者888活动_20251119", "晨星守望者复刻_20260805"}, []string{"晨星守望者周年庆复刻_20260910", "晨星守望者新春版_20260915"}, 1},
	} {
		bundle, err := service.Search.Retrieve(ctx, RetrievalRequest{SearchRequest: SearchRequest{Query: testCase.query}, MaxDocuments: testCase.maxDocuments})
		if err != nil {
			t.Fatal(err)
		}
		selected, tableCount := map[string]bool{}, 0
		for _, hit := range bundle.Search.Hits {
			selected[hit.Title] = true
			if hit.SourceKind == "table" {
				tableCount++
			}
		}
		for _, title := range testCase.want {
			if !selected[title] {
				t.Fatalf("%s (max %d): missing %s in %v", testCase.query, testCase.maxDocuments, title, hitTitles(bundle.Search.Hits))
			}
		}
		for _, title := range testCase.absent {
			if selected[title] {
				t.Fatalf("%s (max %d): unexpected %s in %v", testCase.query, testCase.maxDocuments, title, hitTitles(bundle.Search.Hits))
			}
		}
		if tableCount < testCase.minTables {
			t.Fatalf("%s (max %d): want at least %d tables in %v", testCase.query, testCase.maxDocuments, testCase.minTables, hitTitles(bundle.Search.Hits))
		}
	}
}

func TestDeclaredRerunContextBindsTheRelationToTheActivityName(t *testing.T) {
	for _, testCase := range []struct {
		text     string
		declared bool
	}{
		// 行首声明：行、单元格、句子或列表项以活动名开头，后接关系词。
		{"晨星守望者888活动返场相关界面调整和奖励调整。", true},
		{"晨星·守望者888的复刻，调整奖励。", true},
		{"# 概述\n晨星守望者888活动复刻，沿用返场配置。", true},
		{"字段 | A=说明\n行 3 | A=·晨星守望者888活动返场相关界面调整", true},
		{"1. 晨星守望者888活动返场，调整上架时间。", true},
		{"参考上一期配置。\n晨星守望者888活动返场相关调整。", true},
		{"本期改动：\n- 晨星守望者888活动返场相关调整", true},
		// 主语陈述：“本次”“本期”等指代本文档的主语与活动名、关系词紧挨。
		{"本次为晨星守望者888活动返场。", true},
		// 疑问只看活动名所在分句：后面问的是别的事，仍是声明；附加问句把整句变成疑问。
		{"本次为晨星守望者888活动返场，是否需要新增入口？", true},
		{"本次为晨星守望者888活动返场，仅调整奖励。", true},
		{"本次为晨星守望者888活动返场，对吗？", false},
		{"本期复用晨星守望者888活动的玩法。", true},
		// 复刻的是别的活动、只参考该活动、引用别的文档、否定是它的返场，或是更长名称里的子串，都不算。
		{"玩法参考：晨星守望者888活动；本活动是周年庆复刻，不是888的返场或复用。", false},
		{"玩法参考：晨星守望者888活动返场方案；本次是独立周年庆活动。", false},
		{"玩法参考：破晨星守望者888活动返场方案；本次是独立周年庆活动。", false},
		{"玩法参考·晨星守望者888活动返场相关调整。", false},
		{"相关文档：\n·晨星守望者888活动返场方案", false},
		// 引号里转述的旧稿、参考清单里的条目和疑问句都不是本文档的声明。
		{"参考旧稿：\"本次为晨星守望者888活动返场\"。本活动采用全新的周年庆玩法。", false},
		{"旧稿写的是“本次为晨星守望者888活动返场”，本期不沿用。", false},
		{"参考活动：\n- 晨星守望者888活动返场\n本活动采用全新的周年庆玩法，仅作对照。", false},
		{"本次为晨星守望者888活动返场吗？不是，本次为独立周年庆。", false},
		// 引号跨句、编号清单与配表行清单里的条目也要找到引号或清单标题。
		{"旧稿写的是“说明如下。晨星守望者888活动返场”。本期为独立周年庆，不沿用旧活动。", false},
		{"参考活动：\n1. 星海远航活动返场\n2. 晨星守望者888活动返场\n本期为独立周年庆。", false},
		{"行 1 | A=参考活动：\n行 2 | A=·星海远航活动返场\n行 3 | A=·晨星守望者888活动返场\n行 4 | A=本期为独立周年庆。", false},
		{"行 1 | A=本期改动：\n行 2 | A=·晨星守望者888活动返场相关界面调整", true},
		// 参考标题按单元格识别：同一行另有备注列、标题带编号、标题与条目同一行、条目不带符号、标题不带冒号，
		// 以及 Markdown、Word 表格和“三、”“##”小节标题，都要找到它；标题与条目之间隔着说明文字也一样。
		{"行 1 | A=栏目 | B=内容 | C=备注\n行 2 | A=参考活动： | C=仅供对照\n行 3 | B=·星海远航活动返场\n行 4 | B=·晨星守望者888活动返场\n行 5 | A=本期活动 | B=独立周年庆复刻，不沿用888活动", false},
		{"行 2 | A=1.参考活动：\n行 3 | B=·星海远航活动返场\n行 4 | B=·晨星守望者888活动返场", false},
		{"行 2 | A=参考活动： | B=晨星守望者888活动返场", false},
		{"行 2 | A=参考活动：\n行 3 | B=星海远航活动返场\n行 4 | B=晨星守望者888活动返场", false},
		{"参考活动\n·晨星守望者888活动返场", false},
		{"三、参考活动\n·晨星守望者888活动返场", false},
		{"行 2 | A=三、参考活动 | C=仅供对照\n行 3 | B=·晨星守望者888活动返场", false},
		{"三、参考的往期活动与玩法设计要点汇总说明\n·晨星守望者888活动返场", false},
		{"## 参考活动\n- 晨星守望者888活动返场", false},
		{"| 参考活动： | | 仅供对照 |\n| | ·晨星守望者888活动返场 | |", false},
		{"参考活动： | 仅供对照\n· 星海远航活动返场 | \n· 晨星守望者888活动返场 | ", false},
		{"参考活动：\n以下两个活动的玩法可以借用。\n·晨星守望者888活动返场", false},
		{"参考活动：\n1. 星海远航：\n玩法说明。\n2. 晨星守望者888活动返场", false},
		// 活动名属于另一个清单或小节时不受上面参考标题影响：冒号标题、“四、”小节标题、不同编号层级的标题都会截断。
		{"参考活动：\n·星海远航活动\n本期改动：\n·晨星守望者888活动返场相关调整", true},
		{"1.参考活动：\n·星海远航活动返场\n2.本期改动：\n·晨星守望者888活动返场相关调整", true},
		{"三、参考活动\n·星海远航活动\n四、本期内容\n·晨星守望者888活动返场相关调整", true},
		{"行 1 | A=参考活动： | B=星海远航活动\n行 2 | A=本期改动：\n行 3 | A=·晨星守望者888活动返场相关调整", true},
		{"一、系统目的\n·晨星守望者888活动返场相关界面调整和奖励调整", true},
		{"| 栏目 | 内容 |\n| 本期改动： | ·晨星守望者888活动返场相关调整 |", true},
		// 标题与参考标题共用单元格级识别：旁边另有备注列、与声明写在同一行、不以“本期”开头但下一列为空、
		// 不带冒号但以“本期”开头，都是另起的标题。
		{"行 1 | A=参考活动： | C=仅供对照\n行 2 | B=·星海远航活动\n行 3 | A=本期改动： | C=仅改日期\n行 4 | B=·晨星守望者888活动返场相关调整", true},
		{"行 1 | A=参考活动： | C=仅供对照\n行 2 | B=·星海远航活动\n行 3 | A=本期改动： | B=·晨星守望者888活动返场相关调整", true},
		{"行 1 | A=参考活动： | C=仅供对照\n行 2 | B=·星海远航活动\n行 3 | A=改动内容： | C=仅改日期\n行 4 | B=·晨星守望者888活动返场相关调整", true},
		{"行 1 | A=参考活动： | C=仅供对照\n行 2 | B=·星海远航活动\n行 3 | A=改动内容： | B=·晨星守望者888活动返场相关调整", true},
		{"| 参考活动： | | 仅供对照 |\n| | ·星海远航活动 | |\n| 本期改动： | | 仅改日期 |\n| | ·晨星守望者888活动返场相关调整 | |", true},
		{"参考活动\n·星海远航活动\n本期内容\n·晨星守望者888活动返场相关调整", true},
		// “活动名： | ××”这类下一列紧跟内容的字段名不是标题，参考清单里的字段行仍只是引用。
		{"行 1 | A=参考活动：\n行 2 | A=活动名： | B=星海远航活动返场\n行 3 | A=活动名： | B=晨星守望者888活动返场", false},
		{"参考活动：\n活动名： | 晨星守望者888活动返场", false},
		{"本次不复用晨星守望者888活动，采用独立周年庆玩法。", false},
		{"本活动不是晨星守望者888活动返场，玩法全新。", false},
		{"并非复用晨星守望者888活动。", false},
		{"参考晨星守望者888活动。返场奖励另行配置。", false},
		// 说不清主语的写法保守地不算，文档仍是普通候选。
		{"说明：晨星守望者888活动返场相关调整。", false},
		{"晨星守望者888活动第二次复刻。", false},
	} {
		context, declared := declaredRerunContext(newNormalizedSpanText(testCase.text), "晨星守望者888", 16)
		if declared != testCase.declared || declared && !strings.Contains(context, "守望者888") {
			t.Fatalf("%q: declared=%v context=%q, want declared=%v", testCase.text, declared, context, testCase.declared)
		}
	}
}

func TestTitleDeclaresRerun(t *testing.T) {
	for title, want := range map[string]bool{
		"晨星守望者限时返场_20260603": true,
		"晨星守望者复刻_20260805":   true,
		"【复用】晨星守望者888活动":     true,
		"晨星守望者六月调整_20260705": false,
		"晨星守望者周年庆（非返场）":      false,
		"晨星守望者新春版（不复用旧玩法）":   false,
	} {
		if got := titleDeclaresRerun(title); got != want {
			t.Fatalf("%s: titleDeclaresRerun=%v, want %v", title, got, want)
		}
	}
}

func TestGoIdentityMentionsDoNotTakeTheRerunSlot(t *testing.T) {
	for _, body := range []string{
		"玩法参考：晨星守望者888活动返场方案；本次是独立周年庆活动。",
		"玩法参考：破晨星守望者888活动返场方案；本次是独立周年庆活动。",
		"本次不复用晨星守望者888活动，采用独立周年庆玩法。",
		"本文复刻晨星守望者周年庆活动。玩法参考：晨星守望者888活动；本活动是周年庆复刻，不是888的返场或复用。",
		"参考旧稿：\"本次为晨星守望者888活动返场\"。本活动采用全新的周年庆玩法。",
		"参考活动：\n- 晨星守望者888活动返场\n本活动采用全新的周年庆玩法，仅作对照。",
		"本次为晨星守望者888活动返场吗？不是，本次为独立周年庆。",
		"旧稿写的是“说明如下。晨星守望者888活动返场”。本期为独立周年庆，不沿用旧活动。",
		"参考活动：\n1. 星海远航活动返场\n2. 晨星守望者888活动返场\n本期为独立周年庆。",
		"参考活动\n\n- 晨星守望者888活动返场\n\n本期为独立周年庆。",
		"本期为独立周年庆。\n\n## 参考活动\n\n- 星海远航活动返场\n- 晨星守望者888活动返场",
	} {
		// 标题写不写“复刻”都一样：标题带“复刻”时，正文的引用、转述、疑问与参考清单同样不能换来返场名额。
		for _, title := range []string{"晨星守望者周年庆_20260901", "晨星守望者周年庆复刻_20260901"} {
			service := newGoSearchService(t, []goSearchSource{{"plans", "design", map[string]string{
				"晨星·守望者888活动_20251119.md": "# 玩法\n\n晨星·守望者888活动的玩法与产出。",
				"晨星守望者限时返场_20260603.md":   "# 概述\n\n晨星守望者888活动返场相关界面调整和奖励调整。",
				title + ".md": "# 概述\n\n" + body,
			}}})
			bundle, err := service.Search.Retrieve(context.Background(), RetrievalRequest{SearchRequest: SearchRequest{Query: "复用晨星888"}, MaxDocuments: 2})
			if err != nil {
				t.Fatal(err)
			}
			// 周年庆只是提及、引用或否定该活动：它不能挤掉真正的返场稿，也不能得到返场提示。
			titles := hitTitles(bundle.Search.Hits)
			if indexOfString(titles, "晨星守望者限时返场_20260603") < 0 || strings.Contains(strings.Join(bundle.Search.Warnings, "\n"), "周年庆") {
				t.Fatalf("%s %q: the declared rerun must keep its slot: %v %q", title, body, titles, bundle.Search.Warnings)
			}
		}
	}
}

func TestGoIdentityTableReferenceListsDoNotTakeTheRerunSlot(t *testing.T) {
	retrieve := func(t *testing.T, title, table string) RetrievalBundle {
		t.Helper()
		service := newGoSearchService(t, []goSearchSource{{"plans", "design", map[string]string{
			"晨星·守望者888活动_20251119.md": "# 玩法\n\n晨星·守望者888活动的玩法与产出。",
			"晨星守望者限时返场_20260603.md":   "# 概述\n\n晨星守望者888活动返场相关界面调整和奖励调整。",
			title + ".csv": table,
		}}})
		bundle, err := service.Search.Retrieve(context.Background(), RetrievalRequest{SearchRequest: SearchRequest{Query: "复用晨星888"}, MaxDocuments: 2})
		if err != nil {
			t.Fatal(err)
		}
		return bundle
	}
	// 表格里的参考清单：标题行另有备注列、标题带编号、只有一列，或条目不带符号，都只是引用。
	for name, table := range map[string]string{
		"note_column":     "栏目,内容,备注\n参考活动：,,仅供对照\n,·星海远航活动返场,\n,·晨星守望者888活动返场,\n本期活动,独立周年庆复刻，不沿用888活动,\n",
		"numbered":        "栏目,内容,备注\n1.参考活动：,,\n,·星海远航活动返场,\n,·晨星守望者888活动返场,\n本期活动,独立周年庆复刻，不沿用888活动,\n",
		"single_column":   "栏目,内容,备注\n参考活动：,,\n,·星海远航活动返场,\n,·晨星守望者888活动返场,\n本期活动,独立周年庆复刻，不沿用888活动,\n",
		"without_bullets": "栏目,内容,备注\n参考活动：,,仅供对照\n,星海远航活动返场,\n,晨星守望者888活动返场,\n本期活动,独立周年庆复刻，不沿用888活动,\n",
		"same_row":        "栏目,内容,备注\n参考活动：,晨星守望者888活动返场,仅供对照\n本期活动,独立周年庆复刻，不沿用888活动,\n",
		"field_labels":    "栏目,内容,备注\n参考活动：,,仅供对照\n活动名：,星海远航活动返场,\n活动名：,晨星守望者888活动返场,\n本期活动,独立周年庆复刻，不沿用888活动,\n",
	} {
		t.Run(name, func(t *testing.T) {
			bundle := retrieve(t, "晨星守望者周年庆复刻_20260901", table)
			if indexOfString(hitTitles(bundle.Search.Hits), "晨星守望者限时返场_20260603") < 0 || strings.Contains(strings.Join(bundle.Search.Warnings, "\n"), "周年庆") {
				t.Fatalf("the reference list must not take the rerun slot: %v %q", hitTitles(bundle.Search.Hits), bundle.Search.Warnings)
			}
		})
	}
	// 对照：参考清单之后另起标题声明返场的新稿是返场稿，占返场名额并得到提示；标题旁另有备注列、标题与声明写在
	// 同一行，或标题不以“本期”开头，都算另起的标题。
	for name, table := range map[string]string{
		"own_heading":               "栏目,内容,备注\n参考活动：,,仅供对照\n,·星海远航活动,\n本期改动：,,\n,·晨星守望者888活动返场相关调整,\n",
		"own_heading_with_note":     "栏目,内容,备注\n参考活动：,,仅供对照\n,·星海远航活动,\n本期改动：,,仅改日期\n,·晨星守望者888活动返场相关调整,\n",
		"own_heading_same_row":      "栏目,内容,备注\n参考活动：,,仅供对照\n,·星海远航活动,\n本期改动：,·晨星守望者888活动返场相关调整,\n",
		"neutral_heading_with_note": "栏目,内容,备注\n参考活动：,,仅供对照\n,·星海远航活动,\n改动内容：,,仅改日期\n,·晨星守望者888活动返场相关调整,\n",
		"neutral_heading_same_row":  "栏目,内容,备注\n参考活动：,,仅供对照\n,·星海远航活动,\n改动内容：,·晨星守望者888活动返场相关调整,\n",
	} {
		t.Run(name, func(t *testing.T) {
			bundle := retrieve(t, "晨星守望者限时返场_20260901", table)
			if indexOfString(hitTitles(bundle.Search.Hits), "晨星守望者限时返场_20260901") < 0 || !strings.Contains(strings.Join(bundle.Search.Warnings, "\n"), "《晨星守望者限时返场_20260901》") {
				t.Fatalf("a table declaring the rerun under its own heading must take the slot: %v %q", hitTitles(bundle.Search.Hits), bundle.Search.Warnings)
			}
		})
	}
}

func TestGoIdentityDeclaredRerunLooksBackAcrossParagraphs(t *testing.T) {
	retrieve := func(t *testing.T, title, body string) RetrievalBundle {
		t.Helper()
		service := newGoSearchService(t, []goSearchSource{{"plans", "design", map[string]string{
			"晨星·守望者888活动_20251119.md": "# 玩法\n\n晨星·守望者888活动的玩法与产出。",
			"晨星守望者限时返场_20260603.md":   "# 概述\n\n晨星守望者888活动返场相关界面调整和奖励调整。",
			title + ".md": body,
		}}})
		bundle, err := service.Search.Retrieve(context.Background(), RetrievalRequest{SearchRequest: SearchRequest{Query: "复用晨星888"}, MaxDocuments: 2})
		if err != nil {
			t.Fatal(err)
		}
		return bundle
	}
	// Markdown 按空行分段，清单标题与条目分在不同文本块：往前补看同一小节的文本块，参考清单仍只是引用。
	bundle := retrieve(t, "晨星守望者周年庆复刻_20260901", "# 概述\n\n参考活动：\n\n- 星海远航活动返场\n\n- 晨星守望者888活动返场\n\n本期为独立周年庆。")
	if indexOfString(hitTitles(bundle.Search.Hits), "晨星守望者限时返场_20260603") < 0 || strings.Contains(strings.Join(bundle.Search.Warnings, "\n"), "周年庆") {
		t.Fatalf("a reference list split by blank lines must not take the rerun slot: %v %q", hitTitles(bundle.Search.Hits), bundle.Search.Warnings)
	}
	// 对照：前一个文本块是“本期改动：”，新稿在自己的小节里声明返场，占返场名额并得到提示。
	bundle = retrieve(t, "晨星守望者限时返场_20260901", "# 概述\n\n参考活动：\n\n- 星海远航活动\n\n本期改动：\n\n- 晨星守望者888活动返场相关调整")
	if indexOfString(hitTitles(bundle.Search.Hits), "晨星守望者限时返场_20260901") < 0 || !strings.Contains(strings.Join(bundle.Search.Warnings, "\n"), "《晨星守望者限时返场_20260901》") {
		t.Fatalf("a rerun declared under its own heading must take the slot: %v %q", hitTitles(bundle.Search.Hits), bundle.Search.Warnings)
	}
}

func TestRerunReferenceSection(t *testing.T) {
	for headingPath, want := range map[string]bool{
		`["参考表"]`:             true,
		`["","","4.1.2参考图片"]`: true,
		`["概述","参考活动："]`:      true,
		`["概述"]`:              false,
		`null`:                false,
		`["玩法","玩法：类似这种推箱子解谜，有步数限制"]`: false,
	} {
		if got := rerunReferenceSection(headingPath); got != want {
			t.Fatalf("%s: rerunReferenceSection=%v, want %v", headingPath, got, want)
		}
	}
}

func TestGoRetrieveBothSourceKindsKeepsTheIdentitySlots(t *testing.T) {
	designs := map[string]string{
		"晨星·守望者888活动_20251119.md":     "# 玩法\n\n晨星·守望者888活动的玩法与产出。",
		"晨星守望者限时返场_20260603.md":       "# 概述\n\n晨星守望者888活动返场相关界面调整和奖励调整。",
		"【复用】破晨星·裂空888活动_20260506.md": "# 玩法\n\n复用破晨星·裂空888活动的玩法与产出。",
	}
	tables := map[string]string{}
	for index := 1; index <= 8; index++ {
		tables[fmt.Sprintf("activityConfig%d_2026090%d.md", index, index)] = fmt.Sprintf("# 配置\n\n晨星·守望者888活动第%d项配置。", index)
	}
	service := newGoSearchService(t, []goSearchSource{{"plans", "design", designs}, {"tables", "table", tables}})
	request := RetrievalRequest{SearchRequest: SearchRequest{Query: "我要复用晨星888，需要配哪些表"}, MaxDocuments: 8}
	unrestricted, err := service.Search.Retrieve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	// 调用方显式选了策划与配表两类来源，与不限来源相同：仍分别检索，原案与返场稿不被更新的配表挤掉。
	request.SourceKinds = []string{"design", "table"}
	both, err := service.Search.Retrieve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	titles := hitTitles(both.Search.Hits)
	if strings.Join(titles, "|") != strings.Join(hitTitles(unrestricted.Search.Hits), "|") || indexOfString(titles, "晨星·守望者888活动_20251119") < 0 || indexOfString(titles, "晨星守望者限时返场_20260603") < 0 {
		t.Fatalf("both source kinds must behave like no restriction: %v vs %v", titles, hitTitles(unrestricted.Search.Hits))
	}
}

func TestGoRetrieveIdentityNumberIsNotAnExplicitID(t *testing.T) {
	tables := map[string]string{}
	for index := 1; index <= 8; index++ {
		tables[fmt.Sprintf("activityConfig%d_2026090%d.md", index, index)] = fmt.Sprintf("# 配置\n\n晨星·守望者888活动第%d项配置。", index)
	}
	service := newGoSearchService(t, []goSearchSource{
		{"plans", "design", map[string]string{
			"晨星·守望者888活动_20251119.md":          "# 玩法\n\n晨星·守望者888活动的玩法与产出。",
			"晨星守望者限时返场_20260603.md":            "# 概述\n\n晨星守望者888活动返场相关界面调整和奖励调整。",
			"剧情/晨星·守望者888活动/守望者剧情_20260804.md": "# 剧情\n\n守望者剧情对白。",
		}},
		{"tables", "table", tables},
	})
	bundle, err := service.Search.Retrieve(context.Background(), RetrievalRequest{SearchRequest: SearchRequest{Query: "我要复用晨星888，需要配哪些表"}, MaxDocuments: 8})
	if err != nil {
		t.Fatal(err)
	}
	// 两个策划名额已由身份策划和返场稿占满；“888”是活动身份的编号而不是显式 ID，
	// 同名目录下的剧情稿不能因路径含“888”再被列为必选文档，挤掉配表名额。
	tableCount := 0
	for _, hit := range bundle.Search.Hits {
		if hit.Title == "守望者剧情_20260804" {
			t.Fatalf("the identity number must not pull in a path-only document: %v", hitTitles(bundle.Search.Hits))
		}
		if hit.SourceKind == "table" {
			tableCount++
		}
	}
	if tableCount != 6 {
		t.Fatalf("a table-first bundle of 8 must keep its 6 table slots, got %d: %v", tableCount, hitTitles(bundle.Search.Hits))
	}
}

func hitTitles(hits []SearchHit) []string {
	titles := make([]string, len(hits))
	for index, hit := range hits {
		titles[index] = hit.Title
	}
	return titles
}

func TestGoRetrieveTableFirstPrefersSystemDirectoryTablesOverNewerHubTables(t *testing.T) {
	tables := map[string]string{
		"扭蛋机/alphaLottery_20260801.md": "# 抽奖\n\n普通奖池与特殊奖池的概率配置。",
		"扭蛋机/alphaPool_20260802.md":    "# 奖池\n\n奖池道具与权重配置。",
	}
	// 汇总表更新、在层级标题里登记了系统名，能过相关度门槛，但弱于目录直接归属的专用表。
	for index := 1; index <= 6; index++ {
		tables[fmt.Sprintf("汇总/hubSummary%d_2026091%d.md", index, index)] = fmt.Sprintf("# 扭蛋机\n\n活动列表%d：扭蛋机、签到、商店。", index)
	}
	for index := 1; index <= 12; index++ {
		tables[fmt.Sprintf("通用/common%d_2026070%d.md", index, index%10)] = fmt.Sprintf("# 通用\n\n通用配置%d：字段与参数。", index)
	}
	service := newGoSearchService(t, []goSearchSource{
		{"plans", "design", map[string]string{"扭蛋机活动_20260812.md": "# 配表实现\n\n扭蛋机配置：抽奖表 alphaLottery，奖池表 alphaPool。"}},
		{"tables", "table", tables},
	})
	bundle, err := service.Search.Retrieve(context.Background(), RetrievalRequest{SearchRequest: SearchRequest{Query: "我要新增一个扭蛋机，需要配置哪些表格"}, MaxDocuments: 4})
	if err != nil {
		t.Fatal(err)
	}
	titles := map[string]bool{}
	for _, hit := range bundle.Search.Hits {
		titles[hit.Title] = true
	}
	for _, wanted := range []string{"alphaLottery_20260801", "alphaPool_20260802", "扭蛋机活动_20260812"} {
		if !titles[wanted] {
			t.Fatalf("missing %s in %v", wanted, titles)
		}
	}
	for index := 1; index < len(bundle.Search.Hits); index++ {
		previous, _ := time.Parse(time.RFC3339Nano, bundle.Search.Hits[index-1].EffectiveUpdatedAt)
		current, _ := time.Parse(time.RFC3339Nano, bundle.Search.Hits[index].EffectiveUpdatedAt)
		if bundle.Search.Hits[index-1].SourceKind == bundle.Search.Hits[index].SourceKind && previous.Before(current) {
			t.Fatalf("selected tables must still be presented newest first: %#v", bundle.Search.Hits)
		}
	}
}
