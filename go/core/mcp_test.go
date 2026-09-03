package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newMCPTestService(t *testing.T) (*RuntimeService, string) {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	dataDir := filepath.Join(root, "data")
	sourceRoot := filepath.Join(root, "中文 策划来源")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "轮盘抽奖_20260902.md"), []byte("# 轮盘抽奖\n\n轮盘消耗抽奖券，奖池配置奖励与权重。\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	source, err := CreateSourceConfig("mcp-design", "MCP 策划案", "design", sourceRoot, true)
	if err != nil {
		t.Fatal(err)
	}
	config := CreateDefaultConfig()
	config.Sources = []Source{source}
	config.Indexing.AutomaticScan = false
	config.Indexing.Concurrency = 1
	store := NewConfigStore(configDir, dataDir)
	if _, err := store.SaveSnapshot(config); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DESIGN_RAG_PLUGIN_ROOT", filepath.Join(projectRootForTest(t), "plugins", "design-rag"))
	service, err := NewRuntimeService(context.Background(), ServiceOptions{ConfigDir: configDir, DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := service.Index(context.Background(), IndexOptions{Full: true}, nil); err != nil || result.Indexed != 1 {
		service.Close()
		t.Fatalf("seed index failed: result=%#v err=%v", result, err)
	}
	return service, root
}

func projectRootForTest(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestResolveSkillRootRejectsProcessCWDDecoy(t *testing.T) {
	temporaryRoot := t.TempDir()
	oldWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWorkingDirectory) })
	t.Setenv("DESIGN_RAG_PLUGIN_ROOT", "")
	decoy := filepath.Join(temporaryRoot, "skills", "game-design-rag")
	for _, relative := range []string{"SKILL.md", filepath.Join("references", "administration.md"), filepath.Join("references", "analysis-workflows.md")} {
		path := filepath.Join(decoy, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("untrusted cwd decoy"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chdir(filepath.Dir(filepath.Dir(decoy))); err != nil {
		t.Fatal(err)
	}
	root, err := ResolveSkillRoot()
	if err == nil && pathInside(filepath.Dir(filepath.Dir(decoy)), root) {
		t.Fatalf("accepted process CWD decoy: %s", root)
	}
}

func TestMCPGoSDKContractSearchResourcesAndLifecycle(t *testing.T) {
	service, root := newMCPTestService(t)
	defer service.Close()
	server, jobs, err := NewMCPServer(service)
	if err != nil {
		t.Fatal(err)
	}
	defer jobs.Stop()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "drag-go-test", Version: BackendVersion}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	resources, err := clientSession.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resourceURIs := []string{}
	for _, resource := range resources.Resources {
		resourceURIs = append(resourceURIs, resource.URI)
	}
	sort.Strings(resourceURIs)
	wantedResources := []string{"design-rag://skill/game-design-rag", "design-rag://skill/game-design-rag/administration", "design-rag://skill/game-design-rag/analysis-workflows"}
	if !equalStrings(resourceURIs, wantedResources) {
		t.Fatalf("resources=%v", resourceURIs)
	}
	read, err := clientSession.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: wantedResources[0]})
	if err != nil || len(read.Contents) != 1 || read.Contents[0].Text == "" {
		t.Fatalf("read resource failed: result=%#v err=%v", read, err)
	}

	tools, err := clientSession.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	byName := map[string]*mcp.Tool{}
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		byName[tool.Name] = tool
	}
	sort.Strings(names)
	wantedTools := []string{"drag_cache_clear", "drag_index_pause", "drag_index_resume", "drag_index_status", "drag_index_update", "drag_list_versions", "drag_read_citation", "drag_retrieve", "drag_search", "drag_source_add", "drag_source_remove", "drag_source_update", "drag_sources"}
	if !equalStrings(names, wantedTools) {
		t.Fatalf("tools=%v", names)
	}
	for _, name := range []string{"drag_search", "drag_retrieve", "drag_read_citation", "drag_list_versions", "drag_sources", "drag_index_status"} {
		if byName[name].Annotations == nil || !byName[name].Annotations.ReadOnlyHint {
			t.Fatalf("%s must be read-only", name)
		}
	}
	if byName["drag_source_remove"].Annotations == nil || byName["drag_source_remove"].Annotations.DestructiveHint == nil || !*byName["drag_source_remove"].Annotations.DestructiveHint {
		t.Fatal("drag_source_remove must be destructive")
	}
	if byName["drag_source_remove"].Annotations.IdempotentHint {
		t.Fatal("drag_source_remove must not claim idempotence")
	}

	search, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "drag_search", Arguments: map[string]any{"query": "轮盘奖池", "limit": 10}})
	if err != nil || search.IsError || search.StructuredContent == nil {
		t.Fatalf("search failed: result=%#v err=%v", search, err)
	}
	retrieve, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "drag_retrieve", Arguments: map[string]any{"query": "轮盘奖励怎么配置", "maxDocuments": 30, "maxChars": 30000}})
	if err != nil || retrieve.IsError || retrieve.StructuredContent == nil {
		t.Fatalf("retrieve failed: result=%#v err=%v", retrieve, err)
	}
	for _, invalid := range []struct {
		name string
		args map[string]any
	}{
		{"drag_retrieve", map[string]any{"query": "x", "unknown": true}},
		{"drag_search", map[string]any{"query": "x", "sourceKinds": []string{"invalid"}}},
		{"drag_search", map[string]any{"query": "x", "sectionTypes": []string{"invalid"}}},
		{"drag_list_versions", map[string]any{"familyKey": "missing", "limit": 101}},
		{"drag_source_add", map[string]any{"id": "too-long", "label": strings.Repeat("x", 101), "kind": "design", "rootPath": root, "enabled": false}},
		{"drag_sources", map[string]any{"unexpected": true}},
		{"drag_index_status", map[string]any{"unexpected": true}},
		{"drag_index_pause", map[string]any{"unexpected": true}},
		{"drag_index_resume", map[string]any{"unexpected": true}},
		{"drag_cache_clear", map[string]any{"unexpected": true}},
	} {
		result, callErr := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: invalid.name, Arguments: invalid.args})
		if callErr != nil || result == nil || !result.IsError {
			t.Fatalf("invalid %s args were accepted: result=%#v err=%v", invalid.name, result, callErr)
		}
	}
	statusAfterRejectedClear, err := service.Status()
	if err != nil || statusAfterRejectedClear.DocumentCount != 1 {
		t.Fatalf("invalid cache_clear mutated index: status=%#v err=%v", statusAfterRejectedClear, err)
	}

	disabledRoot := filepath.Join(root, "待启用来源")
	if err := os.MkdirAll(disabledRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	added, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "drag_source_add", Arguments: map[string]any{"id": "go-disabled", "label": "Go Disabled", "kind": "design", "rootPath": disabledRoot, "enabled": false}})
	if err != nil || added.IsError {
		t.Fatalf("source add failed: result=%#v err=%v", added, err)
	}
	if !containsSource(service.Config().Sources, "go-disabled") {
		t.Fatal("source add did not update config")
	}
	removed, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "drag_source_remove", Arguments: map[string]any{"id": "go-disabled"}})
	if err != nil || removed.IsError || containsSource(service.Config().Sources, "go-disabled") {
		t.Fatalf("source remove failed: result=%#v err=%v", removed, err)
	}
}

func TestBackgroundIndexJobRejectsConcurrentMutation(t *testing.T) {
	service, _ := newMCPTestService(t)
	defer service.Close()
	job := NewBackgroundIndexJob(service)
	defer job.Stop()
	job.mutex.Lock()
	job.active = true
	job.operation = "index"
	job.done = make(chan struct{})
	job.mutex.Unlock()
	if _, err := job.StartIndex(IndexOptions{}); err == nil {
		t.Fatal("concurrent background index must be rejected")
	}
	job.mutex.Lock()
	job.active = false
	close(job.done)
	job.mutex.Unlock()
}

func TestBackgroundIndexJobRealStartPauseResumeAndStop(t *testing.T) {
	service, root := newMCPTestService(t)
	defer service.Close()
	sourceRoot := filepath.Join(root, "中文 策划来源")
	payload := strings.Repeat("轮盘奖池配置与奖励流程。", 400)
	for index := 0; index < 240; index++ {
		name := filepath.Join(sourceRoot, fmt.Sprintf("background-%03d.md", index))
		if err := os.WriteFile(name, []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	job := NewBackgroundIndexJob(service)
	status, err := job.StartIndex(IndexOptions{})
	if err != nil || status["backgroundJobActive"] != true {
		job.Stop()
		t.Fatalf("start status=%#v err=%v", status, err)
	}
	if _, err := job.Pause(); err != nil {
		job.Stop()
		t.Fatal(err)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	paused := false
	for !paused {
		service.activeMutex.RLock()
		paused = service.activeSummary != nil && service.activeSummary.Phase == "paused" && service.activeController != nil && service.activeController.IsPaused()
		service.activeMutex.RUnlock()
		if paused {
			break
		}
		select {
		case <-deadline.C:
			job.Stop()
			t.Fatal("real background controller did not observe immediate pause intent")
		case <-ticker.C:
		}
	}
	if err := job.ensureNoBackgroundMutation(); err == nil {
		job.Stop()
		t.Fatal("mutation was allowed while real background index was paused")
	}
	if _, err := job.Resume(); err != nil {
		job.Stop()
		t.Fatal(err)
	}
	job.mutex.Lock()
	done := job.done
	job.mutex.Unlock()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		job.Stop()
		t.Fatal("real background index did not finish after resume")
	}
	status, err = job.Status()
	if err != nil || status["backgroundJobActive"] != false || status["backgroundJobError"] != nil {
		t.Fatalf("final status=%#v err=%v", status, err)
	}
	status, err = job.StartIndex(IndexOptions{})
	if err != nil || status["backgroundJobActive"] != true {
		job.Stop()
		t.Fatalf("restart status=%#v err=%v", status, err)
	}
	job.Stop()
	status, err = job.Status()
	if err != nil || status["backgroundJobActive"] != false || status["backgroundJobError"] != nil {
		t.Fatalf("stopped status=%#v err=%v", status, err)
	}
}

func TestMCPGoTestIdentityAndResourceSchemeAreIsolated(t *testing.T) {
	service, _ := newMCPTestService(t)
	defer service.Close()
	t.Setenv("DESIGN_RAG_MCP_NAME", "design-rag-go-test")
	t.Setenv("DESIGN_RAG_RESOURCE_SCHEME", "design-rag-go-test")
	server, jobs, err := NewMCPServer(service)
	if err != nil {
		t.Fatal(err)
	}
	defer jobs.Stop()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "identity-test"}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	if got := clientSession.InitializeResult().ServerInfo.Name; got != "design-rag-go-test" {
		t.Fatalf("server name=%s", got)
	}
	resources, err := clientSession.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range resources.Resources {
		if len(resource.URI) < len("design-rag-go-test://") || resource.URI[:len("design-rag-go-test://")] != "design-rag-go-test://" {
			t.Fatalf("resource scheme not isolated: %s", resource.URI)
		}
	}
}

func TestMCPMutationErrorPreservesCommittedStructuredResult(t *testing.T) {
	service, root := newMCPTestService(t)
	defer service.Close()
	server, jobs, err := NewMCPServer(service)
	if err != nil {
		t.Fatal(err)
	}
	defer jobs.Stop()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "committed-result-test"}, nil).Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	if _, err := service.Database.db.Exec(`CREATE TRIGGER fail_mcp_pending BEFORE INSERT ON source_index_state BEGIN SELECT RAISE(FAIL,'forced MCP pending failure'); END`); err != nil {
		t.Fatal(err)
	}
	sourceRoot := filepath.Join(root, "committed-source")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "drag_source_add", Arguments: map[string]any{"id": "committed", "label": "Committed", "kind": "design", "rootPath": sourceRoot}})
	if err != nil || result == nil || !result.IsError || result.StructuredContent == nil || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "配置已原子提交") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if !containsSource(service.Config().Sources, "committed") {
		t.Fatal("committed MCP config was not applied despite structured partial failure")
	}
}

func containsSource(sources []Source, id string) bool {
	for _, source := range sources {
		if source.ID == id {
			return true
		}
	}
	return false
}
