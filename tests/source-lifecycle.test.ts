import assert from "node:assert/strict";
import { mkdir, rename, rm, symlink, utimes, writeFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import type { AppConfig, KnowledgeSourceConfig } from "../src/shared/contracts.js";
import { createSourceConfig } from "../src/core/config.js";
import { KnowledgeBaseService } from "../src/core/service.js";

function source(
  id: string,
  rootPath: string,
  kind: KnowledgeSourceConfig["kind"] = "design",
  enabled = true,
): KnowledgeSourceConfig {
  return {
    id,
    label: id.toUpperCase(),
    kind,
    rootPath,
    enabled,
    includeExtensions: [".md"],
    excludeDirectoryNames: [".git"],
    maxFileBytes: 1_000_000,
  };
}

async function createService(root: string, sources: KnowledgeSourceConfig[]): Promise<KnowledgeBaseService> {
  const service = await KnowledgeBaseService.create({
    configDir: path.join(root, "config"),
    dataDir: path.join(root, "data"),
  });
  await service.saveConfig({ ...service.config, sources });
  return service;
}

test("createSourceConfig 为任意 design/table 来源生成跨入口一致的默认配置", () => {
  const absoluteRoot = path.resolve(".");
  const design = createSourceConfig({ id: "design-extra", label: "历史策划", kind: "design", rootPath: absoluteRoot });
  const table = createSourceConfig({ id: "table-extra", label: "活动配表", kind: "table", rootPath: absoluteRoot, enabled: false });
  assert.equal(design.enabled, true);
  assert.equal(path.isAbsolute(design.rootPath), true);
  assert(design.includeExtensions.includes(".docx"));
  assert(!design.includeExtensions.includes(".csv"));
  assert.equal(table.enabled, false);
  assert(table.includeExtensions.includes(".csv"));
  assert(!table.includeExtensions.includes(".docx"));
  assert.notEqual(design.excludeDirectoryNames, table.excludeDirectoryNames);
  assert.throws(
    () => createSourceConfig({ id: "relative", label: "相对路径", kind: "design", rootPath: "." }),
    /必须使用绝对路径/,
  );
});
test("资料源配置允许同名显示，并拒绝重复 id、相同目录和父子重叠目录", async (context) => {
  const root = path.resolve("tests/.tmp/source-config-validation");
  const parent = path.join(root, "source");
  const child = path.join(parent, "child");
  await rm(root, { recursive: true, force: true });
  await mkdir(child, { recursive: true });
  const service = await KnowledgeBaseService.create({
    configDir: path.join(root, "config"),
    dataDir: path.join(root, "data"),
  });
  try {
    const base = service.config;
    await assert.rejects(
      service.saveConfig({ ...base, sources: [source("same", parent), source("same", path.join(root, "other"))] }),
      /资料源 id.*重复/,
    );
    await assert.rejects(
      service.saveConfig({ ...base, sources: [source("one", parent), source("two", parent)] }),
      /资料源目录不能相同或互为父子目录/,
    );
    await assert.rejects(
      service.saveConfig({ ...base, sources: [source("one", parent), source("two", child)] }),
      /资料源目录不能相同或互为父子目录/,
    );
    const alias = path.join(root, "source-alias");
    try {
      await symlink(parent, alias, process.platform === "win32" ? "junction" : "dir");
      await assert.rejects(
        service.saveConfig({ ...base, sources: [source("one", parent), source("two", alias)] }),
        /资料源目录不能相同或互为父子目录/,
      );
    } catch (error) {
      const cannotCreateLink = error instanceof Error && "code" in error && ["EPERM", "EACCES", "ENOTSUP"].includes(String(error.code));
      if (!cannotCreateLink) throw error;
      context.diagnostic("当前环境不允许创建目录链接，跳过 realpath 重叠断言");
    }
    const sibling = path.join(root, "other");
    await mkdir(sibling, { recursive: true });
    const sameLabelLeft = { ...source("label-left", parent), label: "同名来源" };
    const sameLabelRight = { ...source("label-right", sibling), label: "同名来源" };
    await service.saveConfig({ ...base, sources: [sameLabelLeft, sameLabelRight] });
    assert.deepEqual(service.config.sources.map((item) => item.label), ["同名来源", "同名来源"]);
    assert.notEqual(service.config.sources[0]?.id, service.config.sources[1]?.id);
  } finally {
    service.close();
  }
});

test("禁用来源仅屏蔽缓存，引用与版本不可绕过，重新启用执行 scoped 增量而非重建", async () => {
  const root = path.resolve("tests/.tmp/source-visibility");
  const rootA = path.join(root, "a");
  const rootB = path.join(root, "b");
  await rm(root, { recursive: true, force: true });
  await Promise.all([mkdir(rootA, { recursive: true }), mkdir(rootB, { recursive: true })]);
  const duplicateContent = "# 玩法逻辑\n\n共享机制抽奖奖励，命中后发放复用道具。";
  await Promise.all([
    writeFile(path.join(rootA, "共享玩法_20260901.md"), duplicateContent, "utf8"),
    writeFile(path.join(rootB, "共享玩法_20260801.md"), duplicateContent, "utf8"),
    writeFile(path.join(rootA, "版本活动_20260901.md"), "# 玩法\n\n版本链路测试，来源 A。", "utf8"),
    writeFile(path.join(rootB, "版本活动_20260801.md"), "# 玩法\n\n版本链路测试，来源 B。", "utf8"),
  ]);
  const enabledSources = [source("a", rootA), source("b", rootB, "table")];
  const service = await createService(root, enabledSources);
  try {
    const run = await service.index({ full: true });
    assert.equal(run.indexed, 4);

    const resultA = await service.search({ query: "共享机制抽奖奖励", sourceIds: ["a"] });
    const resultB = await service.search({ query: "共享机制抽奖奖励", sourceIds: ["b"] });
    assert.equal(resultA.hits[0]?.sourceId, "a");
    assert.equal(resultB.hits[0]?.sourceId, "b", "显式来源检索应在当前来源集合内动态去重");
    const citationA = resultA.hits[0]?.excerpts[0]?.citation.citationId;
    assert(citationA);
    const versionA = await service.search({ query: "版本链路测试", sourceIds: ["a"] });
    const familyKey = versionA.hits[0]?.familyKey;
    const documentA = versionA.hits[0]?.documentId;
    assert(familyKey && documentA);
    const snapshotsBeforeDisable = await service.inspectSources();

    const revisionBeforeDisable = service.status().indexRevision;
    const cachedRowsBeforeDisable = service.database.db.prepare(`
      SELECT id, source_identity, stale, deleted, content_hash, scan_generation
      FROM documents WHERE source_id = 'a' ORDER BY id
    `).all();
    const chunksBeforeDisable = Number(service.database.db.prepare(`
      SELECT COUNT(*) AS count FROM chunks c JOIN documents d ON d.id = c.document_id
      WHERE d.source_id = 'a'
    `).get()?.count);
    const disabledA: AppConfig = {
      ...service.config,
      sources: [source("a", rootA, "design", false), source("b", rootB, "table")],
    };
    await service.saveConfig(disabledA);
    assert.deepEqual((await service.detectSourceChanges(snapshotsBeforeDisable)).removedSourceIds, []);
    assert.equal(service.status().indexRevision, revisionBeforeDisable);
    assert.equal((await service.search({ query: "共享机制抽奖奖励", sourceIds: ["a"] })).hits.length, 0);
    assert.equal((await service.search({ query: "共享机制抽奖奖励" })).hits[0]?.sourceId, "b");
    assert((await service.retrieve({ query: "共享机制抽奖奖励" })).evidence.every((item) => !item.relativePath.includes("20260901")));
    assert.throws(() => service.readCitation(citationA), /资料源已禁用/);
    assert.throws(() => service.listVersions({ documentId: documentA }), /资料源已禁用/);
    assert(service.listVersions({ familyKey }).every((item) => item.sourceId === "b"));
    assert.equal(Number(service.database.db.prepare("SELECT COUNT(*) AS count FROM documents WHERE source_id = 'a'").get()?.count), 2);
    assert.deepEqual(service.database.db.prepare(`
      SELECT id, source_identity, stale, deleted, content_hash, scan_generation
      FROM documents WHERE source_id = 'a' ORDER BY id
    `).all(), cachedRowsBeforeDisable, "停用来源不得改写其缓存行");
    assert.equal(Number(service.database.db.prepare(`
      SELECT COUNT(*) AS count FROM chunks c JOIN documents d ON d.id = c.document_id
      WHERE d.source_id = 'a'
    `).get()?.count), chunksBeforeDisable, "停用来源不得删除 chunk/FTS 对应缓存");

    await service.saveConfig({ ...disabledA, sources: disabledA.sources.map((item) => ({ ...item, enabled: false })) });
    assert.equal((await service.search({ query: "共享机制抽奖奖励" })).hits.length, 0);

    const reenabledAll = await service.reconcileConfig({ ...disabledA, sources: enabledSources });
    assert.deepEqual(reenabledAll.plan.enabledSourceIds, ["a", "b"]);
    assert.deepEqual(reenabledAll.plan.incrementalSourceIds, ["a", "b"]);
    assert.equal(reenabledAll.indexRun?.discovered, 4);
    assert.equal(reenabledAll.indexRun?.indexed, 0);
    assert.equal(reenabledAll.indexRun?.unchanged, 4);
    assert.equal(service.status().indexRevision, revisionBeforeDisable);
    assert.equal((await service.search({ query: "共享机制抽奖奖励", sourceIds: ["a"] })).hits[0]?.sourceId, "a");
    assert.equal(service.readCitation(citationA).citation.sourceId, "a");

    await service.saveConfig(disabledA);
    const disabledChangePath = path.join(rootA, "停用期间新增_20260902.md");
    await writeFile(disabledChangePath, "# 玩法\n\n停用期间新增内容，重新启用后应通过 scoped 增量同步。", "utf8");
    assert.equal((await service.search({ query: "停用期间新增内容" })).hits.length, 0);
    const rebuiltB = await service.index({ full: true });
    assert.equal(rebuiltB.indexed, 2);
    assert.equal(Number(service.database.db.prepare("SELECT COUNT(*) AS count FROM documents WHERE source_id = 'a'").get()?.count), 2);
    const reenabledA = await service.reconcileConfig({ ...disabledA, sources: enabledSources });
    assert.deepEqual(reenabledA.plan.enabledSourceIds, ["a"]);
    assert.deepEqual(reenabledA.plan.incrementalSourceIds, ["a"]);
    assert.equal(reenabledA.indexRun?.indexed, 1);
    assert.equal(reenabledA.indexRun?.unchanged, 2);
    assert.equal((await service.search({ query: "共享机制抽奖奖励", sourceIds: ["a"] })).hits[0]?.sourceId, "a");
    assert.equal((await service.search({ query: "停用期间新增内容", sourceIds: ["a"] })).hits[0]?.sourceId, "a");

    const removedCanonical = await service.reconcileConfig({ ...disabledA, sources: [source("b", rootB, "table")] });
    assert.deepEqual(removedCanonical.plan.removedSourceIds, ["a"]);
    assert.equal((await service.search({ query: "共享机制抽奖奖励" })).hits[0]?.sourceId, "b");
  } finally {
    service.close();
  }
});

test("同目录切换来源类型会强制重提取并按新类型重新计算业务日期", async () => {
  const root = path.resolve("tests/.tmp/source-kind-identity");
  const sourceRoot = path.join(root, "source");
  await rm(root, { recursive: true, force: true });
  await mkdir(sourceRoot, { recursive: true });
  const filePath = path.join(sourceRoot, "activity.md");
  await writeFile(filePath, "# 复用记录\n\n版本 2026-08-20：调整奖励产出。", "utf8");
  const filesystemDate = new Date("2025-01-02T00:00:00.000Z");
  await utimes(filePath, filesystemDate, filesystemDate);

  const service = await createService(root, [source("same", sourceRoot, "design")]);
  try {
    const initial = await service.index({ full: true });
    assert.equal(initial.indexed, 1);
    const before = service.database.getDocumentByPath(filePath);
    assert(before);
    assert.equal(before.date_source, "version_log");
    assert.equal(before.effective_updated_at, "2026-08-20T00:00:00.000Z");

    const switched = await service.reconcileSources({
      ...service.config,
      sources: [source("same", sourceRoot, "table")],
    });
    assert.deepEqual(switched.plan.replacedSourceIds, ["same"]);
    assert.deepEqual(switched.plan.incrementalSourceIds, ["same"]);
    assert.equal(switched.indexRun?.indexed, 1);
    assert.equal(switched.indexRun?.unchanged, 0, "来源身份变化不得进入 size+mtime unchanged 快路");
    const after = service.database.getDocumentByPath(filePath);
    assert(after);
    assert.notEqual(after.source_identity, before.source_identity);
    assert.equal(after.source_kind, "table");
    assert.equal(after.date_source, "filesystem_mtime");
    assert.equal(after.effective_updated_at, filesystemDate.toISOString());
    assert.equal(Number(after.stale), 0);
    assert.equal(Number(after.deleted), 0);
  } finally {
    service.close();
  }
});

test("配置先落盘后中断时，读路径立即屏蔽旧身份且普通增量完成替换与删除恢复", async () => {
  const root = path.resolve("tests/.tmp/source-config-recovery");
  const rootA = path.join(root, "old-root");
  const rootB = path.join(root, "new-root");
  await rm(root, { recursive: true, force: true });
  await Promise.all([mkdir(rootA, { recursive: true }), mkdir(rootB, { recursive: true })]);
  const oldPath = path.join(rootA, "old.md");
  const newPath = path.join(rootB, "new.md");
  await Promise.all([
    writeFile(oldPath, "# 玩法\n\n旧来源身份不可泄漏。", "utf8"),
    writeFile(newPath, "# 玩法\n\n新来源身份自动恢复。", "utf8"),
  ]);
  const service = await createService(root, [source("recover", rootA)]);
  try {
    await service.index({ full: true });
    const oldHit = (await service.search({ query: "旧来源身份不可泄漏" })).hits[0];
    const oldCitation = oldHit?.excerpts[0]?.citation.citationId;
    assert(oldHit && oldCitation);

    // saveConfig simulates a process exit after the authoritative config was
    // replaced but before reconcileSources could mutate SQLite or run indexing.
    await service.saveConfig({ ...service.config, sources: [source("recover", rootB)] });
    assert.equal(Number(service.database.getDocumentByPath(oldPath)?.deleted), 0, "测试前置应保留未协调的旧缓存");
    assert.equal((await service.search({ query: "旧来源身份不可泄漏" })).hits.length, 0);
    assert.throws(() => service.readCitation(oldCitation), /资料源.*变更/);
    assert.throws(() => service.listVersions({ documentId: oldHit.documentId }), /资料源.*变更/);

    const recovered = await service.index();
    assert.equal(recovered.indexed, 1);
    assert.equal(Number(service.database.getDocumentByPath(oldPath)?.deleted), 1);
    assert.equal(Number(service.database.getDocumentByPath(oldPath)?.stale), 1);
    assert.equal((await service.search({ query: "新来源身份自动恢复" })).hits[0]?.sourceId, "recover");

    const current = service.database.getDocumentByPath(newPath);
    assert(current);
    service.database.putDocumentEmbedding(current.id, "test", current.content_hash, [0.2, 0.4]);
    service.database.recordIssue({
      sourceId: "recover",
      path: newPath,
      code: "pending_delete",
      message: "模拟配置删除后 purge 前退出",
      occurredAt: new Date().toISOString(),
    }, null);
    await service.saveConfig({ ...service.config, sources: [] });
    assert.equal((await service.search({ query: "新来源身份自动恢复" })).hits.length, 0);
    await service.index();
    assert.equal(Number(service.database.db.prepare("SELECT COUNT(*) AS count FROM documents WHERE source_id = 'recover'").get()?.count), 0);
    assert.equal(Number(service.database.db.prepare("SELECT COUNT(*) AS count FROM document_embeddings").get()?.count), 0);
    assert.equal(Number(service.database.db.prepare("SELECT COUNT(*) AS count FROM index_issues WHERE source_id = 'recover'").get()?.count), 0);
  } finally {
    service.close();
  }
});

test("配置落盘后 pending 写入失败仍切换内存权威配置并可由下一次增量恢复", async () => {
  const root = path.resolve("tests/.tmp/source-pending-write-failure");
  const rootA = path.join(root, "old");
  const rootB = path.join(root, "new");
  await rm(root, { recursive: true, force: true });
  await Promise.all([mkdir(rootA, { recursive: true }), mkdir(rootB, { recursive: true })]);
  await Promise.all([
    writeFile(path.join(rootA, "old.md"), "# 玩法\n\npending 写入失败旧证据。", "utf8"),
    writeFile(path.join(rootB, "new.md"), "# 玩法\n\npending 写入失败恢复。", "utf8"),
  ]);
  const service = await createService(root, [source("pending", rootA)]);
  try {
    await service.index({ full: true });
    const original = service.database.markSourceReconciliationPending.bind(service.database);
    service.database.markSourceReconciliationPending = () => { throw new Error("simulated pending failure"); };
    await assert.rejects(
      service.reconcileSources({ ...service.config, sources: [source("pending", rootB)] }),
      /simulated pending failure/,
    );
    service.database.markSourceReconciliationPending = original;
    assert.equal(service.config.sources[0]?.rootPath, rootB, "磁盘保存成功后 finally 必须应用新配置快照");
    assert.equal((await service.search({ query: "pending 写入失败旧证据" })).hits.length, 0);
    const recovered = await service.index({ sourceIds: [] });
    assert.equal(recovered.indexed, 1);
    assert.equal((await service.search({ query: "pending 写入失败恢复" })).hits[0]?.sourceId, "pending");
  } finally {
    service.close();
  }
});

test("新增来源首次不可用后，重复提交相同配置会自动重试 scoped 增量", async () => {
  const root = path.resolve("tests/.tmp/source-add-retry");
  const rootA = path.join(root, "existing");
  const delayedRoot = path.join(root, "delayed");
  await rm(root, { recursive: true, force: true });
  await mkdir(rootA, { recursive: true });
  await writeFile(path.join(rootA, "existing.md"), "# 玩法\n\n已有来源保留。", "utf8");
  const service = await createService(root, [source("a", rootA)]);
  try {
    await service.index({ full: true });
    const failedAdd = await service.reconcileSources({
      ...service.config,
      sources: [source("a", rootA), source("delayed", delayedRoot, "table")],
    });
    assert.equal(failedAdd.indexRun?.phase, "failed");
    assert.deepEqual(failedAdd.plan.incrementalSourceIds, ["delayed"]);

    await mkdir(delayedRoot, { recursive: true });
    await writeFile(path.join(delayedRoot, "delayed.md"), "# 配置\n\n延迟来源恢复成功。", "utf8");
    const retried = await service.reconcileSources(structuredClone(service.config));
    assert.deepEqual(retried.plan.addedSourceIds, []);
    assert.deepEqual(retried.plan.incrementalSourceIds, ["delayed"]);
    assert.equal(retried.indexRun?.indexed, 1);
    assert.equal((await service.search({ query: "延迟来源恢复成功", sourceIds: ["delayed"] })).hits[0]?.sourceId, "delayed");

    const emptyRoot = path.join(root, "empty-ready");
    await mkdir(emptyRoot, { recursive: true });
    const emptyAdded = await service.reconcileSources({
      ...service.config,
      sources: [...service.config.sources, source("empty", emptyRoot)],
    });
    assert.deepEqual(emptyAdded.plan.incrementalSourceIds, ["empty"]);
    assert.equal(emptyAdded.indexRun?.discovered, 0);
    assert.equal(emptyAdded.indexRun?.phase, "complete");
    const emptyRepeated = await service.reconcileSources(structuredClone(service.config));
    assert.deepEqual(emptyRepeated.plan.incrementalSourceIds, [], "成功扫描的空来源应记录 ready，不能反复触发增量");
    assert.equal(emptyRepeated.indexRun, null);
  } finally {
    service.close();
  }
});

test("停用期间修改 root/kind 不改写缓存，重新启用后才失效旧身份并 scoped 更新", async () => {
  const root = path.resolve("tests/.tmp/source-disabled-replacement");
  const rootA = path.join(root, "a");
  const rootB = path.join(root, "b");
  await rm(root, { recursive: true, force: true });
  await Promise.all([mkdir(rootA, { recursive: true }), mkdir(rootB, { recursive: true })]);
  const pathA = path.join(rootA, "cached.md");
  const pathB = path.join(rootB, "replacement.md");
  await Promise.all([
    writeFile(pathA, "# 玩法\n\n停用缓存保持原样。", "utf8"),
    writeFile(pathB, "# 配置\n\n重新启用后更新。", "utf8"),
  ]);
  const service = await createService(root, [source("paused", rootA)]);
  try {
    await service.index({ full: true });
    const before = service.database.getDocumentByPath(pathA);
    assert(before);
    await service.reconcileSources({ ...service.config, sources: [source("paused", rootA, "design", false)] });
    const changedWhileDisabled = await service.reconcileSources({
      ...service.config,
      sources: [source("paused", rootB, "table", false)],
    });
    assert.deepEqual(changedWhileDisabled.plan.replacedSourceIds, ["paused"]);
    assert.deepEqual(changedWhileDisabled.plan.incrementalSourceIds, []);
    assert.equal(changedWhileDisabled.indexRun, null);
    const stillCached = service.database.getDocumentByPath(pathA);
    assert(stillCached);
    assert.equal(stillCached.source_identity, before.source_identity);
    assert.equal(stillCached.deleted, before.deleted);
    assert.equal(stillCached.stale, before.stale);

    const reenabled = await service.reconcileSources({
      ...service.config,
      sources: [source("paused", rootB, "table", true)],
    });
    assert.deepEqual(reenabled.plan.incrementalSourceIds, ["paused"]);
    assert.equal(reenabled.indexRun?.indexed, 1);
    assert.equal(Number(service.database.getDocumentByPath(pathA)?.deleted), 1);
    assert.equal((await service.search({ query: "重新启用后更新" })).hits[0]?.sourceId, "paused");
  } finally {
    service.close();
  }
});

test("来源新增和同 ID 替换执行 scoped 增量，只有配置删除才硬删对应缓存", async () => {
  const root = path.resolve("tests/.tmp/source-reconciliation");
  const rootA = path.join(root, "a");
  const rootB = path.join(root, "b");
  const rootC = path.join(root, "c");
  await rm(root, { recursive: true, force: true });
  await Promise.all([
    mkdir(rootA, { recursive: true }),
    mkdir(rootB, { recursive: true }),
    mkdir(rootC, { recursive: true }),
  ]);
  const pathA = path.join(rootA, "alpha_20260901.md");
  const pathB = path.join(rootB, "beta_20260901.md");
  const pathC = path.join(rootC, "gamma_20260901.md");
  await Promise.all([
    writeFile(pathA, "# 玩法\n\n阿尔法来源保留内容。", "utf8"),
    writeFile(pathB, "# 配置\n\n贝塔来源新增内容。", "utf8"),
    writeFile(pathC, "# 配置\n\n伽马替换来源内容。", "utf8"),
  ]);
  const service = await createService(root, [source("a", rootA)]);
  try {
    await service.index({ full: true });
    const added = await service.reconcileSources({
      ...service.config,
      sources: [source("a", rootA), source("b", rootB, "table")],
    });
    assert.deepEqual(added.plan.addedSourceIds, ["b"]);
    assert.deepEqual(added.plan.incrementalSourceIds, ["b"]);
    assert.equal(added.indexRun?.discovered, 1);
    assert.equal(added.indexRun?.indexed, 1);
    assert.equal(added.indexRun?.unchanged, 0, "新增来源不应重新扫描已有来源");
    assert(service.database.getDocumentByPath(pathA));

    const snapshots = await service.inspectSources();
    const pathB2 = path.join(rootB, "beta-extra_20260902.md");
    await writeFile(pathB2, "# 配置\n\n贝塔来源新增的第二份内容。", "utf8");
    const detected = await service.detectSourceChanges(snapshots);
    assert.deepEqual(detected.changedSourceIds, ["b"]);
    const refreshed = await service.index({ sourceIds: detected.changedSourceIds });
    assert.equal(refreshed.discovered, 2);
    assert.equal(refreshed.indexed, 1);
    assert.equal(refreshed.unchanged, 1);

    const replaced = await service.reconcileSources({
      ...service.config,
      sources: [source("a", rootA), source("b", rootC, "table")],
    });
    assert.deepEqual(replaced.plan.replacedSourceIds, ["b"]);
    assert.deepEqual(replaced.plan.purgeSourceIds, []);
    assert.equal(replaced.purged.documents, 0);
    assert.equal(replaced.indexRun?.indexed, 1);
    assert.equal(Number(service.database.getDocumentByPath(pathB)?.deleted), 1);
    assert.equal(Number(service.database.getDocumentByPath(pathB2)?.deleted), 1);
    const gamma = service.database.getDocumentByPath(pathC);
    assert(gamma);

    const missingRoot = path.join(root, "missing-replacement");
    const unavailableReplacement = await service.reconcileSources({
      ...service.config,
      sources: [source("a", rootA), source("b", missingRoot, "table")],
    });
    assert.equal(unavailableReplacement.indexRun?.phase, "failed");
    assert.equal(unavailableReplacement.indexRun?.failed, 1);
    assert.equal(service.config.sources.find((item) => item.id === "b")?.rootPath, missingRoot);
    assert.equal(Number(service.database.getDocumentByPath(pathC)?.deleted), 1);
    assert.equal((await service.search({ query: "伽马替换来源内容", sourceIds: ["b"] })).hits.length, 0);
    assert.equal(
      Number(service.database.db.prepare("SELECT COUNT(*) AS count FROM documents WHERE source_id = 'b'").get()?.count),
      3,
      "替换来源不可访问时只软屏蔽旧缓存，不应硬删除",
    );

    service.database.putDocumentEmbedding(gamma.id, "test", gamma.content_hash, [0.1, 0.2]);
    service.database.recordIssue({
      sourceId: "b",
      path: pathC,
      code: "test_issue",
      message: "待删除来源问题",
      occurredAt: new Date().toISOString(),
    }, null);

    const revisionBeforeRemove = service.status().indexRevision;
    const removed = await service.reconcileSources({ ...service.config, sources: [source("a", rootA)] });
    assert.deepEqual(removed.plan.removedSourceIds, ["b"]);
    assert.deepEqual(removed.plan.purgeSourceIds, ["b"]);
    assert.equal(removed.purged.documents, 3);
    assert(removed.purged.chunks > 0);
    assert.equal(removed.purged.embeddings, 1);
    assert.equal(removed.purged.issues, 2, "来源删除应同时清理不可访问诊断与业务问题记录");
    assert(removed.purged.indexRevision > revisionBeforeRemove);
    assert.equal(removed.indexRun, null);
    assert.equal(service.database.getDocumentByPath(pathC), null);
    assert.equal((await service.search({ query: "阿尔法来源保留内容" })).hits[0]?.sourceId, "a");
    if (service.database.fts5Available) {
      const chunks = Number(service.database.db.prepare("SELECT COUNT(*) AS count FROM chunks").get()?.count);
      const terms = Number(service.database.db.prepare("SELECT COUNT(*) AS count FROM chunks_terms").get()?.count);
      assert.equal(terms, chunks, "删除来源后 FTS 不应遗留孤儿 rowid");
    }

    const offlinePath = `${rootA}-offline`;
    await rename(rootA, offlinePath);
    const unavailable = await service.index({ sourceIds: ["a"] });
    assert.equal(unavailable.phase, "failed");
    assert.equal(unavailable.failed, 1);
    assert.equal(Number(service.database.db.prepare("SELECT ready FROM source_index_state WHERE source_id = 'a'").get()?.ready), 0);
    assert(service.database.getDocumentByPath(pathA), "目录临时不可用时必须保留 last-good 文档");
    assert.equal((await service.search({ query: "阿尔法来源保留内容" })).hits[0]?.sourceId, "a");
    await rename(offlinePath, rootA);

    const recoveredPending = await service.index({ sourceIds: [] });
    assert.equal(recoveredPending.discovered, 1, "scoped index 必须合并缺失或 pending 的启用来源");
    assert.equal(recoveredPending.unchanged, 1);
    assert.equal(Number(service.database.db.prepare("SELECT ready FROM source_index_state WHERE source_id = 'a'").get()?.ready), 1);

    await rm(pathA);
    const emptied = await service.index({ sourceIds: ["a"] });
    assert.equal(emptied.deleted, 1, "可访问的空目录应清理已不存在的文档");
    assert.equal((await service.search({ query: "阿尔法来源保留内容" })).hits.length, 0);
  } finally {
    service.close();
  }
});
