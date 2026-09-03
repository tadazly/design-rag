package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGoConfigAcceptsTypeScriptUnknownFieldsAndEmptyExcludeList(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	source, err := CreateSourceConfig("plans", "策划案", "design", sourceRoot, true)
	if err != nil {
		t.Fatal(err)
	}
	fileAncestor := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(fileAncestor, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateSourceRootPath(filepath.Join(fileAncestor, "child")); err == nil || !strings.Contains(err.Error(), "上级不是目录") {
		t.Fatalf("non-directory ancestor must fail: %v", err)
	}
	source.ExcludeDirectoryNames = []string{}
	config := CreateDefaultConfig()
	config.Sources = []Source{source}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	wire["futureTopLevelField"] = map[string]any{"safe": true}
	search := wire["search"].(map[string]any)
	search["futureSearchField"] = "ignored like Zod object stripping"
	raw, _ = json.MarshalIndent(wire, "", "  ")
	store := NewConfigStore(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if err := os.MkdirAll(store.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ConfigPath, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.LoadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Config.Sources) != 1 || len(snapshot.Config.Sources[0].ExcludeDirectoryNames) == 0 || !containsString(snapshot.Config.Sources[0].ExcludeDirectoryNames, ".git") {
		t.Fatalf("defaults not restored: %#v", snapshot.Config.Sources)
	}
	invalid := snapshot.Config
	badEndpoint := "not-a-url"
	invalid.Search.Embedding.Endpoint = badEndpoint
	if _, err := ValidateConfig(invalid); err == nil {
		t.Fatal("invalid embedding URL must fail")
	}
	invalid = snapshot.Config
	invalid.Codex.Model = new(string)
	if _, err := ValidateConfig(invalid); err == nil {
		t.Fatal("empty optional Codex string must fail")
	}
}

func TestGoConfigRejectsMissingNullAndTrailingRequiredData(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	source, err := CreateSourceConfig("plans", "策划案", "design", sourceRoot, true)
	if err != nil {
		t.Fatal(err)
	}
	config := CreateDefaultConfig()
	config.Sources = []Source{source}
	base, _ := json.Marshal(config)
	store := NewConfigStore(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if err := os.MkdirAll(store.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(map[string]any){
		"sources null":           func(value map[string]any) { value["sources"] = nil },
		"codex missing":          func(value map[string]any) { delete(value, "codex") },
		"source enabled missing": func(value map[string]any) { delete(value["sources"].([]any)[0].(map[string]any), "enabled") },
		"synonym missing":        func(value map[string]any) { delete(value["search"].(map[string]any), "synonymExpansion") },
		"automatic scan missing": func(value map[string]any) { delete(value["indexing"].(map[string]any), "automaticScan") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(base, &value); err != nil {
				t.Fatal(err)
			}
			mutate(value)
			raw, _ := json.Marshal(value)
			if err := os.WriteFile(store.ConfigPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.LoadSnapshot(); err == nil {
				t.Fatal("invalid required shape was accepted")
			}
		})
	}
	if err := os.WriteFile(store.ConfigPath, append(base, []byte("\n{}")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadSnapshot(); err == nil {
		t.Fatal("trailing JSON value was accepted")
	}
}

func TestReadOnlyServiceDoesNotInitializePristineState(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "missing-config")
	dataDir := filepath.Join(root, "missing-data")
	if _, err := NewRuntimeService(context.Background(), ServiceOptions{ConfigDir: configDir, DataDir: dataDir, ReadOnly: true}); err == nil || !strings.Contains(err.Error(), "drag init") {
		t.Fatalf("read-only pristine service error=%v", err)
	}
	for _, path := range []string{configDir, dataDir} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read-only service created %s: %v", path, err)
		}
	}
}

func TestConfigFingerprintNeverCreatesStateDirectories(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "missing-config")
	dataDir := filepath.Join(root, "missing-data")
	store := NewConfigStore(configDir, dataDir)
	if _, err := store.Fingerprint(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fingerprint error=%v", err)
	}
	for _, path := range []string{configDir, dataDir} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("fingerprint created %s: %v", path, err)
		}
	}
}

func TestPauseResumePersistsObservablePhase(t *testing.T) {
	database, err := OpenIndexDatabase(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	summary := RunSummary{RunID: "pause-test", Phase: "extract", StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := database.StartRun(context.Background(), summary); err != nil {
		t.Fatal(err)
	}
	service := &RuntimeService{Database: database, activeController: NewController(), activeSummary: &summary}
	paused := service.PauseIndex()
	if paused == nil || paused.Phase != "paused" {
		t.Fatalf("paused=%#v", paused)
	}
	status, err := database.RuntimeStatus("")
	if err != nil || status.ActiveRun == nil || status.ActiveRun.Phase != "paused" {
		t.Fatalf("paused status=%#v err=%v", status, err)
	}
	resumed := service.ResumeIndex()
	if resumed == nil || resumed.Phase != "extract" {
		t.Fatalf("resumed=%#v", resumed)
	}
}

func TestExpiredMutationLeaseCannotBeRenewed(t *testing.T) {
	database, err := OpenIndexDatabase(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	lease, err := database.TryAcquireMutationLease("expired-owner", "test", -time.Second)
	if err != nil || lease == nil {
		t.Fatalf("lease=%#v err=%v", lease, err)
	}
	if renewed, err := database.RenewMutationLease("expired-owner", time.Minute); err != nil || renewed {
		t.Fatalf("expired lease renewed=%v err=%v", renewed, err)
	}
}

func TestGoServiceSourceLifecycleDisableRetainsCacheAndRemovePurges(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "轮盘_20260902.md"), []byte("# 玩法\n\n轮盘奖励和奖池配置。"), 0o644); err != nil {
		t.Fatal(err)
	}
	source, err := CreateSourceConfig("plans", "策划案", "design", sourceRoot, true)
	if err != nil {
		t.Fatal(err)
	}
	config := CreateDefaultConfig()
	config.Sources = []Source{source}
	config.Indexing.AutomaticScan = false
	config.Indexing.Concurrency = 1
	store := NewConfigStore(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if _, err := store.SaveSnapshot(config); err != nil {
		t.Fatal(err)
	}
	service, err := NewRuntimeService(context.Background(), ServiceOptions{ConfigDir: store.ConfigDir, DataDir: store.DataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if run, err := service.Index(context.Background(), IndexOptions{Full: true}, nil); err != nil || run.Indexed != 1 {
		t.Fatalf("index=%#v err=%v", run, err)
	}

	disabled := service.Config()
	disabled.Sources[0].Enabled = false
	reconciled, err := service.ReconcileSources(context.Background(), disabled, true, nil)
	if err != nil || !containsString(reconciled.Plan.DisabledSourceIDs, "plans") || reconciled.Purged.Documents != 0 {
		t.Fatalf("disable reconciliation=%#v err=%v", reconciled, err)
	}
	status, err := service.Status()
	if err != nil || status.DocumentCount != 1 {
		t.Fatalf("disabled cache must remain: status=%#v err=%v", status, err)
	}
	search, err := service.Search.Search(context.Background(), SearchRequest{Query: "轮盘"})
	if err != nil || len(search.Hits) != 0 {
		t.Fatalf("disabled source leaked into search: %#v err=%v", search, err)
	}

	removed := service.Config()
	removed.Sources = nil
	reconciled, err = service.ReconcileSources(context.Background(), removed, false, nil)
	if err != nil || reconciled.Purged.Documents != 1 {
		t.Fatalf("remove reconciliation=%#v err=%v", reconciled, err)
	}
	status, err = service.Status()
	if err != nil || status.DocumentCount != 0 || status.ChunkCount != 0 {
		t.Fatalf("removed cache remained: status=%#v err=%v", status, err)
	}
}

func TestGoServiceReloadAndCrossProcessMutationLease(t *testing.T) {
	root := t.TempDir()
	store := NewConfigStore(filepath.Join(root, "config"), filepath.Join(root, "data"))
	config := CreateDefaultConfig()
	config.Sources = nil
	if _, err := store.SaveSnapshot(config); err != nil {
		t.Fatal(err)
	}
	service, err := NewRuntimeService(context.Background(), ServiceOptions{ConfigDir: store.ConfigDir, DataDir: store.DataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	updated := service.Config()
	updated.Indexing.ScanIntervalMinutes++
	if _, err := store.SaveSnapshot(updated); err != nil {
		t.Fatal(err)
	}
	reloaded, err := service.ReloadConfigIfChanged()
	if err != nil || !reloaded.Changed || service.Config().Indexing.ScanIntervalMinutes != updated.Indexing.ScanIntervalMinutes {
		t.Fatalf("reload=%#v err=%v", reloaded, err)
	}
	lease, err := service.Database.TryAcquireMutationLease("external-test", "external", time.Minute)
	if err != nil || lease == nil {
		t.Fatalf("lease=%#v err=%v", lease, err)
	}
	defer service.Database.ReleaseMutationLease("external-test")
	if _, err := service.Index(context.Background(), IndexOptions{}, nil); err == nil || !strings.Contains(err.Error(), "另一个进程") {
		t.Fatalf("mutation lease not enforced: %v", err)
	}
}

func TestGoServiceStatusReadsPersistedBackendMetricsAcrossProcesses(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "metrics.md"), []byte("# Metrics\n\npersisted backend metrics"), 0o644); err != nil {
		t.Fatal(err)
	}
	source, err := CreateSourceConfig("metrics", "metrics", "design", sourceRoot, true)
	if err != nil {
		t.Fatal(err)
	}
	config := CreateDefaultConfig()
	config.Sources = []Source{source}
	config.Indexing.AutomaticScan = false
	config.Indexing.Concurrency = 1
	store := NewConfigStore(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if _, err := store.SaveSnapshot(config); err != nil {
		t.Fatal(err)
	}
	writer, err := NewRuntimeService(context.Background(), ServiceOptions{ConfigDir: store.ConfigDir, DataDir: store.DataDir})
	if err != nil {
		t.Fatal(err)
	}
	if summary, err := writer.Index(context.Background(), IndexOptions{Full: true}, nil); err != nil || summary.Indexed != 1 {
		writer.Close()
		t.Fatalf("index summary=%#v err=%v", summary, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := NewRuntimeService(context.Background(), ServiceOptions{ConfigDir: store.ConfigDir, DataDir: store.DataDir, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	status, err := reader.Status()
	if err != nil {
		t.Fatal(err)
	}
	backend, ok := status.IndexBackend.(map[string]any)
	if !ok {
		t.Fatalf("unexpected backend status type: %#v", status.IndexBackend)
	}
	metrics, ok := backend["lastMetrics"].(*Metrics)
	if !ok || metrics == nil || metrics.Backend != "go" || metrics.WorkerCount != 1 {
		t.Fatalf("persisted metrics missing: %#v", backend)
	}
	lastRun, ok := backend["lastRun"].(*IndexBackendRun)
	if !ok || lastRun == nil || lastRun.RunID == "" || lastRun.Hello.BackendVersion != BackendVersion || lastRun.Hello.Platform == "" || lastRun.Metrics == nil {
		t.Fatalf("persisted backend provenance missing: %#v", backend)
	}
}

func TestReloadAppliesAtomicConfigWhileAnotherProcessHoldsMutationLease(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "private.md"), []byte("# Secret\n\nlease-visible-evidence"), 0o644); err != nil {
		t.Fatal(err)
	}
	source, err := CreateSourceConfig("plans", "plans", "design", sourceRoot, true)
	if err != nil {
		t.Fatal(err)
	}
	config := CreateDefaultConfig()
	config.Sources = []Source{source}
	config.Indexing.AutomaticScan = false
	config.Indexing.Concurrency = 1
	store := NewConfigStore(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if _, err := store.SaveSnapshot(config); err != nil {
		t.Fatal(err)
	}
	writer, err := NewRuntimeService(context.Background(), ServiceOptions{ConfigDir: store.ConfigDir, DataDir: store.DataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if summary, err := writer.Index(context.Background(), IndexOptions{Full: true}, nil); err != nil || summary.Indexed != 1 {
		t.Fatalf("index summary=%#v err=%v", summary, err)
	}
	reader, err := NewRuntimeService(context.Background(), ServiceOptions{ConfigDir: store.ConfigDir, DataDir: store.DataDir, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	lease, err := writer.Database.TryAcquireMutationLease("config-writer", "disable-source", time.Minute)
	if err != nil || lease == nil {
		t.Fatalf("lease=%#v err=%v", lease, err)
	}
	defer writer.Database.ReleaseMutationLease("config-writer")
	disabled := config
	disabled.Sources = append([]Source(nil), config.Sources...)
	disabled.Sources[0].Enabled = false
	if _, err := store.SaveSnapshot(disabled); err != nil {
		t.Fatal(err)
	}
	reloaded, err := reader.ReloadConfigIfChanged()
	if err != nil || !reloaded.Changed || !reloaded.Deferred || reader.Config().Sources[0].Enabled {
		t.Fatalf("reload=%#v err=%v", reloaded, err)
	}
	result, err := reader.Search.Search(context.Background(), SearchRequest{Query: "lease-visible-evidence"})
	if err != nil || len(result.Hits) != 0 {
		t.Fatalf("disabled source leaked while lease active: result=%#v err=%v", result, err)
	}
}

func TestClearIndexCacheRemovesAllCacheRowsAndPreservesSourceFile(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(sourceRoot, "preserved.md")
	content := []byte("# Preserve\n\ncache-clear-source-marker")
	if err := os.WriteFile(sourcePath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	source, err := CreateSourceConfig("plans", "plans", "design", sourceRoot, true)
	if err != nil {
		t.Fatal(err)
	}
	config := CreateDefaultConfig()
	config.Sources = []Source{source}
	config.Indexing.AutomaticScan = false
	config.Indexing.Concurrency = 1
	store := NewConfigStore(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if _, err := store.SaveSnapshot(config); err != nil {
		t.Fatal(err)
	}
	service, err := NewRuntimeService(context.Background(), ServiceOptions{ConfigDir: store.ConfigDir, DataDir: store.DataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if summary, err := service.Index(context.Background(), IndexOptions{Full: true}, nil); err != nil || summary.Indexed != 1 {
		t.Fatalf("index summary=%#v err=%v", summary, err)
	}
	status, err := service.ClearIndexCache(context.Background())
	if err != nil || status.DocumentCount != 0 || status.ChunkCount != 0 {
		t.Fatalf("clear status=%#v err=%v", status, err)
	}
	for _, query := range []string{
		"SELECT COUNT(*) FROM documents",
		"SELECT COUNT(*) FROM chunks",
		"SELECT COUNT(*) FROM chunks_terms",
		"SELECT COUNT(*) FROM chunks_trigram",
		"SELECT COUNT(*) FROM index_runs",
		"SELECT COUNT(*) FROM source_index_state",
		"SELECT COUNT(*) FROM index_issues",
	} {
		var count int
		if err := service.Database.db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", query, count, err)
		}
	}
	after, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	afterContent, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterContent) != string(content) || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		t.Fatalf("source file changed: before=%#v after=%#v", before, after)
	}
	if info, err := os.Stat(service.Database.Path() + "-wal"); err == nil && info.Size() != 0 {
		t.Fatalf("WAL not truncated after VACUUM: %d", info.Size())
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestServiceStartupRecoversWhenCoreDataPageIsCorrupt(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "corrupt.md"), []byte("# Corrupt\n\nstartup-health-marker"), 0o644); err != nil {
		t.Fatal(err)
	}
	source, err := CreateSourceConfig("plans", "plans", "design", sourceRoot, true)
	if err != nil {
		t.Fatal(err)
	}
	config := CreateDefaultConfig()
	config.Sources = []Source{source}
	config.Indexing.AutomaticScan = false
	config.Indexing.Concurrency = 1
	store := NewConfigStore(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if _, err := store.SaveSnapshot(config); err != nil {
		t.Fatal(err)
	}
	service, err := NewRuntimeService(context.Background(), ServiceOptions{ConfigDir: store.ConfigDir, DataDir: store.DataDir})
	if err != nil {
		t.Fatal(err)
	}
	if summary, err := service.Index(context.Background(), IndexOptions{Full: true}, nil); err != nil || summary.Indexed != 1 {
		service.Close()
		t.Fatalf("index summary=%#v err=%v", summary, err)
	}
	if err := service.Database.Checkpoint(context.Background()); err != nil {
		service.Close()
		t.Fatal(err)
	}
	var journalMode string
	if err := service.Database.db.QueryRow("PRAGMA journal_mode=DELETE").Scan(&journalMode); err != nil || strings.ToLower(journalMode) != "delete" {
		service.Close()
		t.Fatalf("journal mode=%q err=%v", journalMode, err)
	}
	healthyDatabasePath := service.Database.Path()
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	rawDatabase, err := sql.Open("sqlite", sqliteWriteDSN(healthyDatabasePath))
	if err != nil {
		t.Fatal(err)
	}
	var pageSize, rootPage int64
	if err := rawDatabase.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		rawDatabase.Close()
		t.Fatal(err)
	}
	if err := rawDatabase.QueryRow("SELECT rootpage FROM sqlite_master WHERE type='table' AND name='chunks'").Scan(&rootPage); err != nil {
		rawDatabase.Close()
		t.Fatal(err)
	}
	if err := rawDatabase.Close(); err != nil {
		t.Fatal(err)
	}
	rawBytes, err := os.ReadFile(healthyDatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(store.DataDir, "index.corrupt.sqlite")
	if err := os.WriteFile(databasePath, rawBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	pointerBytes, _ := json.Marshal(activeIndexPointer{SchemaVersion: 1, FileName: filepath.Base(databasePath), ActivatedAt: time.Now().UTC().Format(time.RFC3339Nano), Reason: "corruption-recovery"})
	if err := os.WriteFile(filepath.Join(store.DataDir, indexPointerFile), pointerBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(databasePath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	corruptPage := bytes.Repeat([]byte{0xff}, int(pageSize))
	if _, err := file.WriteAt(corruptPage, (rootPage-1)*pageSize); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewRuntimeService(context.Background(), ServiceOptions{ConfigDir: store.ConfigDir, DataDir: store.DataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if recovered.Database.Path() == databasePath {
		t.Fatal("corrupt generation was not replaced")
	}
	status, err := recovered.Status()
	if err != nil || status.DocumentCount != 0 {
		t.Fatalf("recovered status=%#v err=%v", status, err)
	}
	found := false
	for _, issue := range status.RecentIssues {
		found = found || issue.Code == "cache_corrupt_recovered" && issue.Path == databasePath
	}
	if !found {
		t.Fatalf("recovery issue missing: %#v", status.RecentIssues)
	}
}

func TestSourceReconciliationReportsCommittedConfigOnPostSaveFailure(t *testing.T) {
	root := t.TempDir()
	store := NewConfigStore(filepath.Join(root, "config"), filepath.Join(root, "data"))
	config := CreateDefaultConfig()
	config.Sources = nil
	if _, err := store.SaveSnapshot(config); err != nil {
		t.Fatal(err)
	}
	service, err := NewRuntimeService(context.Background(), ServiceOptions{ConfigDir: store.ConfigDir, DataDir: store.DataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if _, err := service.Database.db.Exec(`CREATE TRIGGER fail_pending BEFORE INSERT ON source_index_state BEGIN SELECT RAISE(FAIL,'forced pending failure'); END`); err != nil {
		t.Fatal(err)
	}
	sourceRoot := filepath.Join(root, "new-source")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	source, err := CreateSourceConfig("new-source", "new source", "design", sourceRoot, true)
	if err != nil {
		t.Fatal(err)
	}
	next := service.Config()
	next.Sources = append(next.Sources, source)
	result, err := service.ReconcileSources(context.Background(), next, false, nil)
	var committed *CommittedMutationError
	if !errors.As(err, &committed) || !result.Committed || result.Phase != "mark_sources_pending" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	saved, loadErr := store.LoadSnapshotReadOnly()
	if loadErr != nil || len(saved.Config.Sources) != 1 || saved.Config.Sources[0].ID != "new-source" || !service.Config().Sources[0].Enabled {
		t.Fatalf("committed config was not authoritative: snapshot=%#v service=%#v err=%v", saved.Config, service.Config(), loadErr)
	}
}
