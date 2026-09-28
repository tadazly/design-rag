import { access, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { KnowledgeBaseService } from "../dist/core/service.js";

// 默认题目不含真实活动名。针对真实语料的题目与期望命中（正则字符串）放在本地未跟踪的 JSON，
// 路径由 DRAG_SMOKE_QUESTIONS 指定，默认 tests/.tmp/real-corpus/smoke-questions.json。
const localQuestionsPath = path.resolve(process.env.DRAG_SMOKE_QUESTIONS?.trim() || "tests/.tmp/real-corpus/smoke-questions.json");
let questions = [
  { query: "找到最新的一个 888活动，说明一下里面的玩法和产出逻辑", expect: "888" },
  { query: "我要新增一个扭蛋机，需要配置哪些表格", expect: "(扭蛋|lottery)" },
  { query: "我想复用轮盘抽奖活动，有哪些可以复用", expect: "(轮盘|转盘)" },
];
try {
  questions = JSON.parse(await readFile(localQuestionsPath, "utf8"));
} catch (error) {
  if (error.code !== "ENOENT") throw error;
}

const service = await KnowledgeBaseService.create();
const results = [];
try {
  for (const { query, expect } of questions) {
    const bundle = await service.retrieve({
      query,
      sort: "newest",
      maxDocuments: 30,
      maxChunksPerDocument: 5,
      maxChars: 60_000,
    });
    if (bundle.evidence.length === 0) throw new Error(`检索无证据：${query}`);
    const evidence = [];
    for (const item of bundle.evidence) {
      await access(item.absolutePath);
      if (!item.locator || !item.indexedContentHash || !item.sourceLink.markdown.includes(item.locator)) {
        throw new Error(`证据字段不完整：${item.absolutePath}`);
      }
      if (/DRAG:chunk_/i.test(item.sourceLink.markdown)) throw new Error(`sourceLink 泄露内部 ID：${item.sourceLink.markdown}`);
      const read = service.readCitation(item.citationId, bundle.indexRevision);
      if (read.changed || read.content.length === 0) throw new Error(`citation 回读失败：${item.citationId}`);
      evidence.push({
        citationId: item.citationId,
        title: item.title,
        effectiveUpdatedAt: item.effectiveUpdatedAt,
        dateSource: item.dateSource,
        sourceKind: read.citation.sourceKind,
        absolutePath: item.absolutePath,
        locator: item.locator,
        sourceLink: item.sourceLink.markdown,
        contentPreview: item.content.slice(0, 240),
      });
    }
    const searchable = `${bundle.search.hits.map((hit) => `${hit.title} ${hit.relativePath}`).join("\n")}\n${evidence.map((item) => `${item.title} ${item.absolutePath} ${item.contentPreview}`).join("\n")}`;
    if (!new RegExp(expect, "i").test(searchable)) throw new Error(`问题未召回预期内容 ${expect}：${query}`);
    results.push({
      query,
      indexRevision: bundle.indexRevision,
      totalCandidates: bundle.search.totalCandidates,
      tookMs: bundle.search.tookMs,
      truncated: bundle.truncated,
      characterCount: bundle.characterCount,
      topHits: bundle.search.hits.slice(0, 12).map((hit) => ({
        title: hit.title,
        sourceKind: hit.sourceKind,
        effectiveUpdatedAt: hit.effectiveUpdatedAt,
        dateSource: hit.dateSource,
        absolutePath: hit.absolutePath,
        locators: hit.excerpts.map((excerpt) => excerpt.locator),
      })),
      evidence,
    });
  }
  const outputPath = path.join(path.dirname(service.configStore.dataDir), "retrieval-questions-report.json");
  await writeFile(outputPath, `${JSON.stringify({ status: "PASS", createdAt: new Date().toISOString(), results }, null, 2)}\n`, "utf8");
  process.stdout.write(`${JSON.stringify({ status: "PASS", outputPath, results: results.map((item) => ({ query: item.query, totalCandidates: item.totalCandidates, tookMs: item.tookMs, evidenceCount: item.evidence.length, topTitles: item.topHits.slice(0, 6).map((hit) => hit.title) })) }, null, 2)}\n`);
} finally {
  service.close();
}
