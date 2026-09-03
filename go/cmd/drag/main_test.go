package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tadazly/design-rag/go/core"
)

func parsed(t *testing.T, values ...string) arguments {
	t.Helper()
	result, err := parseArguments(values)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPureGoCLIParseVersionAndSourceLifecycle(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	dataDir := filepath.Join(root, "data")
	sourceDir := filepath.Join(root, "中文 策划来源")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "轮盘_20260902.md"), []byte("# 玩法\n\n轮盘奖励与奖池配置。"), 0o644); err != nil {
		t.Fatal(err)
	}
	config := core.CreateDefaultConfig()
	config.Sources = nil
	config.Indexing.AutomaticScan = false
	config.Indexing.Concurrency = 1
	if _, err := core.NewConfigStore(configDir, dataDir).SaveSnapshot(config); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DESIGN_RAG_CONFIG_DIR", configDir)
	t.Setenv("DESIGN_RAG_DATA_DIR", dataDir)

	version, write, err := execute(context.Background(), parsed(t, "--version", "--json"))
	if err != nil || !write || version.(map[string]any)["name"] != "design-rag" {
		t.Fatalf("version=%#v write=%v err=%v", version, write, err)
	}
	if _, _, err := execute(context.Background(), parsed(t, "sources", "add", "--id", "relative", "--kind", "design", "--path", "relative")); err == nil {
		t.Fatal("relative source path must fail")
	}
	added, _, err := execute(context.Background(), parsed(t, "sources", "add", "--id", "cli-design", "--label", "CLI 策划", "--kind", "design", "--path", sourceDir, "--disabled", "--json"))
	if err != nil || added == nil {
		t.Fatalf("add failed: %#v %v", added, err)
	}
	if _, _, err := execute(context.Background(), parsed(t, "index", "--source", "cli-design", "--json")); err == nil {
		t.Fatal("explicit disabled source must fail closed")
	}
	if _, _, err := execute(context.Background(), parsed(t, "index", "--source", "missing-source", "--json")); err == nil {
		t.Fatal("unknown explicit source must fail closed")
	}
	listed, _, err := execute(context.Background(), parsed(t, "sources", "list", "--json"))
	if err != nil || listed == nil {
		t.Fatalf("list failed: %#v %v", listed, err)
	}
	if _, _, err := execute(context.Background(), parsed(t, "sources", "update", "cli-design", "--enable", "--json")); err != nil {
		t.Fatal(err)
	}
	full, _, err := execute(context.Background(), parsed(t, "index", "--full", "--json"))
	if err != nil || full.(core.RunSummary).Discovered != 1 || full.(core.RunSummary).Indexed != 1 {
		t.Fatalf("unscoped full index must include enabled sources: %#v err=%v", full, err)
	}
	search, _, err := execute(context.Background(), parsed(t, "search", "轮盘奖池", "--limit", "8", "--json"))
	if err != nil || len(search.(core.SearchResponse).Hits) != 1 {
		t.Fatalf("search=%#v err=%v", search, err)
	}
	updatedSourceDir := filepath.Join(root, "更新后的来源")
	if err := os.MkdirAll(updatedSourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(updatedSourceDir, "轮盘_20260902.md"), []byte("# 玩法\n\n轮盘奖励与奖池配置。"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := core.NewConfigStore(configDir, dataDir)
	snapshot, err := store.LoadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Config.Sources[0].IncludeExtensions = []string{".md", ".txt"}
	snapshot.Config.Sources[0].ExcludeDirectoryNames = append(snapshot.Config.Sources[0].ExcludeDirectoryNames, "custom-private")
	snapshot.Config.Sources[0].MaxFileBytes = 4096
	if _, err := store.SaveSnapshot(snapshot.Config); err != nil {
		t.Fatal(err)
	}
	if _, _, err := execute(context.Background(), parsed(t, "sources", "update", "cli-design", "--path", updatedSourceDir, "--json")); err != nil {
		t.Fatal(err)
	}
	updatedSnapshot, err := store.LoadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	updatedSource := updatedSnapshot.Config.Sources[0]
	if updatedSource.MaxFileBytes != 4096 || !contains(updatedSource.IncludeExtensions, ".txt") || !contains(updatedSource.ExcludeDirectoryNames, "custom-private") {
		t.Fatalf("path-only update reset source policy: %#v", updatedSource)
	}
	if _, _, err := execute(context.Background(), parsed(t, "sources", "remove", "cli-design", "--yes", "--json")); err != nil {
		t.Fatal(err)
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func TestPureGoCLIRejectsConflictingIndexScope(t *testing.T) {
	root := t.TempDir()
	config := core.CreateDefaultConfig()
	config.Sources = nil
	store := core.NewConfigStore(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if _, err := store.SaveSnapshot(config); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DESIGN_RAG_CONFIG_DIR", store.ConfigDir)
	t.Setenv("DESIGN_RAG_DATA_DIR", store.DataDir)
	if _, _, err := execute(context.Background(), parsed(t, "index", "--full", "--source", "plans")); err == nil {
		t.Fatal("--full and --source must conflict")
	}
}
