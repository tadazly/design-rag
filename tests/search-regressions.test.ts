import assert from "node:assert/strict";
import { mkdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import type { AppConfig, KnowledgeSourceConfig } from "../src/shared/contracts.js";
import { KnowledgeBaseService } from "../src/core/service.js";

function source(id: string, rootPath: string, kind: KnowledgeSourceConfig["kind"]): KnowledgeSourceConfig {
  return {
    id,
    label: id,
    kind,
    rootPath,
    enabled: true,
    includeExtensions: [".md"],
    excludeDirectoryNames: [],
    maxFileBytes: 1_000_000,
  };
}

async function configuredService(root: string, sources: KnowledgeSourceConfig[]): Promise<KnowledgeBaseService> {
  const service = await KnowledgeBaseService.create({
    configDir: path.join(root, "config"),
    dataDir: path.join(root, "data"),
  });
  const config: AppConfig = { ...service.config, sources };
  await service.saveConfig(config);
  await service.index({ full: true });
  return service;
}

test("显式 alias 在同内容 canonical 去重后仍可召回，exact 覆盖时不扫描 LIKE", async () => {
  const root = path.resolve("tests/.tmp/search-exact-alias");
  const tableRoot = path.join(root, "table");
  await rm(root, { recursive: true, force: true });
  await mkdir(tableRoot, { recursive: true });
  const identical = "# 配置\n\n完全相同的别名内容。";
  await Promise.all([
    writeFile(path.join(tableRoot, "legacyAlias_20260101.md"), identical, "utf8"),
    writeFile(path.join(tableRoot, "copy_20260831.md"), identical, "utf8"),
  ]);

  const service = await configuredService(root, [source("tables", tableRoot, "table")]);
  try {
    let likeCalls = 0;
    const originalLike = service.database.likeCandidates.bind(service.database);
    service.database.likeCandidates = (...args) => {
      likeCalls++;
      return originalLike(...args);
    };
    const result = await service.search({ query: "legacyAlias_20260101", limit: 8 });
    assert(result.hits.some((hit) => hit.title === "legacyAlias_20260101"));
    assert.equal(
      result.hits.filter((hit) => hit.title === "legacyAlias_20260101" || hit.title === "copy_20260831").length,
      1,
      "query-aware canonical 去重不得同时返回 alias 和 preferred copy",
    );
    assert.equal(likeCalls, 0, "所有显式 strong anchor 已被 exact 覆盖时不得再全表 LIKE");
  } finally {
    service.close();
  }
});

test("多个显式 strong ID 在 top8 和 evidence 中各保留代表", async () => {
  const root = path.resolve("tests/.tmp/search-multi-strong-id");
  const tableRoot = path.join(root, "table");
  await rm(root, { recursive: true, force: true });
  await mkdir(tableRoot, { recursive: true });
  await Promise.all([
    ...Array.from({ length: 9 }, (_, index) => {
      const day = String(index + 1).padStart(2, "0");
      return writeFile(
        path.join(tableRoot, `alphaStrong_202608${day}.md`),
        `# 配置\n\nalphaStrong 配置版本 ${index + 1}。`,
        "utf8",
      );
    }),
    writeFile(path.join(tableRoot, "betaStrong_20260101.md"), "# 配置\n\nbetaStrong 独立配置。", "utf8"),
  ]);

  const service = await configuredService(root, [source("tables", tableRoot, "table")]);
  try {
    const bundle = await service.retrieve({
      query: "alphaStrong betaStrong 配置",
      maxDocuments: 8,
      maxChunksPerDocument: 1,
      maxChars: 24_000,
    });
    assert(bundle.search.hits.some((hit) => hit.title.startsWith("alphaStrong_")));
    assert(bundle.search.hits.some((hit) => hit.title === "betaStrong_20260101"), "较旧的第二个显式 ID 不能被同一 ID 的新版本挤出 top8");
    assert(bundle.evidence.some((item) => item.title === "betaStrong_20260101"), "显式 ID 代表必须进入 evidence");
  } finally {
    service.close();
  }
});

test("来源 quota 选集完成后按请求 newest 做全局重排", async () => {
  const root = path.resolve("tests/.tmp/search-global-newest");
  const designRoot = path.join(root, "design");
  const tableRoot = path.join(root, "table");
  await rm(root, { recursive: true, force: true });
  await Promise.all([mkdir(designRoot, { recursive: true }), mkdir(tableRoot, { recursive: true })]);
  await Promise.all([
    writeFile(path.join(designRoot, "共同机制_20260101.md"), "# 玩法\n\n共同机制说明。", "utf8"),
    writeFile(path.join(tableRoot, "共同机制_20260831.md"), "# 数据\n\n共同机制参数。", "utf8"),
  ]);

  const service = await configuredService(root, [
    source("plans", designRoot, "design"),
    source("tables", tableRoot, "table"),
  ]);
  try {
    const bundle = await service.retrieve({ query: "共同机制", maxDocuments: 4, sort: "newest" });
    const timestamps = bundle.search.hits.map((hit) => Date.parse(hit.effectiveUpdatedAt));
    assert.deepEqual(timestamps, [...timestamps].sort((left, right) => right - left));
    assert.equal(bundle.search.hits[0]?.title, "共同机制_20260831");
  } finally {
    service.close();
  }
});

test("最新活动身份只由 title/path anchor 决定，更新更晚的表正文命中不得冒充", async () => {
  const root = path.resolve("tests/.tmp/search-latest-document-identity");
  const designRoot = path.join(root, "design");
  const tableRoot = path.join(root, "table");
  await rm(root, { recursive: true, force: true });
  await Promise.all([mkdir(designRoot, { recursive: true }), mkdir(tableRoot, { recursive: true })]);
  await Promise.all([
    writeFile(
      path.join(designRoot, "环潮龙888活动_20260101.md"),
      "# 玩法\n\n环潮龙888活动的玩法和产出逻辑。",
      "utf8",
    ),
    writeFile(
      path.join(designRoot, "冰王888活动_20251201.md"),
      "# 玩法\n\n冰王888活动的历史方案。",
      "utf8",
    ),
    writeFile(
      path.join(tableRoot, "rule_20260831.md"),
      "# 数据\n\n规则表正文包含 888，但它不是活动策划身份。",
      "utf8",
    ),
  ]);

  const service = await configuredService(root, [
    source("plans", designRoot, "design"),
    source("tables", tableRoot, "table"),
  ]);
  try {
    for (const query of ["找到最新的一个 888活动", "最近 888活动", "latest 888"]) {
      const search = await service.search({ query, sort: "newest", limit: 8 });
      assert.equal(search.hits[0]?.title, "环潮龙888活动_20260101", `${query} 的 top1 必须是 title anchor 活动`);
      assert(search.hits.every((hit) => `${hit.title}\n${hit.relativePath}`.includes("888")), `${query} 不得保留仅正文命中 888 的文档`);
      assert.deepEqual(
        search.hits.map((hit) => Date.parse(hit.effectiveUpdatedAt)),
        [...search.hits].map((hit) => Date.parse(hit.effectiveUpdatedAt)).sort((left, right) => right - left),
        `${query} 的活动身份集合必须继续按 newest 全局排序`,
      );

      const bundle = await service.retrieve({
        query,
        sort: "newest",
        maxDocuments: 8,
        maxChunksPerDocument: 2,
        maxChars: 24_000,
      });
      assert.equal(bundle.search.hits[0]?.title, "环潮龙888活动_20260101", `${query} retrieve top1 必须保持活动身份`);
      assert.equal(bundle.search.hits.some((hit) => hit.title === "rule_20260831"), false, `${query} retrieve 不得混回泛表正文命中`);
    }

    const ordinary = await service.retrieve({ query: "888活动", sort: "newest", maxDocuments: 8 });
    assert(ordinary.search.hits.some((hit) => hit.title === "rule_20260831"), "没有最新意图时不得改变既有正文召回");

    const tableIntent = await service.retrieve({ query: "最新 888 配表", sort: "newest", maxDocuments: 8 });
    assert(tableIntent.search.hits.some((hit) => hit.title === "rule_20260831"), "配表意图不得启用活动身份过滤");
  } finally {
    service.close();
  }
});

test("中文命名活动优先 title/path 身份，同日期主案不受 chunk 命中密度影响", async () => {
  const root = path.resolve("tests/.tmp/search-named-activity-identity");
  const designRoot = path.join(root, "design");
  const tableRoot = path.join(root, "table");
  await rm(root, { recursive: true, force: true });
  await Promise.all([mkdir(designRoot, { recursive: true }), mkdir(tableRoot, { recursive: true })]);
  const denseAuxiliary = Array.from({ length: 140 }, (_, index) =>
    `## 玩法内容 ${index + 1}\n环潮龙888 产出逻辑、流程、步骤、交互和机制。`).join("\n");
  await Promise.all([
    writeFile(
      path.join(designRoot, "环潮龙888活动_20260722.md"),
      "# 活动主案\n\n环潮龙888活动的主玩法与货币产出说明。",
      "utf8",
    ),
    writeFile(
      path.join(designRoot, "环潮龙888玩法内容设计_20260722.md"),
      `# 辅助玩法设计\n\n${denseAuxiliary}`,
      "utf8",
    ),
    writeFile(
      path.join(designRoot, "【复用】环潮龙888累充_20260722.md"),
      "# 累充辅助案\n\n环潮龙888累充档位配置。",
      "utf8",
    ),
    writeFile(
      path.join(tableRoot, "errorCode_20260824.md"),
      "# 提示语\n\n环潮龙888：配置不存在。这是错误码表，不是活动产出逻辑。",
      "utf8",
    ),
    writeFile(
      path.join(tableRoot, "activityTaskReset_20260827.md"),
      "# 任务\n\nid=101888 冰王累充任务；后续正文提到环潮龙888活动任务。",
      "utf8",
    ),
  ]);

  const service = await configuredService(root, [
    source("plans", designRoot, "design"),
    source("tables", tableRoot, "table"),
  ]);
  try {
    const named = await service.retrieve({
      query: "环潮龙888 产出逻辑",
      sort: "newest",
      maxDocuments: 8,
      maxChunksPerDocument: 3,
      maxChars: 24_000,
    });
    assert.equal(named.search.hits[0]?.title, "环潮龙888活动_20260722");
    assert(named.search.hits.every((hit) => /环潮龙.*888/.test(`${hit.title}\n${hit.relativePath}`)));
    assert.equal(named.search.hits.some((hit) => hit.sourceKind === "table"), false, "正文命中表不得进入非配表命名活动集合");

    const latest = await service.retrieve({
      query: "找到最新的一个 888活动，说明玩法和产出逻辑",
      sort: "newest",
      maxDocuments: 8,
      maxChunksPerDocument: 3,
      maxChars: 24_000,
    });
    assert.equal(latest.search.hits[0]?.title, "环潮龙888活动_20260722", "同日期应先比较文档身份/角色，再比较 chunk relevance");
  } finally {
    service.close();
  }
});

test("documentIds 将检索限定在已选文档，多词查询不会静默丢失候选", async () => {
  const root = path.resolve("tests/.tmp/retrieve-selected-document");
  const designRoot = path.join(root, "design");
  await rm(root, { recursive: true, force: true });
  await mkdir(designRoot, { recursive: true });
  await Promise.all([
    writeFile(
      path.join(designRoot, "目标活动_20260831.md"),
      "# 玩法产出\n\neventSummary 记录活动货币产出与兑换流程。",
      "utf8",
    ),
    writeFile(
      path.join(designRoot, "全词干扰项_20260901.md"),
      "# 配置\n\neventSummary dropUnit item statistic 玩法产出。",
      "utf8",
    ),
  ]);

  const service = await configuredService(root, [source("plans", designRoot, "design")]);
  try {
    const selected = await service.search({ query: "目标活动 eventSummary", limit: 8 });
    const documentId = selected.hits.find((hit) => hit.title === "目标活动_20260831")?.documentId;
    assert(documentId, "drag_search 应先返回可传给 drag_retrieve 的 documentId");

    const bundle = await service.retrieve({
      query: "eventSummary dropUnit item statistic 玩法产出",
      documentIds: [documentId],
      maxDocuments: 1,
      maxChunksPerDocument: 3,
      maxChars: 8_000,
    });
    assert.deepEqual(bundle.search.hits.map((hit) => hit.documentId), [documentId]);
    assert(bundle.evidence.length > 0, "documentIds 不得在全库检索后过滤成空 evidence");
    assert(bundle.evidence.every((item) => item.title === "目标活动_20260831"));
    assert(bundle.evidence.some((item) => /eventSummary|玩法产出/.test(item.content)), "仍应选择文档内与 query 相关的片段");
  } finally {
    service.close();
  }
});
