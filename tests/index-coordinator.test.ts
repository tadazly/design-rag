import assert from "node:assert/strict";
import test from "node:test";
import { createDefaultConfig } from "../src/core/config.js";
import { MutationLeaseBusyError, type KnowledgeBaseService } from "../src/core/service.js";
import type { AppConfig, AppEvent, IndexRunSummary } from "../src/shared/contracts.js";
import { ChatController } from "../src/main/chat-controller.js";
import { IndexCoordinator } from "../src/main/index-coordinator.js";

function completedRun(): IndexRunSummary {
  return {
    runId: "test-run",
    phase: "complete",
    startedAt: new Date().toISOString(),
    finishedAt: new Date().toISOString(),
    discovered: 2,
    indexed: 1,
    unchanged: 1,
    skipped: 0,
    failed: 0,
    deleted: 0,
    currentPath: null,
    error: null,
  };
}

async function waitFor(predicate: () => boolean, timeoutMs = 2_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!predicate() && Date.now() < deadline) await new Promise((resolve) => setTimeout(resolve, 10));
  assert(predicate(), "等待异步协调状态超时");
}

test("IndexCoordinator 遇到跨进程 lease busy 会合并期间的新事件并退避重试", async () => {
  const config = createDefaultConfig();
  config.indexing.automaticScan = true;
  let calls = 0;
  const firstControl: { reject?: (error: Error) => void } = {};
  let resolveFirstStarted: (() => void) | null = null;
  const firstStarted = new Promise<void>((resolve) => { resolveFirstStarted = resolve; });
  const receivedSourceIds: Array<string[] | undefined> = [];
  const knowledge = {
    config,
    indexer: { isRunning: false },
    index: (options: { sourceIds?: string[] }) => {
      calls += 1;
      receivedSourceIds.push(options.sourceIds);
      if (calls === 1) {
        resolveFirstStarted?.();
        return new Promise<IndexRunSummary>((_resolve, reject) => { firstControl.reject = reject; });
      }
      return Promise.resolve(completedRun());
    },
  } as unknown as KnowledgeBaseService;
  const errors: string[] = [];
  let snapshots = 0;
  const coordinator = new IndexCoordinator(knowledge, {
    onProgress: () => undefined,
    onNotice: () => undefined,
    onSnapshot: () => { snapshots += 1; },
    onError: (message) => errors.push(message),
  });
  try {
    coordinator.request("scheduled", ["source-a"]);
    await firstStarted;
    coordinator.request("scheduled", ["source-b"]);
    assert(firstControl.reject);
    firstControl.reject(new MutationLeaseBusyError("index", "index"));
    await waitFor(() => calls === 2);
    await waitFor(() => snapshots === 1);
    assert.deepEqual(new Set(receivedSourceIds[1]), new Set(["source-a", "source-b"]));
    assert.deepEqual(errors, [], "lease busy 属于可恢复竞争，不应显示为永久错误");
  } finally {
    coordinator.stop();
  }
});

test("ChatController 来源协调已完成 scoped index 后不会重复排入 IndexCoordinator", async () => {
  const previous = createDefaultConfig();
  previous.indexing.automaticScan = true;
  const next: AppConfig = structuredClone(previous);
  const indexRun = completedRun();
  const requests: Array<{ reason: string; sourceIds: string[] }> = [];
  const refreshes: boolean[] = [];
  const fakeController = {
    knowledge: {
      config: previous,
      reconcileConfig: async () => ({
        config: next,
        plan: {
          addedSourceIds: [],
          removedSourceIds: [],
          replacedSourceIds: [],
          modifiedSourceIds: [],
          enabledSourceIds: ["plans"],
          disabledSourceIds: [],
          purgeSourceIds: [],
          incrementalSourceIds: ["plans"],
        },
        purged: {
          sourceIds: [], documents: 0, chunks: 0, embeddings: 0, issues: 0, indexRevision: 1,
        },
        indexRun,
      }),
    },
    indexCoordinator: {
      refresh: (runInitial: boolean) => refreshes.push(runInitial),
      request: (reason: string, sourceIds: string[]) => requests.push({ reason, sourceIds }),
    },
    publish: () => undefined,
    snapshot: () => ({}),
  };

  const saved = await ChatController.prototype.reconcileConfig.call(
    fakeController as unknown as ChatController,
    next,
  );
  assert.equal(saved, next);
  assert.deepEqual(refreshes, [false]);
  assert.deepEqual(requests, [], "core reconcile 已完成增量索引，不应再次请求相同来源");
});

test("ChatController 对增量失败显示警告，删除无缓存来源也会明确提示", async () => {
  const config = createDefaultConfig();
  const events: AppEvent[] = [];
  const plan = {
    addedSourceIds: [] as string[],
    removedSourceIds: [] as string[],
    replacedSourceIds: [] as string[],
    modifiedSourceIds: [] as string[],
    enabledSourceIds: [] as string[],
    disabledSourceIds: [] as string[],
    purgeSourceIds: [] as string[],
    incrementalSourceIds: [] as string[],
  };
  const purged = { sourceIds: [] as string[], documents: 0, chunks: 0, embeddings: 0, issues: 0, indexRevision: 1 };
  let response: unknown = {
    config,
    plan: { ...plan, addedSourceIds: ["missing"], incrementalSourceIds: ["missing"] },
    purged,
    indexRun: { ...completedRun(), phase: "failed", failed: 1, error: "来源目录不可访问" },
  };
  const fakeController = {
    knowledge: { config, reconcileConfig: async () => response },
    indexCoordinator: { refresh: () => undefined },
    publish: (event: AppEvent) => events.push(event),
    snapshot: () => ({}),
  };

  await ChatController.prototype.reconcileConfig.call(fakeController as unknown as ChatController, config);
  const failedNotice = events.findLast((event) => event.type === "notice");
  assert(failedNotice && failedNotice.type === "notice");
  assert.equal(failedNotice.notice.kind, "warning");
  assert.match(failedNotice.notice.title, /未完整完成/);
  assert.match(failedNotice.notice.message, /来源目录不可访问/);

  events.length = 0;
  response = {
    config,
    plan: { ...plan, removedSourceIds: ["empty"], purgeSourceIds: ["empty"] },
    purged: { ...purged, sourceIds: ["empty"] },
    indexRun: null,
  };
  await ChatController.prototype.reconcileConfig.call(fakeController as unknown as ChatController, config);
  const removedNotice = events.findLast((event) => event.type === "notice");
  assert(removedNotice && removedNotice.type === "notice");
  assert.equal(removedNotice.notice.kind, "index-updated");
  assert.equal(removedNotice.notice.title, "来源已删除");
  assert.match(removedNotice.notice.message, /源文件未被修改/);
});
