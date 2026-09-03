import assert from "node:assert/strict";
import { mkdir, rm, stat, writeFile, utimes } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import type { AppConfig } from "../src/shared/contracts.js";
import { KnowledgeBaseService } from "../src/core/service.js";

const root = path.resolve("tests/.tmp/integration");
const sourceRoot = path.join(root, "source");

async function setupService(): Promise<KnowledgeBaseService> {
  await rm(root, { recursive: true, force: true });
  await mkdir(sourceRoot, { recursive: true });
  const commonMtime = new Date("2026-08-04T00:00:00Z");
  const files = [
    ["designer-a_【复用】幸运轮盘_精灵_20260819.md", "# 版本修改记录\n\n20260311 初版\n\n20260819 复用精灵奖励\n\n# 配置表明细\n\nturntable dropId activityExchange module 498"],
    ["designer-a_【复用】幸运轮盘_精灵_20260805.md", "# 版本修改记录\n\n20260805 复用精灵和皮肤\n\n# 面板&逻辑\n\n入口、抽奖、奖励发放、结果展示"],
    ["designer-a_暑期勋章兑好礼+转盘_20260819.md", "# 玩法规则\n\n充值送勋章并参与转盘，不属于幸运轮盘复用线。"],
    ["未来功能_20261231.md", "# 玩法规则\n\n这是一个普通抽奖活动配置。"],
    ["环潮龙888活动_20260722.md", "# 玩法&逻辑\n\n四个大关，击败敌人产出活动货币并兑换奖励。"],
  ] as const;
  for (const [name, content] of files) {
    const filePath = path.join(sourceRoot, name);
    await writeFile(filePath, content, "utf8");
    await utimes(filePath, commonMtime, commonMtime);
  }
  await mkdir(path.join(sourceRoot, ".svn", "text-base"), { recursive: true });
  await writeFile(path.join(sourceRoot, ".svn", "text-base", "轮盘.md.svn-base"), "不应进入索引", "utf8");
  await writeFile(path.join(sourceRoot, "~$轮盘锁文件.md"), "不应进入索引", "utf8");

  const service = await KnowledgeBaseService.create({ configDir: path.join(root, "config"), dataDir: path.join(root, "data") });
  const config: AppConfig = {
    ...service.config,
    sources: [{
      id: "plans",
      label: "策划案",
      kind: "design",
      rootPath: sourceRoot,
      enabled: true,
      includeExtensions: [".md"],
      excludeDirectoryNames: [".svn", ".git", "node_modules"],
      maxFileBytes: 10 * 1024 * 1024,
    }],
    indexing: { ...service.config.indexing, concurrency: 2 },
  };
  await service.saveConfig(config);
  return service;
}

test("完整索引、中文检索、newest 排序、引用回读与增量零重解析", async () => {
  const service = await setupService();
  try {
    const firstRun = await service.index({ full: true });
    assert.equal(firstRun.phase, "complete");
    assert.equal(firstRun.indexed, 5);
    assert.equal(service.status().documentCount, 5);
    assert.equal(service.status().fts5Available, true);

    const result = await service.search({ query: "我想复用轮盘抽奖活动", sort: "newest", limit: 10 });
    assert(result.hits.length >= 2);
    assert(result.hits[0]?.title.includes("20260819"));
    assert(["filename", "path"].includes(result.hits[0]?.dateSource ?? ""));
    assert(Date.parse(result.hits[0]?.effectiveUpdatedAt ?? "") >= Date.parse(result.hits[1]?.effectiveUpdatedAt ?? ""));
    assert(!result.hits.some((hit) => hit.relativePath.includes(".svn")));

    const citationId = result.hits[0]?.excerpts[0]?.citation.citationId;
    assert(citationId);
    const citation = service.readCitation(citationId);
    assert(citation.content.length > 0);
    assert.equal(citation.citation.citationId, citationId);
    assert.equal(citation.changed, false);
    assert.equal(citation.citation.sourceLink.fileName, path.basename(citation.citation.absolutePath));
    assert.match(citation.citation.sourceLink.markdown, /^\[.+\]\(<.+>\) · `.+`$/);
    assert(citation.citation.sourceLink.markdown.includes(citation.citation.locator));
    assert(!citation.citation.sourceLink.markdown.includes("DRAG:chunk_"));

    const latestOnly = await service.search({ query: "幸运轮盘复用", sort: "newest", latestPerFamily: true, limit: 10 });
    assert.equal(latestOnly.hits.filter((hit) => hit.familyKey.includes("幸运轮盘")).length, 1);

    const numericEntity = await service.search({ query: "找到最新 888 活动的玩法逻辑", sort: "newest", limit: 10 });
    assert.equal(numericEntity.hits[0]?.title, "环潮龙888活动_20260722");
    assert(numericEntity.hits.every((hit) => /888/.test(`${hit.title} ${hit.excerpts.map((excerpt) => excerpt.text).join(" ")}`)));

    const bundle = await service.retrieve({ query: "轮盘配置和历史改动", maxChars: 4_000 });
    assert.equal(bundle.kind, "drag_retrieval_bundle_v1");
    assert(bundle.evidence.length > 0);
    assert(bundle.evidence.every((item) => item.citationId.startsWith("DRAG:")));
    assert(bundle.evidence.every((item) => item.absolutePath.length > 0 && item.sourceLink.markdown.includes(item.locator)));

    const secondRun = await service.index();
    assert.equal(secondRun.indexed, 0);
    assert.equal(secondRun.unchanged, 5);

    const touchedPath = path.join(sourceRoot, "designer-a_【复用】幸运轮盘_精灵_20260819.md");
    const touchedAt = new Date("2026-08-31T01:02:03.456Z");
    await utimes(touchedPath, touchedAt, touchedAt);
    const touchedRun = await service.index();
    assert.equal(touchedRun.indexed, 0);
    assert.equal(touchedRun.unchanged, 5);
    const stored = service.database.getDocumentByPath(touchedPath);
    const touchedStat = await stat(touchedPath);
    assert.equal(Number(stored?.filesystem_mtime_ms), Math.trunc(touchedStat.mtimeMs));
    assert(Number.isInteger(Number(stored?.filesystem_mtime_ms)));

    const rebuilt = await service.index({ full: true });
    assert.equal(rebuilt.indexed, 5);
    assert.equal(rebuilt.failed, 0);
    assert.equal(service.status().documentCount, 5);

    let requestedPause = false;
    let observedPaused = false;
    const pausedRun = service.index({
      full: true,
      onProgress: (progress) => {
        if (!requestedPause && progress.phase === "extract") {
          requestedPause = true;
          const paused = service.pauseIndex();
          assert.equal(paused?.phase, "paused");
        }
        if (progress.phase === "paused") observedPaused = true;
      },
    });
    while (!observedPaused) await new Promise((resolve) => setTimeout(resolve, 5));
    assert.equal(service.status().activeRun?.phase, "paused");
    assert.throws(() => service.clearIndexCache(), /索引任务运行期间/);
    assert.equal(service.resumeIndex()?.phase, "extract");
    const resumed = await pausedRun;
    assert.equal(resumed.phase, "complete");
    assert.equal(resumed.indexed, 5);

    const revisionBeforeClear = service.status().indexRevision;
    const cleared = service.clearIndexCache();
    assert.equal(cleared.documentCount, 0);
    assert.equal(cleared.chunkCount, 0);
    assert.equal(cleared.lastRun, null);
    assert(cleared.indexRevision > revisionBeforeClear);
    assert.equal((await service.search({ query: "幸运轮盘" })).hits.length, 0);
  } finally {
    service.close();
  }
});
