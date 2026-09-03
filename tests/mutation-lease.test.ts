import assert from "node:assert/strict";
import { mkdir, rm, utimes, writeFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import type { AppConfig, KnowledgeSourceConfig } from "../src/shared/contracts.js";
import { KnowledgeBaseService, MutationLeaseBusyError } from "../src/core/service.js";

function source(rootPath: string, enabled = true): KnowledgeSourceConfig {
  return {
    id: "plans",
    label: "策划案",
    kind: "design",
    rootPath,
    enabled,
    includeExtensions: [".md"],
    excludeDirectoryNames: [],
    maxFileBytes: 1_000_000,
  };
}

async function setupSharedServices(root: string): Promise<{
  first: KnowledgeBaseService;
  second: KnowledgeBaseService;
  sourceRoot: string;
}> {
  const sourceRoot = path.join(root, "source");
  const configDir = path.join(root, "config");
  const dataDir = path.join(root, "data");
  await rm(root, { recursive: true, force: true });
  await mkdir(sourceRoot, { recursive: true });
  await writeFile(path.join(sourceRoot, "共享索引_20260901.md"), "# 玩法\n\n跨进程 mutation lease 测试资料。", "utf8");
  const first = await KnowledgeBaseService.create({ configDir, dataDir });
  await first.saveConfig({ ...first.config, sources: [source(sourceRoot)] });
  const second = await KnowledgeBaseService.create({ configDir, dataDir });
  return { first, second, sourceRoot };
}

function isLeaseBusy(error: unknown): boolean {
  return error instanceof MutationLeaseBusyError && error.code === "MUTATION_LEASE_BUSY";
}

test("两个 KnowledgeBaseService 实例不能并发索引、保存配置或清理缓存", async () => {
  const root = path.resolve("tests/.tmp/mutation-lease-concurrency");
  const { first, second } = await setupSharedServices(root);
  let activeRun: Promise<unknown> | null = null;
  let releaseRun = () => {};
  const runBarrier = new Promise<void>((resolve) => { releaseRun = resolve; });
  let signalRunEntered = () => {};
  const runEntered = new Promise<void>((resolve) => { signalRunEntered = resolve; });
  const originalRun = first.indexer.run.bind(first.indexer);
  first.indexer.run = async (...args: Parameters<typeof originalRun>) => {
    signalRunEntered();
    await runBarrier;
    return originalRun(...args);
  };
  try {
    activeRun = first.index();
    await runEntered;
    assert(first.database.getActiveMutationLease());

    await assert.rejects(second.index(), isLeaseBusy);
    await assert.rejects(second.saveConfig(second.config), isLeaseBusy);
    await assert.rejects(second.reconcileConfig(second.config), isLeaseBusy);
    assert.throws(() => second.clearIndexCache(), isLeaseBusy);
    assert.throws(() => second.purgeSourceIndex("plans"), isLeaseBusy);

    releaseRun();
    await activeRun;
    activeRun = null;
    assert.equal(first.database.getActiveMutationLease(), null);

    const followUp = await second.index();
    assert.equal(followUp.phase, "complete");
    assert.equal(followUp.unchanged, 1);
  } finally {
    releaseRun();
    await activeRun?.catch(() => undefined);
    first.indexer.run = originalRun;
    first.close();
    second.close();
  }
});

test("mutation lease 支持 heartbeat 并可在过期后由另一个实例恢复", async () => {
  const root = path.resolve("tests/.tmp/mutation-lease-expiry");
  const { first, second } = await setupSharedServices(root);
  try {
    const now = Date.now();
    assert(first.database.tryAcquireMutationLease("owner-one", "stale-index", 50, now));
    assert.equal(second.database.tryAcquireMutationLease("owner-two", "clear-cache", 50, now + 25), null);
    const recovered = second.database.tryAcquireMutationLease("owner-two", "clear-cache", 50, now + 51);
    assert.equal(recovered?.ownerId, "owner-two");
    assert.equal(second.database.renewMutationLease("owner-two", 50, now + 70), true);
    const active = first.database.getActiveMutationLease(now + 110);
    assert.equal(active?.ownerId, "owner-two");
    assert.equal(active?.heartbeatAtMs, now + 70);
    assert.equal(second.database.releaseMutationLease("owner-two"), true);
  } finally {
    first.close();
    second.close();
  }
});

test("index 获取 lease 后会重载磁盘配置，不会扫描另一个进程已停用的旧来源", async () => {
  const root = path.resolve("tests/.tmp/index-config-reload");
  const { first, second, sourceRoot } = await setupSharedServices(root);
  try {
    await first.saveConfig({ ...first.config, sources: [source(sourceRoot, false)] });
    assert.equal(second.config.sources[0]?.enabled, true);
    const result = await second.index();
    assert.equal(result.discovered, 0);
    assert.equal(second.config.sources[0]?.enabled, false);
  } finally {
    first.close();
    second.close();
  }
});

test("reloadConfigIfChanged 使用内容 hash 刷新外部配置，并在 mutation 期间延后应用", async () => {
  const root = path.resolve("tests/.tmp/config-reload");
  const { first, second, sourceRoot } = await setupSharedServices(root);
  try {
    await first.index({ full: true });
    assert.equal(second.config.sources[0]?.enabled, true);

    const disabledConfig: AppConfig = { ...first.config, sources: [source(sourceRoot, false)] };
    await first.saveConfig(disabledConfig);
    assert.equal(second.config.sources[0]?.enabled, true, "另一个实例在 reload 前应保持自己的快照");
    const disabledReload = await second.reloadConfigIfChanged();
    assert.equal(disabledReload.changed, true);
    assert.equal(disabledReload.deferred, false);
    assert.equal(second.config.sources[0]?.enabled, false);
    assert.equal((await second.search({ query: "跨进程 mutation lease" })).hits.length, 0);

    const enabledConfig: AppConfig = { ...disabledConfig, sources: [source(sourceRoot, true)] };
    await first.saveConfig(enabledConfig);
    const originalTime = new Date(disabledReload.fingerprint.mtimeMs);
    await utimes(first.configStore.configPath, originalTime, originalTime);
    const hashReload = await second.reloadConfigIfChanged();
    assert.equal(hashReload.changed, true, "mtime 被恢复时仍必须通过内容 hash 发现变化");
    assert.equal(second.config.sources[0]?.enabled, true);

    const ownerId = "external-config-writer";
    assert(first.database.tryAcquireMutationLease(ownerId, "reconcile-config", 60_000));
    await first.configStore.saveSnapshot(disabledConfig);
    const deferred = await second.reloadConfigIfChanged();
    assert.equal(deferred.changed, false);
    assert.equal(deferred.deferred, true);
    assert.equal(second.config.sources[0]?.enabled, true);
    assert.equal(first.database.releaseMutationLease(ownerId), true);

    const applied = await second.reloadConfigIfChanged();
    assert.equal(applied.changed, true);
    assert.equal(applied.deferred, false);
    assert.equal(second.config.sources[0]?.enabled, false);
  } finally {
    first.close();
    second.close();
  }
});
