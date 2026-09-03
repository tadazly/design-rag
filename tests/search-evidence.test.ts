import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdir, rm } from "node:fs/promises";
import test from "node:test";
import path from "node:path";
import { IndexDatabase } from "../src/core/database.js";
import { sourceIndexIdentity } from "../src/core/paths.js";
import { makeExcerpt, queryAnchorSignals, SearchEngine } from "../src/core/search.js";
import type { AppConfig, KnowledgeSourceConfig } from "../src/shared/contracts.js";

test("查询信号提取中文活动实体组但不把泛 888 活动或显式表 ID 混为实体", () => {
  assert.deepEqual(queryAnchorSignals("环潮龙888 产出逻辑").identityGroups, [
    { phrase: "环潮龙888", terms: ["环潮龙", "888"] },
  ]);
  assert.deepEqual(queryAnchorSignals("我要复用妖王888，需要配哪些表").identityGroups, [
    { phrase: "妖王888", terms: ["妖王", "888"] },
  ]);
  assert.deepEqual(queryAnchorSignals("找到最新的一个 888活动").identityGroups, []);
  assert.deepEqual(queryAnchorSignals("newLottery newPrizePool 配置").identityGroups, []);
});

test("表格检索投影保留字段名并把 locator 收窄到命中行窗口", () => {
  const rows = Array.from({ length: 192 }, (_, index) => {
    const row = index + 1;
    const value = row === 150 ? "unique_target" : `value_${row}`;
    return `行 ${row} | A=${row} | B=普通 | C=${value} | D=100`;
  });
  const text = [
    "字段 | A=ID | B=类型 | C=奖励ID | D=权重",
    ...rows,
  ].join("\n");
  const projection = makeExcerpt(text, "newPrizePool!A1:D192", ["unique_target"]);
  assert.match(projection.text, /^字段映射（投影）/);
  assert.doesNotMatch(projection.text, /字段（行 \d+）/, "不得把 chunk 起始行伪装成真实表头行");
  assert.match(projection.text, /C=奖励ID/);
  assert.match(projection.text, /行 150 .*C=unique_target/);
  assert.equal(projection.locator, "newPrizePool!A148:D152");
  assert(projection.text.length <= 520);
  assert(!projection.text.includes("行 1 | A=1"), "命中窗口不应回退为块首 520 字符");
});

test("表格投影优先强查询词、保留无表头稀疏列并严格遵守 maxLength", () => {
  const rows = Array.from({ length: 192 }, (_, index) => {
    const row = index + 193;
    if (row === 194) return `行 ${row} | A=${row} | B=配置 | C=普通`;
    if (row === 342) return `行 ${row} | A=${row} | B=普通 | Z=unique_target_${"x".repeat(120)}`;
    return `行 ${row} | A=${row} | B=普通 | C=value_${row}`;
  });
  const projection = makeExcerpt([
    "字段 | A=ID | B=类型 | C=普通字段",
    ...rows,
  ].join("\n"), "newPrizePool!A193:Z384", ["unique_target", "配置"], 180);

  assert.match(projection.text, /Z=unique_target/);
  assert.match(projection.text, /Z=未命名字段/);
  assert.match(projection.locator, /!A342:Z342$/);
  assert(projection.text.length <= 180, `projection length=${projection.text.length}`);
});

test("表格投影优先完整命名实体而不是更早出现的泛数字", () => {
  const rows = Array.from({ length: 80 }, (_, index) => {
    const row = index + 1;
    if (row === 2) return `行 ${row} | A=101888 | B=冰王累充任务 | C=累计充值388元`;
    if (row === 70) return `行 ${row} | A=102416 | B=环潮龙888活动 | C=击败敌人产出活动货币`;
    return `行 ${row} | A=${100000 + row} | B=普通活动 | C=普通配置`;
  });
  const projection = makeExcerpt([
    "字段 | A=ID | B=活动名称 | C=任务描述",
    ...rows,
  ].join("\n"), "activityTaskReset!A1:C80", ["888", "环潮龙888", "环潮龙"]);

  assert.match(projection.text, /环潮龙888活动/);
  assert.doesNotMatch(projection.text, /冰王累充任务/);
  assert.equal(projection.locator, "activityTaskReset!A68:C72");
});

test("投影 citation 回读同一 locator 和内容，旧 chunk citation 继续回读整块", async () => {
  const root = path.resolve("tests/.tmp/scoped-citation");
  const sourceRoot = path.join(root, "table");
  await rm(root, { recursive: true, force: true });
  await mkdir(sourceRoot, { recursive: true });
  const source: KnowledgeSourceConfig = {
    id: "tables",
    label: "配表",
    kind: "table",
    rootPath: sourceRoot,
    enabled: true,
    includeExtensions: [".xlsx"],
    excludeDirectoryNames: [],
    maxFileBytes: 1_000_000,
  };
  const config: AppConfig = {
    schemaVersion: 1,
    sources: [source],
    search: {
      defaultSort: "newest",
      defaultLimit: 20,
      maxEvidenceChars: 24_000,
      synonymExpansion: true,
      embedding: {
        enabled: false,
        provider: "ollama",
        model: "nomic-embed-text",
        endpoint: "http://127.0.0.1:11434",
        timeoutMs: 2_000,
      },
    },
    indexing: {
      automaticScan: false,
      concurrency: 1,
      scanIntervalMinutes: 10,
    },
    codex: {
      codexPath: null,
      model: null,
      reasoningEffort: null,
    },
  };
  const rows = Array.from({ length: 192 }, (_, index) => {
    const row = index + 193;
    if (row === 222) return `行 ${row} | A=newLottery | B=222 | C=扭蛋机重做抽奖配置 | I=winningProbability | N=扭蛋机素材目录`;
    if (row === 223) return `行 ${row} | A=newPrizePool | B=223 | C=扭蛋机重做奖池配置 | I=weight | N=奖池素材目录`;
    return `行 ${row} | A=table${row} | B=${row} | C=普通配置 | N=普通素材目录`;
  });
  const chunkText = [
    "字段 | A=表名 | B=ID | C=类型名称 | D=子健 | E=合并到索引表 | F=发布时删除 | G=结束时间过滤 | H=多维表关联 | I=敏感字段 | J=有效性验证 | K=转换为html格式的字段 | L=宏定义命名空间 | M=宏定义字段 | N=素材目录 | O=...",
    ...rows,
  ].join("\n");
  const gachaRows = Array.from({ length: 109 }, (_, index) => {
    const row = index + 1;
    if (row === 1) return "行 1 | A=id | B=权重 | C=名称 | F=概率期望 | H=奖池 | I=道具 | J=权重 | K=赛尔豆 | L=钻石 | M=钻石期望 | O=实际钻石期望";
    if (row === 2) return "行 2 | A=1003 | B=160 | C=回血药高级 | E=1003_160 | F=0.0235 | H=普通奖池 | I=回血药高级 | J=160 | K=4000 | L=2 | M=0.1546 | O=0.0470";
    if (row === 3) return "行 3 | A=1004 | B=200 | C=回血药超级 | E=1004_200 | F=0.0293 | I=回血药超级 | J=80 | K=20000 | L=10 | M=0.3866 | O=0.2938";
    return `行 ${row} | A=${10_000 + row} | B=20 | E=${10_000 + row}_20 | F=0.0029 | I=扭蛋机奖池道具${row} | J=20 | L=30 | M=0.2899 | O=0.0881`;
  });
  const gachaText = [
    "字段 | A=id | B=权重 | C=名称 | F=概率期望 | H=奖池 | I=道具 | J=权重 | K=赛尔豆 | L=钻石 | M=钻石期望 | O=实际钻石期望",
    ...gachaRows,
  ].join("\n");
  const database = new IndexDatabase(path.join(root, "index.sqlite"));
  try {
    database.replaceDocument({
      id: "doc_a6a36c4ac001cdb0bbc35193",
      candidate: {
        sourceId: source.id,
        sourceLabel: source.label,
        sourceKind: source.kind,
        sourceIdentity: sourceIndexIdentity(source),
        rootPath: sourceRoot,
        absolutePath: path.join(sourceRoot, "eventSummary.xlsx"),
        relativePath: "eventSummary.xlsx",
        extension: ".xlsx",
        sizeBytes: chunkText.length,
        filesystemMtimeMs: Date.parse("2026-08-31T00:00:00Z"),
      },
      title: "$items",
      familyKey: "$items",
      familyConfidence: 1,
      contentHash: "d".repeat(64),
      date: {
        effectiveUpdatedAtMs: Date.parse("2026-08-31T00:00:00Z"),
        dateSource: "filename",
        filenameDateMs: Date.parse("2026-08-31T00:00:00Z"),
        versionLogDateMs: null,
        pathDateMs: null,
        embeddedModifiedAtMs: null,
      },
      chunks: [{
        ordinal: 0,
        text: chunkText,
        headingPath: ["表数据"],
        sectionType: "config",
        locator: "表数据!A193:M384",
        contentHash: "c".repeat(64),
      }],
      warnings: [],
      needsOcr: false,
    }, "scoped-citation-test");
    database.replaceDocument({
      id: "doc_599d124b04dad9a05db5d0af",
      candidate: {
        sourceId: source.id,
        sourceLabel: source.label,
        sourceKind: source.kind,
        sourceIdentity: sourceIndexIdentity(source),
        rootPath: sourceRoot,
        absolutePath: path.join(sourceRoot, "newPrizePool_2.xlsx"),
        relativePath: "common/高级配置/fanta/扭蛋机/newPrizePool_2.xlsx",
        extension: ".xlsx",
        sizeBytes: gachaText.length,
        filesystemMtimeMs: Date.parse("2026-08-31T00:00:00Z"),
      },
      title: "newPrizePool_2",
      familyKey: "newPrizePool_2",
      familyConfidence: 1,
      contentHash: "f".repeat(64),
      date: {
        effectiveUpdatedAtMs: Date.parse("2026-08-31T00:00:00Z"),
        dateSource: "filename",
        filenameDateMs: Date.parse("2026-08-31T00:00:00Z"),
        versionLogDateMs: null,
        pathDateMs: null,
        embeddedModifiedAtMs: null,
      },
      chunks: [{
        ordinal: 6,
        text: gachaText,
        headingPath: ["奖池3"],
        sectionType: "config",
        locator: "奖池3!A1:O109",
        contentHash: "e".repeat(64),
      }],
      warnings: [],
      needsOcr: false,
    }, "scoped-citation-gacha-test");
    const engine = new SearchEngine(database, () => config);
    const result = await engine.retrieve({
      query: "newLottery newPrizePool 素材目录 配置",
      documentIds: ["doc_a6a36c4ac001cdb0bbc35193"],
      sourceKinds: ["table"],
      maxDocuments: 1,
    });
    const excerpt = result.search.hits[0]?.excerpts[0];
    assert(excerpt);
    assert.notEqual(excerpt.locator, "表数据!A193:M384");
    assert.match(excerpt.locator, /^表数据!A\d+:[A-M]\d+$/);
    assert.doesNotMatch(excerpt.locator, /:[N-Z]/, "生成 projection 必须以底层 chunk locator 的 M 列为硬边界");
    assert.doesNotMatch(excerpt.text, /N=素材目录|N=扭蛋机素材目录|N=奖池素材目录/);
    assert.match(excerpt.citation.citationId, /^DRAG:2\./, "投影结果应使用短 scoped citationId v2");

    const scoped = engine.readCitation(excerpt.citation.citationId, result.indexRevision);
    assert.equal(scoped.citation.citationId, excerpt.citation.citationId);
    assert.equal(scoped.citation.locator, excerpt.locator);
    assert.equal(scoped.content, excerpt.text);
    assert(scoped.content.length < chunkText.length / 4, "scoped citation 不得回读整张 sheet chunk");
    assert.throws(
      () => engine.readCitation(`${excerpt.citation.citationId.slice(0, -1)}x`),
      /引用范围.*损坏|scoped citation/i,
      "篡改 scope 必须被校验拒绝",
    );

    const legacy = engine.readCitation(`DRAG:${excerpt.chunkId}`);
    assert.equal(legacy.citation.locator, "表数据!A193:M384");
    assert.equal(legacy.content, chunkText, "裸 chunk citation 必须保持兼容");

    const gacha = await engine.retrieve({
      query: "我要新增一个扭蛋机，需要配置哪些表格",
      documentIds: ["doc_599d124b04dad9a05db5d0af"],
      sourceKinds: ["table"],
      maxDocuments: 1,
      maxChunksPerDocument: 3,
      maxChars: 8_000,
    });
    const gachaEvidence = gacha.evidence[0];
    const gachaExcerpt = gacha.search.hits[0]?.excerpts[0];
    assert(gachaEvidence && gachaExcerpt);
    assert.match(gachaEvidence.citationId, /^DRAG:2\./);
    assert(gachaEvidence.citationId.length < 90, `短 citationId 实际长度 ${gachaEvidence.citationId.length}`);
    const gachaRead = engine.readCitation(gachaEvidence.citationId, gacha.indexRevision);
    assert.equal(gachaRead.citation.citationId, gachaEvidence.citationId);
    assert.equal(gachaRead.citation.locator, gachaEvidence.locator);
    assert.equal(gachaRead.content, gachaEvidence.content, "真实 gacha 形态必须可逐字复制并精确回读");

    const v1Scope = {
      v: 1,
      l: "奖池3!A1:I3",
      c: ["A", "B", "C", "E", "F", "H", "I"],
    };
    const v1Payload = Buffer.from(JSON.stringify(v1Scope), "utf8").toString("base64url");
    const v1Digest = createHash("sha256")
      .update("drag-scoped-citation-v1\0")
      .update(gachaExcerpt.chunkId)
      .update("\0")
      .update("e".repeat(64))
      .update("\0")
      .update(v1Payload)
      .digest("base64url")
      .slice(0, 22);
    const v1CitationId = `DRAG:${gachaExcerpt.chunkId}~${v1Payload}.${v1Digest}`;
    assert(gachaEvidence.citationId.length < v1CitationId.length, "v2 必须显著短于兼容 v1 JSON token");
    const v1Read = engine.readCitation(v1CitationId);
    assert.equal(v1Read.citation.citationId, v1CitationId);
    assert.equal(v1Read.citation.locator, v1Scope.l);
    assert.match(v1Read.content, /普通奖池|回血药高级/);
  } finally {
    database.close();
  }
});
