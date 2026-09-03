package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	mutationLeaseTTL       = 5 * time.Minute
	mutationHeartbeat      = 10 * time.Second
	indexPointerFile       = "index.active.json"
	indexRecoveryLockFile  = "index.recovery.lock"
	indexRecoveryLockStale = 30 * time.Second
	indexRecoveryWait      = 10 * time.Second
)

var errMutationLeaseLost = errors.New("mutation lease lost")

type ServiceOptions struct {
	ConfigDir string
	DataDir   string
	ReadOnly  bool
}

type ConfigReloadResult struct {
	Changed     bool                  `json:"changed"`
	Deferred    bool                  `json:"deferred"`
	Config      AppConfig             `json:"config"`
	Fingerprint ConfigFileFingerprint `json:"fingerprint"`
}

type SourceReconciliationPlan struct {
	AddedSourceIDs       []string `json:"addedSourceIds"`
	RemovedSourceIDs     []string `json:"removedSourceIds"`
	ReplacedSourceIDs    []string `json:"replacedSourceIds"`
	ModifiedSourceIDs    []string `json:"modifiedSourceIds"`
	EnabledSourceIDs     []string `json:"enabledSourceIds"`
	DisabledSourceIDs    []string `json:"disabledSourceIds"`
	PurgeSourceIDs       []string `json:"purgeSourceIds"`
	IncrementalSourceIDs []string `json:"incrementalSourceIds"`
}

type SourceReconciliationResult struct {
	Committed bool                     `json:"committed"`
	Phase     string                   `json:"phase"`
	Config    AppConfig                `json:"config"`
	Plan      SourceReconciliationPlan `json:"plan"`
	Purged    PurgeSourcesResult       `json:"purged"`
	IndexRun  *RunSummary              `json:"indexRun"`
}

type CommittedMutationError struct {
	Phase string
	Cause error
}

func (err *CommittedMutationError) Error() string {
	return fmt.Sprintf("配置已原子提交，但后续阶段 %s 失败: %v", err.Phase, err.Cause)
}

func (err *CommittedMutationError) Unwrap() error { return err.Cause }

type RuntimeService struct {
	ConfigStore *ConfigStore
	Database    *IndexDatabase
	Search      *SearchEngine

	configMutex       sync.RWMutex
	config            AppConfig
	configFingerprint ConfigFileFingerprint
	mutationMutex     sync.Mutex
	activeMutex       sync.RWMutex
	activeController  *Controller
	activeSummary     *RunSummary
	pausedPhase       string
	lastMetrics       *Metrics
	closed            bool
}

type activeIndexPointer struct {
	SchemaVersion int    `json:"schemaVersion"`
	FileName      string `json:"fileName"`
	ActivatedAt   string `json:"activatedAt"`
	Reason        string `json:"reason"`
}

func isSafeIndexFileName(value string) bool {
	return filepath.Base(value) == value && regexp.MustCompile(`(?i)^index(?:\.[a-z0-9-]+)?\.sqlite$`).MatchString(value)
}

func resolveActiveDatabasePath(dataDir string) (string, error) {
	pointerPath := filepath.Join(dataDir, indexPointerFile)
	raw, err := os.ReadFile(pointerPath)
	if errors.Is(err, os.ErrNotExist) {
		return filepath.Join(dataDir, "index.sqlite"), nil
	}
	if err != nil {
		return "", err
	}
	var pointer activeIndexPointer
	if err := json.Unmarshal(raw, &pointer); err != nil || pointer.SchemaVersion != 1 || !isSafeIndexFileName(pointer.FileName) {
		return "", fmt.Errorf("索引指针无效：%s", pointerPath)
	}
	return filepath.Join(dataDir, pointer.FileName), nil
}

func writeAtomicBytes(directory, destination string, raw []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(directory, ".atomic-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := file.Name()
	cleanup := func() { _ = os.Remove(temporaryPath) }
	if err = file.Chmod(mode); err == nil {
		_, err = file.Write(raw)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		cleanup()
		return err
	}
	if err = replaceFileAtomic(temporaryPath, destination); err != nil {
		cleanup()
		return err
	}
	return nil
}

func isCorruptDatabaseError(err error) bool {
	if err == nil {
		return false
	}
	return regexp.MustCompile(`(?i)(database disk image is malformed|database corruption|file is not a database|SQLITE_CORRUPT|SQLITE_NOTADB)`).MatchString(err.Error())
}

func recoverCorruptDatabase(ctx context.Context, dataDir, failedPath string) (*IndexDatabase, string, error) {
	lockPath := filepath.Join(dataDir, indexRecoveryLockFile)
	deadline := time.Now().Add(indexRecoveryWait)
	for time.Now().Before(deadline) {
		current, err := resolveActiveDatabasePath(dataDir)
		if err != nil {
			return nil, "", err
		}
		if current != failedPath {
			database, err := OpenIndexDatabase(current)
			return database, failedPath, err
		}
		lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(lock, `{"pid":%d,"createdAt":%q}`, os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
			_ = lock.Close()
			defer os.Remove(lockPath)
			fileName := fmt.Sprintf("index.recovered-%d.sqlite", time.Now().UTC().UnixNano())
			newPath := filepath.Join(dataDir, fileName)
			database, openErr := OpenIndexDatabase(newPath)
			if openErr != nil {
				return nil, "", openErr
			}
			pointer := activeIndexPointer{SchemaVersion: 1, FileName: fileName, ActivatedAt: time.Now().UTC().Format(time.RFC3339Nano), Reason: "corruption-recovery"}
			raw, _ := json.MarshalIndent(pointer, "", "  ")
			raw = append(raw, '\n')
			if writeErr := writeAtomicBytes(dataDir, filepath.Join(dataDir, indexPointerFile), raw, 0o600); writeErr != nil {
				database.Close()
				return nil, "", writeErr
			}
			return database, failedPath, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, "", err
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > indexRecoveryLockStale {
			_ = os.Remove(lockPath)
			continue
		}
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil, "", fmt.Errorf("等待索引损坏恢复超时")
}

func NewRuntimeService(ctx context.Context, options ServiceOptions) (*RuntimeService, error) {
	store := NewConfigStore(options.ConfigDir, options.DataDir)
	var snapshot ConfigSnapshot
	var err error
	if options.ReadOnly {
		snapshot, err = store.LoadSnapshotReadOnly()
	} else {
		snapshot, err = store.LoadSnapshot()
	}
	if err != nil {
		return nil, err
	}
	databasePath, err := resolveActiveDatabasePath(store.DataDir)
	if err != nil {
		return nil, err
	}
	var database *IndexDatabase
	if options.ReadOnly {
		database, err = OpenIndexDatabaseReadOnly(databasePath)
	} else {
		database, err = OpenIndexDatabase(databasePath)
	}
	if err == nil {
		if healthErr := database.ProbeReadHealth(ctx, store.ConfigPath); healthErr != nil {
			_ = database.Close()
			database = nil
			err = healthErr
		}
	}
	if err != nil && !options.ReadOnly && isCorruptDatabaseError(err) {
		var failedPath string
		database, failedPath, err = recoverCorruptDatabase(ctx, store.DataDir, databasePath)
		if err == nil {
			_ = database.RecordSystemIssue(failedPath, "cache_corrupt_recovered", "检测到损坏的本地检索缓存，已切换到新的索引文件；旧缓存保持原样，请重新建立索引。")
			if healthErr := database.ProbeReadHealth(ctx, store.ConfigPath); healthErr != nil {
				_ = database.Close()
				database = nil
				err = healthErr
			}
		}
	}
	if err != nil {
		return nil, err
	}
	service := &RuntimeService{ConfigStore: store, Database: database, config: snapshot.Config, configFingerprint: snapshot.Fingerprint}
	service.Search = NewSearchEngine(database, service.Config)
	service.Search.refreshConfig = func() error {
		_, err := service.ReloadConfigIfChanged()
		return err
	}
	if !options.ReadOnly {
		if err := database.InitializeSourceStateBaseline(ctx, snapshot.Config.Sources); err != nil {
			database.Close()
			return nil, err
		}
	}
	return service, nil
}

func cloneConfig(config AppConfig) AppConfig {
	raw, _ := json.Marshal(config)
	var result AppConfig
	_ = json.Unmarshal(raw, &result)
	return result
}

func (service *RuntimeService) Config() AppConfig {
	config, _ := service.currentConfigSnapshot()
	return config
}

func (service *RuntimeService) ConfigWithFingerprint() (AppConfig, ConfigFileFingerprint) {
	return service.currentConfigSnapshot()
}

func (service *RuntimeService) currentConfigSnapshot() (AppConfig, ConfigFileFingerprint) {
	service.configMutex.RLock()
	defer service.configMutex.RUnlock()
	return cloneConfig(service.config), service.configFingerprint
}

func (service *RuntimeService) applyConfigSnapshot(snapshot ConfigSnapshot) {
	service.configMutex.Lock()
	service.config = snapshot.Config
	service.configFingerprint = snapshot.Fingerprint
	service.configMutex.Unlock()
}

func (service *RuntimeService) ReloadConfigIfChanged() (ConfigReloadResult, error) {
	lease, err := service.Database.ActiveMutationLease()
	if err != nil {
		return ConfigReloadResult{}, err
	}
	deferred := lease != nil
	fingerprint, err := service.ConfigStore.Fingerprint()
	if err != nil {
		return ConfigReloadResult{}, err
	}
	service.configMutex.RLock()
	currentHash := service.configFingerprint.SHA256
	service.configMutex.RUnlock()
	if fingerprint.SHA256 == currentHash {
		service.configMutex.Lock()
		service.configFingerprint = fingerprint
		service.configMutex.Unlock()
		return ConfigReloadResult{Deferred: deferred, Config: service.Config(), Fingerprint: fingerprint}, nil
	}
	snapshot, err := service.ConfigStore.LoadSnapshot()
	if err != nil {
		return ConfigReloadResult{}, err
	}
	changed := snapshot.Fingerprint.SHA256 != currentHash
	if changed {
		service.applyConfigSnapshot(snapshot)
	}
	return ConfigReloadResult{Changed: changed, Deferred: deferred, Config: service.Config(), Fingerprint: snapshot.Fingerprint}, nil
}

func sourceReplacement(previous, next Source) bool {
	return previous.Kind != next.Kind || CanonicalPathKey(previous.RootPath) != CanonicalPathKey(next.RootPath)
}

func sourceMetadataChanged(previous, next Source) bool {
	return previous.Label != next.Label || previous.MaxFileBytes != next.MaxFileBytes || normalizedStringSet(previous.IncludeExtensions) != normalizedStringSet(next.IncludeExtensions) || normalizedStringSet(previous.ExcludeDirectoryNames) != normalizedStringSet(next.ExcludeDirectoryNames)
}

func PlanSourceReconciliation(previous, next AppConfig) SourceReconciliationPlan {
	plan := SourceReconciliationPlan{AddedSourceIDs: []string{}, RemovedSourceIDs: []string{}, ReplacedSourceIDs: []string{}, ModifiedSourceIDs: []string{}, EnabledSourceIDs: []string{}, DisabledSourceIDs: []string{}, PurgeSourceIDs: []string{}, IncrementalSourceIDs: []string{}}
	previousByID, nextByID := map[string]Source{}, map[string]Source{}
	for _, source := range previous.Sources {
		previousByID[source.ID] = source
	}
	for _, source := range next.Sources {
		nextByID[source.ID] = source
		old, ok := previousByID[source.ID]
		if !ok {
			plan.AddedSourceIDs = append(plan.AddedSourceIDs, source.ID)
			continue
		}
		if sourceReplacement(old, source) {
			plan.ReplacedSourceIDs = append(plan.ReplacedSourceIDs, source.ID)
		} else if sourceMetadataChanged(old, source) {
			plan.ModifiedSourceIDs = append(plan.ModifiedSourceIDs, source.ID)
		}
		if !old.Enabled && source.Enabled {
			plan.EnabledSourceIDs = append(plan.EnabledSourceIDs, source.ID)
		}
		if old.Enabled && !source.Enabled {
			plan.DisabledSourceIDs = append(plan.DisabledSourceIDs, source.ID)
		}
	}
	for _, source := range previous.Sources {
		if _, ok := nextByID[source.ID]; !ok {
			plan.RemovedSourceIDs = append(plan.RemovedSourceIDs, source.ID)
		}
	}
	plan.PurgeSourceIDs = append(plan.PurgeSourceIDs, plan.RemovedSourceIDs...)
	for _, id := range append(append(append(append([]string{}, plan.AddedSourceIDs...), plan.ReplacedSourceIDs...), plan.ModifiedSourceIDs...), plan.EnabledSourceIDs...) {
		if source, ok := nextByID[id]; ok && source.Enabled {
			plan.IncrementalSourceIDs = appendUnique(plan.IncrementalSourceIDs, id)
		}
	}
	return plan
}

type leaseAction func(context.Context, func() error) (any, error)

func (service *RuntimeService) withMutationLease(ctx context.Context, operation string, action leaseAction) (any, error) {
	if !service.mutationMutex.TryLock() {
		return nil, fmt.Errorf("另一个进程正在更新配置或索引，当前操作 %s 暂不能开始", operation)
	}
	defer service.mutationMutex.Unlock()
	ownerID := fmt.Sprintf("%d:%s", os.Getpid(), newRunID())
	lease, err := service.Database.TryAcquireMutationLease(ownerID, operation, mutationLeaseTTL)
	if err != nil {
		return nil, err
	}
	if lease == nil {
		active, _ := service.Database.ActiveMutationLease()
		if active != nil {
			return nil, fmt.Errorf("另一个进程正在执行 %s，当前操作 %s 暂不能开始", active.Operation, operation)
		}
		return nil, fmt.Errorf("另一个进程正在更新配置或索引，当前操作 %s 暂不能开始", operation)
	}
	defer service.Database.ReleaseMutationLease(ownerID)
	leaseContext, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	renew := func() error {
		ok, err := service.Database.RenewMutationLease(ownerID, mutationLeaseTTL)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w：跨进程 mutation lease 已失效，当前操作已取消", errMutationLeaseLost)
		}
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(mutationHeartbeat)
		defer ticker.Stop()
		lastSuccessfulRenewal := time.Now()
		for {
			select {
			case <-leaseContext.Done():
				return
			case <-ticker.C:
				if err := renew(); err != nil {
					// The index writer can legitimately hold SQLite's single writer
					// slot across a large batch. A transient SQLITE_BUSY must not be
					// confused with another process taking the lease. The five-minute
					// lease TTL leaves ample time to retry after the batch commits.
					if errors.Is(err, errMutationLeaseLost) || time.Since(lastSuccessfulRenewal) >= mutationLeaseTTL/2 {
						cancel(err)
						return
					}
					continue
				}
				lastSuccessfulRenewal = time.Now()
			}
		}
	}()
	result, actionErr := action(leaseContext, renew)
	cancel(actionErr)
	<-done
	if actionErr != nil {
		return result, actionErr
	}
	if cause := context.Cause(leaseContext); cause != nil && !errors.Is(cause, context.Canceled) {
		return nil, cause
	}
	if err := renew(); err != nil {
		return nil, err
	}
	return result, nil
}

func (service *RuntimeService) SaveConfig(ctx context.Context, config AppConfig) (AppConfig, error) {
	result, err := service.withMutationLease(ctx, "save-config", func(leaseContext context.Context, renew func() error) (any, error) {
		if err := renew(); err != nil {
			return nil, err
		}
		if err := leaseContext.Err(); err != nil {
			return nil, err
		}
		snapshot, err := service.ConfigStore.SaveSnapshot(config)
		if err == nil {
			service.applyConfigSnapshot(snapshot)
		}
		return snapshot.Config, err
	})
	if err != nil {
		return AppConfig{}, err
	}
	return result.(AppConfig), nil
}

func (service *RuntimeService) runIndex(ctx context.Context, config AppConfig, options IndexOptions, progress ProgressSink) (RunSummary, Metrics, error) {
	controller := NewController()
	service.activeMutex.Lock()
	service.activeController = controller
	service.activeSummary = nil
	service.activeMutex.Unlock()
	defer func() {
		service.activeMutex.Lock()
		service.activeController = nil
		service.activeSummary = nil
		service.pausedPhase = ""
		service.activeMutex.Unlock()
	}()
	onProgress := func(summary RunSummary) {
		service.activeMutex.Lock()
		copy := cloneSummary(summary)
		if controller.IsPaused() {
			service.pausedPhase = copy.Phase
			copy.Phase = "paused"
		}
		service.activeSummary = &copy
		service.activeMutex.Unlock()
		if progress != nil {
			progress(summary)
		}
	}
	summary, metrics, err := RunIndex(ctx, IndexRequest{DatabasePath: service.Database.path, Config: ConfigForIndex(config), Options: options}, controller, nil, onProgress)
	service.activeMutex.Lock()
	metricsCopy := metrics
	service.lastMetrics = &metricsCopy
	service.activeMutex.Unlock()
	if persistErr := service.Database.SetIndexBackendRun(map[string]any{"runId": summary.RunID, "hello": map[string]any{"protocolVersion": ProtocolVersion, "backendVersion": BackendVersion, "platform": runtime.GOOS, "arch": runtime.GOARCH, "capabilities": RuntimeCapabilities()}, "metrics": metrics}); persistErr != nil && err == nil {
		err = fmt.Errorf("索引已完成，但无法持久化 backend 指标: %w", persistErr)
	}
	return summary, metrics, err
}

func RuntimeCapabilities() []string {
	return []string{"scan", "extract", "chunk", "sqlite", "fts5", "incremental", "last-good", "pause-resume", "cancel", "progress", "native-format-compatibility", "search", "retrieve", "citation", "versions", "mcp"}
}

func (service *RuntimeService) Index(ctx context.Context, options IndexOptions, progress ProgressSink) (RunSummary, error) {
	result, err := service.withMutationLease(ctx, "index", func(leaseContext context.Context, _ func() error) (any, error) {
		snapshot, err := service.ConfigStore.LoadSnapshot()
		if err != nil {
			return nil, err
		}
		service.applyConfigSnapshot(snapshot)
		if len(options.SourceIDs) == 0 {
			options.SourceIDs = nil
		} else {
			requested := uniqueStrings(options.SourceIDs)
			enabled := map[string]bool{}
			for _, source := range snapshot.Config.Sources {
				if source.Enabled {
					enabled[source.ID] = true
				}
			}
			invalid := []string{}
			for _, sourceID := range requested {
				if !enabled[sourceID] {
					invalid = append(invalid, sourceID)
				}
			}
			if len(invalid) > 0 {
				sort.Strings(invalid)
				return nil, fmt.Errorf("sourceIds 包含不存在或未启用的资料源：%s", strings.Join(invalid, ", "))
			}
			options.SourceIDs = requested
		}
		reconciliation, err := service.Database.ReconcileSourceConfiguration(leaseContext, snapshot.Config.Sources)
		if err != nil {
			return nil, err
		}
		if !options.Full && options.SourceIDs != nil {
			options.SourceIDs = uniqueStrings(append(options.SourceIDs, reconciliation.RecoverySourceIDs...))
		}
		summary, _, err := service.runIndex(leaseContext, snapshot.Config, options, progress)
		return summary, err
	})
	if err != nil {
		if summary, ok := result.(RunSummary); ok {
			return summary, err
		}
		return RunSummary{}, err
	}
	return result.(RunSummary), nil
}

func (service *RuntimeService) ReconcileSources(ctx context.Context, config AppConfig, runIncremental bool, progress ProgressSink) (SourceReconciliationResult, error) {
	return service.reconcileSources(ctx, config, "", runIncremental, progress)
}

func (service *RuntimeService) ReconcileSourcesCAS(ctx context.Context, config AppConfig, expectedFingerprintSHA256 string, runIncremental bool, progress ProgressSink) (SourceReconciliationResult, error) {
	return service.reconcileSources(ctx, config, expectedFingerprintSHA256, runIncremental, progress)
}

func (service *RuntimeService) reconcileSources(ctx context.Context, config AppConfig, expectedFingerprintSHA256 string, runIncremental bool, progress ProgressSink) (SourceReconciliationResult, error) {
	validated, err := ValidateConfig(config)
	if err != nil {
		return SourceReconciliationResult{}, err
	}
	result, err := service.withMutationLease(ctx, "reconcile-config", func(leaseContext context.Context, renew func() error) (any, error) {
		current, err := service.ConfigStore.LoadSnapshot()
		if err != nil {
			return nil, err
		}
		service.applyConfigSnapshot(current)
		if expectedFingerprintSHA256 != "" && current.Fingerprint.SHA256 != expectedFingerprintSHA256 {
			return nil, fmt.Errorf("配置已被其他进程修改，本次来源变更已取消，请重新加载后重试")
		}
		plan := PlanSourceReconciliation(current.Config, validated)
		service.activeMutex.RLock()
		running := service.activeController != nil
		service.activeMutex.RUnlock()
		if running && (len(plan.PurgeSourceIDs) > 0 || runIncremental && len(plan.IncrementalSourceIDs) > 0) {
			return nil, fmt.Errorf("索引任务运行期间不能添加、更新、重新启用或删除资料源，请等待当前任务结束")
		}
		if err := renew(); err != nil {
			return nil, err
		}
		if err := leaseContext.Err(); err != nil {
			return nil, err
		}
		saved, err := service.ConfigStore.SaveSnapshot(validated)
		if err != nil {
			return nil, err
		}
		// The saved config becomes read-path authority immediately. Database
		// reconciliation may continue, but removed/disabled/replaced sources must
		// not remain queryable after the atomic config write succeeds.
		service.applyConfigSnapshot(saved)
		response := SourceReconciliationResult{Committed: true, Phase: "config_saved", Config: saved.Config, Plan: plan}
		committedFailure := func(phase string, cause error) (any, error) {
			response.Phase = phase
			return response, &CommittedMutationError{Phase: phase, Cause: cause}
		}
		pending := []Source{}
		for _, source := range saved.Config.Sources {
			if containsString(plan.IncrementalSourceIDs, source.ID) {
				pending = append(pending, sourceWithIdentity(source))
			}
		}
		if err := service.Database.MarkSourceScansPending(leaseContext, pending); err != nil {
			return committedFailure("mark_sources_pending", err)
		}
		response.Phase = "sources_pending"
		if err := renew(); err != nil {
			return committedFailure("renew_before_database_reconciliation", err)
		}
		databasePlan, err := service.Database.ReconcileSourceConfiguration(leaseContext, saved.Config.Sources)
		if err != nil {
			return committedFailure("database_reconciliation", err)
		}
		plan.IncrementalSourceIDs = uniqueStrings(append(plan.IncrementalSourceIDs, databasePlan.RecoverySourceIDs...))
		response.Plan = plan
		response.Purged = databasePlan.Purged
		response.Phase = "database_reconciled"
		if runIncremental && len(plan.IncrementalSourceIDs) > 0 {
			summary, _, err := service.runIndex(leaseContext, saved.Config, IndexOptions{SourceIDs: plan.IncrementalSourceIDs}, progress)
			response.IndexRun = &summary
			if err != nil {
				return committedFailure("incremental_index", err)
			}
		}
		response.Phase = "complete"
		return response, nil
	})
	if err != nil {
		if reconciliation, ok := result.(SourceReconciliationResult); ok {
			return reconciliation, err
		}
		return SourceReconciliationResult{}, err
	}
	return result.(SourceReconciliationResult), nil
}

func (service *RuntimeService) PauseIndex() *RunSummary {
	service.activeMutex.Lock()
	controller, summary := service.activeController, service.activeSummary
	if controller != nil {
		controller.Pause()
	}
	var result *RunSummary
	if summary != nil {
		copy := cloneSummary(*summary)
		if copy.Phase != "paused" {
			service.pausedPhase = copy.Phase
		}
		copy.Phase = "paused"
		service.activeSummary = &copy
		result = &copy
	}
	service.activeMutex.Unlock()
	if summary == nil {
		return nil
	}
	_ = service.Database.UpdateRun(context.Background(), *result)
	return result
}

func (service *RuntimeService) ResumeIndex() *RunSummary {
	service.activeMutex.Lock()
	controller, summary := service.activeController, service.activeSummary
	if controller != nil {
		controller.Resume()
	}
	var result *RunSummary
	if summary != nil {
		copy := cloneSummary(*summary)
		if copy.Phase == "paused" {
			copy.Phase = service.pausedPhase
			if copy.Phase == "" {
				copy.Phase = "extract"
			}
		}
		service.activeSummary = &copy
		result = &copy
	}
	service.pausedPhase = ""
	service.activeMutex.Unlock()
	if summary == nil {
		return nil
	}
	_ = service.Database.UpdateRun(context.Background(), *result)
	return result
}

func (service *RuntimeService) IsIndexRunning() bool {
	service.activeMutex.RLock()
	defer service.activeMutex.RUnlock()
	return service.activeController != nil
}

func (service *RuntimeService) Status() (RuntimeIndexStatus, error) {
	status, err := service.Database.RuntimeStatus(service.ConfigStore.ConfigPath)
	if err != nil {
		return status, err
	}
	persisted, err := service.Database.GetIndexBackendRun()
	if err != nil {
		return status, err
	}
	service.activeMutex.RLock()
	if service.activeSummary != nil {
		copy := cloneSummary(*service.activeSummary)
		status.ActiveRun = &copy
	}
	metrics := service.lastMetrics
	if metrics == nil && persisted != nil {
		metrics = persisted.Metrics
	}
	running := service.activeController != nil
	service.activeMutex.RUnlock()
	binaryPath, _ := os.Executable()
	status.IndexBackend = map[string]any{"engine": "go", "binaryPath": binaryPath, "running": running, "pid": func() any {
		if running {
			return os.Getpid()
		}
		return nil
	}(), "protocolVersion": ProtocolVersion, "backendVersion": BackendVersion, "platform": runtime.GOOS, "arch": runtime.GOARCH, "capabilities": RuntimeCapabilities(), "lastMetrics": metrics, "lastRun": persisted}
	return status, nil
}

func (service *RuntimeService) ClearIndexCache(ctx context.Context) (RuntimeIndexStatus, error) {
	result, err := service.withMutationLease(ctx, "clear-index-cache", func(leaseContext context.Context, _ func() error) (any, error) {
		if service.IsIndexRunning() {
			return nil, fmt.Errorf("索引任务运行期间不能删除本地检索缓存，请先等待任务结束")
		}
		if err := service.Database.ClearLocalCache(leaseContext); err != nil {
			return nil, err
		}
		return service.Status()
	})
	if err != nil {
		return RuntimeIndexStatus{}, err
	}
	// withMutationLease releases its row after ClearLocalCache's internal
	// VACUUM/checkpoint. Truncate once more so that lease cleanup itself does not
	// leave a new WAL segment behind.
	if err := service.Database.Checkpoint(ctx); err != nil {
		return RuntimeIndexStatus{}, err
	}
	return result.(RuntimeIndexStatus), nil
}

func (service *RuntimeService) Close() error {
	service.configMutex.Lock()
	if service.closed {
		service.configMutex.Unlock()
		return nil
	}
	service.closed = true
	service.configMutex.Unlock()
	return service.Database.Close()
}

func sortedSourceIDs(values []string) []string {
	result := uniqueStrings(values)
	sort.Strings(result)
	return result
}
