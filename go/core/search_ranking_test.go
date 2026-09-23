package core

import (
	"context"
	"fmt"
	"reflect"
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
		{"title contains", normalizedCandidateFields{title: "扭蛋机活动_20260819"}, false, 0.85},
		{"name column cell", normalizedCandidateFields{title: "pet", relativePath: `special\pet.xlsx`, text: header + " 行 2 | a=11 | b=扭蛋机"}, true, 0.85},
		{"table system directory", normalizedCandidateFields{title: "alphalottery", relativePath: `高级配置\扭蛋机\alphalottery.xlsx`, text: header + " 行 2 | a=1 | b=普通"}, true, 0.8},
		{"other exact cell", normalizedCandidateFields{title: "eventsummary", relativePath: `前端\eventsummary.xlsx`, text: header + " 行 2 | a=1 | c=扭蛋机"}, true, 0.75},
		{"design directory", normalizedCandidateFields{title: "设定", relativePath: `扭蛋机\设定.docx`}, false, 0.65},
		{"body mention", normalizedCandidateFields{title: "eventsummary", relativePath: `前端\eventsummary.xlsx`, text: header + " 行 2 | a=1 | c=扭蛋机返场活动"}, true, 0.3},
		{"missing", normalizedCandidateFields{title: "eventsummary", text: header}, true, 0},
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

func TestGoSearchIdentityPrefersStandaloneEntityOverLongerNewerName(t *testing.T) {
	service := newGoSearchService(t, []goSearchSource{
		{"plans", "design", map[string]string{
			"晨星·守望者888活动_20251119.md":  "# 玩法\n\n晨星·守望者888活动的玩法与产出。",
			"破晨星·裂空888活动_20260506.md":  "# 玩法\n\n破晨星·裂空888活动的玩法与产出。",
			"【复用】星河龙888活动_20260901.md": "# 配表\n\n复用活动需要配置的表。",
		}},
		{"tables", "table", map[string]string{"activityTime_20260901.md": "# 时间\n\n晨星·守望者888活动开放时间。"}},
	})
	ctx := context.Background()
	result, err := service.Search.Search(ctx, SearchRequest{Query: "晨星888活动的玩法", Sort: "newest", Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) == 0 || result.Hits[0].Title != "晨星·守望者888活动_20251119" {
		t.Fatalf("hits=%#v", result.Hits)
	}
	for _, hit := range result.Hits {
		if hit.Title == "破晨星·裂空888活动_20260506" {
			t.Fatal("a longer entity name must not satisfy the identity gate when a standalone match exists")
		}
	}
	tableIntent, err := service.Search.Search(ctx, SearchRequest{Query: "复用晨星888，需要配哪些表", Sort: "newest", Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	foundTable := false
	for _, hit := range tableIntent.Hits {
		if hit.Title == "破晨星·裂空888活动_20260506" {
			t.Fatal("table-intent search must also drop a longer entity name when a standalone match exists")
		}
		if hit.Title == "【复用】星河龙888活动_20260901" {
			t.Fatal("table-intent search must keep design hits on the requested activity identity")
		}
		foundTable = foundTable || hit.SourceKind == "table"
	}
	if !foundTable {
		t.Fatalf("table-intent search must not filter tables by activity identity: %#v", tableIntent.Hits)
	}
	bundle, err := service.Search.Retrieve(ctx, RetrievalRequest{SearchRequest: SearchRequest{Query: "我要复用晨星888，需要配哪些表"}, MaxDocuments: 4})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, hit := range bundle.Search.Hits {
		found = found || hit.Title == "晨星·守望者888活动_20251119"
		if hit.Title == "破晨星·裂空888活动_20260506" {
			t.Fatal("table-first retrieve picked the longer entity name as the activity plan")
		}
	}
	if !found {
		t.Fatalf("table-first retrieve must keep the activity plan: %#v", bundle.Search.Hits)
	}
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
		{"plans", "design", map[string]string{"扭蛋机活动_20260819.md": "# 配表实现\n\n扭蛋机配置：抽奖表 alphaLottery，奖池表 alphaPool。"}},
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
	for _, wanted := range []string{"alphaLottery_20260801", "alphaPool_20260802", "扭蛋机活动_20260819"} {
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
