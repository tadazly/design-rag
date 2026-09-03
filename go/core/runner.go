package core

import (
	"context"
	"crypto/rand"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type ProgressSink func(RunSummary)

type runnerTask struct {
	candidate Candidate
	existing  *ExistingDocument
}

func newRunID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

func cloneSummary(summary RunSummary) RunSummary {
	result := summary
	if summary.FinishedAt != nil {
		value := *summary.FinishedAt
		result.FinishedAt = &value
	}
	if summary.CurrentPath != nil {
		value := *summary.CurrentPath
		result.CurrentPath = &value
	}
	if summary.Error != nil {
		value := *summary.Error
		result.Error = &value
	}
	return result
}

func selectedSources(config AppConfig, options IndexOptions) []Source {
	wanted := map[string]struct{}{}
	for _, sourceID := range options.SourceIDs {
		wanted[sourceID] = struct{}{}
	}
	result := []Source{}
	for _, source := range config.Sources {
		if !source.Enabled {
			continue
		}
		if len(options.SourceIDs) > 0 {
			if _, ok := wanted[source.ID]; !ok {
				continue
			}
		}
		result = append(result, source)
	}
	return result
}

func sourceIDs(sources []Source) []string {
	result := make([]string, len(sources))
	for index, source := range sources {
		result[index] = source.ID
	}
	return result
}

func startMetricSampler(ctx context.Context, metrics *Metrics, mutex *sync.Mutex) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		previousAt := time.Now()
		_, previousCPU := processSnapshot()
		for {
			var memory runtime.MemStats
			runtime.ReadMemStats(&memory)
			workingSet, cpuTime := processSnapshot()
			now := time.Now()
			elapsedMS := now.Sub(previousAt).Milliseconds()
			cpuPercent := 0.0
			if elapsedMS > 0 && cpuTime >= previousCPU {
				cpuPercent = float64(cpuTime-previousCPU) / float64(elapsedMS) * 100
			}
			mutex.Lock()
			metrics.PeakHeapAllocBytes = max(metrics.PeakHeapAllocBytes, memory.HeapAlloc)
			metrics.PeakHeapSystemBytes = max(metrics.PeakHeapSystemBytes, memory.HeapSys)
			metrics.PeakGoroutines = max(metrics.PeakGoroutines, runtime.NumGoroutine())
			metrics.PeakWorkingSetBytes = max(metrics.PeakWorkingSetBytes, workingSet)
			metrics.CPUTimeMS = cpuTime
			metrics.PeakCPUPercent = max(metrics.PeakCPUPercent, cpuPercent)
			mutex.Unlock()
			previousAt = now
			previousCPU = cpuTime
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { <-done }
}

func RunIndex(
	ctx context.Context,
	request IndexRequest,
	controller *Controller,
	fallback FallbackProvider,
	onProgress ProgressSink,
) (summary RunSummary, metrics Metrics, returnedError error) {
	started := time.Now()
	summary = RunSummary{RunID: newRunID(), Phase: "discover", StartedAt: started.UTC().Format(time.RFC3339Nano)}
	metrics = Metrics{Backend: "go", BackendVersion: BackendVersion, ProtocolVersion: ProtocolVersion}
	metricContext, stopMetrics := context.WithCancel(context.Background())
	var metricMutex sync.Mutex
	waitMetrics := startMetricSampler(metricContext, &metrics, &metricMutex)
	defer func() {
		stopMetrics()
		waitMetrics()
		metrics.WallClockMS = time.Since(started).Milliseconds()
		processed := summary.Indexed + summary.Unchanged + summary.Failed
		if metrics.WallClockMS > 0 {
			metrics.DocumentsPerSecond = float64(processed) / (float64(metrics.WallClockMS) / 1000)
		}
	}()

	publish := func() {
		if onProgress != nil {
			onProgress(cloneSummary(summary))
		}
	}
	database, err := OpenIndexDatabase(request.DatabasePath)
	if err != nil {
		return summary, metrics, err
	}
	defer database.Close()
	if err = database.ConfigureIndexing(ctx, request.Options.Full); err != nil {
		return summary, metrics, err
	}
	if err = database.StartRun(ctx, summary); err != nil {
		return summary, metrics, err
	}
	failFatal := func(cause error) (RunSummary, Metrics, error) {
		message := cause.Error()
		summary.Phase = "failed"
		summary.Error = &message
		finished := time.Now().UTC().Format(time.RFC3339Nano)
		summary.FinishedAt = &finished
		summary.CurrentPath = nil
		_ = database.UpdateRun(context.Background(), summary)
		publish()
		return summary, metrics, cause
	}

	sources := selectedSources(request.Config, request.Options)
	for _, source := range sources {
		if strings.TrimSpace(source.IndexIdentity) == "" {
			return failFatal(fmt.Errorf("来源 %s 缺少 indexIdentity，host/backend 协议不兼容", source.ID))
		}
	}
	if err = database.MarkSourceScansPending(ctx, sources); err != nil {
		return failFatal(err)
	}
	if _, err = database.InvalidateSourceIdentities(ctx, sources); err != nil {
		return failFatal(err)
	}
	discoveryStarted := time.Now()
	discovered := DiscoverSources(ctx, sources)
	metrics.DiscoverMS = time.Since(discoveryStarted).Milliseconds()
	if err := ctx.Err(); err != nil {
		return failFatal(err)
	}
	candidates := []Candidate{}
	successfulSourceIDs := []string{}
	sourceIssues := []Issue{}
	for _, result := range discovered {
		summary.Skipped += result.Skipped
		if result.Err != nil {
			summary.Failed++
			sourceIssues = append(sourceIssues, Issue{SourceID: result.Source.ID, Path: result.Source.RootPath, Code: "source_unavailable", Message: result.Err.Error()})
			continue
		}
		successfulSourceIDs = append(successfulSourceIDs, result.Source.ID)
		candidates = append(candidates, result.Candidates...)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		leftFallback := candidates[i].Extension == ".xls" || candidates[i].Extension == ".pdf"
		rightFallback := candidates[j].Extension == ".xls" || candidates[j].Extension == ".pdf"
		return leftFallback && !rightFallback
	})
	summary.Discovered = len(candidates)
	summary.Phase = "extract"
	if len(sourceIssues) > 0 {
		if err = database.RecordIssues(ctx, summary.RunID, sourceIssues); err != nil {
			return failFatal(err)
		}
	}
	existing, err := database.LoadExisting(ctx, sourceIDs(sources))
	if err != nil {
		return failFatal(err)
	}
	cleanRebuild := false
	if request.Options.Full && summary.Failed == 0 && len(sources) > 0 && len(successfulSourceIDs) == len(sources) {
		purged, purgeErr := database.PurgeSources(ctx, sourceIDs(sources))
		if purgeErr != nil {
			err = purgeErr
			return failFatal(err)
		}
		summary.Deleted += int(purged)
		if err = database.PrepareCleanRebuild(ctx); err != nil {
			return failFatal(err)
		}
		cleanRebuild = true
		if err = database.MarkSourceScansPending(ctx, sources); err != nil {
			return failFatal(err)
		}
	}
	if err = database.UpdateRun(ctx, summary); err != nil {
		return failFatal(err)
	}
	publish()

	workerCount := request.Config.Indexing.Concurrency
	if workerCount <= 0 {
		workerCount = runtime.GOMAXPROCS(0)
	}
	workerCount = max(1, min(workerCount, 64))
	metrics.WorkerCount = workerCount
	workContext, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	tasks := make(chan runnerTask, workerCount*2)
	results := make(chan TaskResult, workerCount*2)
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for task := range tasks {
				if err := controller.Wait(workContext); err != nil {
					return
				}
				select {
				case results <- ProcessCandidate(workContext, task.candidate, task.existing, request.Options.Full, fallback):
				case <-workContext.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(tasks)
		for _, candidate := range candidates {
			if err := controller.Wait(workContext); err != nil {
				return
			}
			current := existing[CanonicalPathKey(candidate.AbsolutePath)]
			if current != nil && current.SourceIdentity != candidate.SourceIdentity {
				current = nil
			}
			item := runnerTask{candidate: candidate, existing: current}
			select {
			case tasks <- item:
			case <-workContext.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	extractStarted := time.Now()
	var writer *batchWriter
	batchDocuments := 0
	var batchStartChunks int64
	lastPublish := time.Time{}
	beginBatch := func() error {
		if writer != nil {
			return nil
		}
		var beginErr error
		writer, beginErr = database.BeginBatch(ctx, cleanRebuild)
		if beginErr == nil {
			batchStartChunks = writer.chunksWritten
			batchDocuments = 0
		}
		return beginErr
	}
	commitBatch := func() error {
		if writer == nil {
			return nil
		}
		writeStarted := time.Now()
		metrics.ChunksWritten += writer.chunksWritten - batchStartChunks
		if err := writer.Commit(ctx); err != nil {
			return err
		}
		writer = nil
		metrics.SQLiteWriteMS += time.Since(writeStarted).Milliseconds()
		if err := database.UpdateRun(ctx, summary); err != nil {
			return err
		}
		return nil
	}
	for result := range results {
		if err := ctx.Err(); err != nil {
			if writer != nil {
				writer.Rollback()
			}
			return failFatal(err)
		}
		if err := beginBatch(); err != nil {
			cancelWork()
			return failFatal(err)
		}
		current := result.Candidate.AbsolutePath
		summary.CurrentPath = &current
		metricMutex.Lock()
		metrics.BytesRead += result.BytesRead
		metrics.WorkerTaskMSTotal += result.ElapsedMS
		metrics.MaxTaskMS = max(metrics.MaxTaskMS, result.ElapsedMS)
		if result.Fallback {
			metrics.FallbackDocuments++
			metrics.FallbackTaskMSTotal += result.ElapsedMS
		}
		metricMutex.Unlock()
		writeStarted := time.Now()
		if writeErr := writer.Write(ctx, summary.RunID, summary.RunID, result); writeErr != nil {
			writer.Rollback()
			writer = nil
			cancelWork()
			return failFatal(writeErr)
		}
		metrics.SQLiteWriteMS += time.Since(writeStarted).Milliseconds()
		batchDocuments++
		switch {
		case result.Unchanged:
			summary.Unchanged++
		case result.Issue != nil:
			summary.Failed++
		case result.Draft != nil:
			summary.Indexed++
		}
		if batchDocuments >= 256 || writer.chunksWritten-batchStartChunks >= 25_000 || len(results) == 0 {
			if err = commitBatch(); err != nil {
				cancelWork()
				return failFatal(err)
			}
		}
		if time.Since(lastPublish) >= 120*time.Millisecond {
			lastPublish = time.Now()
			publish()
		}
	}
	if err = commitBatch(); err != nil {
		return failFatal(err)
	}
	metrics.ExtractAndIndexMS = time.Since(extractStarted).Milliseconds()
	if err := ctx.Err(); err != nil {
		return failFatal(err)
	}

	finalizeStarted := time.Now()
	summary.Phase = "index"
	publish()
	deleted, err := database.MarkMissingDeleted(ctx, summary.RunID, successfulSourceIDs)
	if err != nil {
		return failFatal(err)
	}
	summary.Deleted += deleted
	if err = database.ReconcileDuplicates(ctx); err != nil {
		return failFatal(err)
	}
	if err = database.MarkSourceScansReady(ctx, sources, successfulSourceIDs, summary.RunID); err != nil {
		return failFatal(err)
	}
	metrics.FinalizeMS = time.Since(finalizeStarted).Milliseconds()
	if summary.Failed > 0 && summary.Indexed+summary.Unchanged == 0 && (summary.Discovered > 0 || (len(sources) > 0 && len(successfulSourceIDs) == 0)) {
		message := "所有发现的文档都索引失败"
		summary.Phase = "failed"
		summary.Error = &message
	} else {
		summary.Phase = "complete"
	}
	finished := time.Now().UTC().Format(time.RFC3339Nano)
	summary.FinishedAt = &finished
	summary.CurrentPath = nil
	if err = database.UpdateRun(ctx, summary); err != nil {
		return failFatal(err)
	}
	if err = database.Checkpoint(ctx); err != nil {
		return failFatal(fmt.Errorf("SQLite WAL checkpoint 失败: %w", err))
	}
	publish()
	return summary, metrics, nil
}
