import assert from "node:assert/strict";
import { mkdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import type { AppConfig } from "../src/shared/contracts.js";
import { KnowledgeBaseService } from "../src/core/service.js";

test("表格意图检索同时保留最新候选、核心配置表和策划案", async () => {
  const root = path.resolve("tests/.tmp/retrieval-balance");
  const designRoot = path.join(root, "design");
  const tableRoot = path.join(root, "table");
  await rm(root, { recursive: true, force: true });
  await Promise.all([mkdir(designRoot, { recursive: true }), mkdir(tableRoot, { recursive: true })]);
  await writeFile(path.join(designRoot, "扭蛋机活动_20260819.md"), "# 配置表明细\n扭蛋机需要奖池、抽取规则和活动控制配置。", "utf8");
  const tables = [
    ["rule_20260827.md", "通用规则表，包含少量扭蛋机开关。"],
    ["items_20260827.md", "通用道具表，包含扭蛋机道具。"],
    ["newLottery_20260827.md", "扭蛋机核心抽取配置表，配置轮次、消耗和奖池引用。"],
    ["constantProm_20260826.md", "通用常量表，包含扭蛋机参数。"],
    ["activityLuckdrawControl_20260820.md", "扭蛋机活动控制配置表。"],
    ["newPrizePool_20260820.md", "扭蛋机核心奖池配置表，配置奖励、权重和保底。"],
  ] as const;
  await Promise.all(tables.map(([name, content]) => writeFile(path.join(tableRoot, name), `# 配置\n${content}`, "utf8")));

  const service = await KnowledgeBaseService.create({ configDir: path.join(root, "config"), dataDir: path.join(root, "data") });
  try {
    const config: AppConfig = {
      ...service.config,
      sources: [
        {
          id: "plans",
          label: "策划案",
          kind: "design",
          rootPath: designRoot,
          enabled: true,
          includeExtensions: [".md"],
          excludeDirectoryNames: [],
          maxFileBytes: 1_000_000,
        },
        {
          id: "tables",
          label: "配表",
          kind: "table",
          rootPath: tableRoot,
          enabled: true,
          includeExtensions: [".md"],
          excludeDirectoryNames: [],
          maxFileBytes: 1_000_000,
        },
      ],
    };
    await service.saveConfig(config);
    await service.index({ full: true });
    const bundle = await service.retrieve({ query: "我要新增一个扭蛋机，需要配置哪些表格", maxDocuments: 8 });
    const titles = new Set(bundle.search.hits.map((hit) => hit.title));
    assert(titles.has("newLottery_20260827"));
    assert(titles.has("newPrizePool_20260820"));
    assert(bundle.search.hits.some((hit) => hit.sourceKind === "design"));
    assert(bundle.search.hits.filter((hit) => hit.sourceKind === "table").length >= 5);

    const exact = await service.retrieve({
      query: "newLottery newPrizePool 配置",
      maxDocuments: 8,
      maxChunksPerDocument: 3,
      maxChars: 24_000,
    });
    const exactTitles = new Set(exact.search.hits.map((hit) => hit.title));
    const evidenceTitles = new Set(exact.evidence.map((item) => item.title));
    assert(exactTitles.has("newLottery_20260827"));
    assert(exactTitles.has("newPrizePool_20260820"));
    assert(evidenceTitles.has("newLottery_20260827"), "显式文档 ID 必须进入最终 evidence");
    assert(evidenceTitles.has("newPrizePool_20260820"), "多个显式文档 ID 应按文档级召回分别保留");
  } finally {
    service.close();
  }
});

test("配表意图为中文命名活动保留 design 身份席位，同时维持 table 配额", async () => {
  const root = path.resolve("tests/.tmp/retrieval-balance-named-activity");
  const designRoot = path.join(root, "design");
  const tableRoot = path.join(root, "table");
  await rm(root, { recursive: true, force: true });
  await Promise.all([mkdir(designRoot, { recursive: true }), mkdir(tableRoot, { recursive: true })]);
  await Promise.all([
    writeFile(path.join(designRoot, "环潮龙888活动_20260722.md"), "# 配置\n其他888活动配置。", "utf8"),
    writeFile(path.join(designRoot, "艾尔芙琳888活动_20260610.md"), "# 配置\n其他888活动配置。", "utf8"),
    writeFile(path.join(designRoot, "冰王888活动_20260325.md"), "# 配置\n其他888活动配置。", "utf8"),
    writeFile(
      path.join(designRoot, "【复用】万妖王·摩哥斯888活动_20260506.md"),
      "# 配置表明细\n妖王888复用需要任务、奖励、活动时间、跳转和兑换配置。",
      "utf8",
    ),
    ...Array.from({ length: 7 }, (_, index) => writeFile(
      path.join(tableRoot, `genericConfig${index + 1}_202608${String(index + 20).padStart(2, "0")}.md`),
      `# 配置\n通用888活动配置可复用，字段参数版本 ${index + 1}。`,
      "utf8",
    )),
  ]);

  const service = await KnowledgeBaseService.create({ configDir: path.join(root, "config"), dataDir: path.join(root, "data") });
  try {
    const config: AppConfig = {
      ...service.config,
      sources: [
        {
          id: "plans", label: "策划案", kind: "design", rootPath: designRoot, enabled: true,
          includeExtensions: [".md"], excludeDirectoryNames: [], maxFileBytes: 1_000_000,
        },
        {
          id: "tables", label: "配表", kind: "table", rootPath: tableRoot, enabled: true,
          includeExtensions: [".md"], excludeDirectoryNames: [], maxFileBytes: 1_000_000,
        },
      ],
    };
    await service.saveConfig(config);
    await service.index({ full: true });
    const bundle = await service.retrieve({
      query: "我要复用妖王888，需要配哪些表，帮我把新的配置列出来",
      sort: "newest",
      maxDocuments: 8,
      maxChunksPerDocument: 3,
      maxChars: 24_000,
    });
    const identityHit = bundle.search.hits.find((hit) => /妖王.*888/.test(`${hit.title}\n${hit.relativePath}`));
    assert.equal(identityHit?.title, "【复用】万妖王·摩哥斯888活动_20260506");
    assert(bundle.evidence.some((item) => item.title === identityHit?.title), "命名活动 design 必须进入 evidence");
    assert(bundle.search.hits.filter((hit) => hit.sourceKind === "table").length >= 5, "保留命名活动身份不得吞掉 table 配额");
  } finally {
    service.close();
  }
});
