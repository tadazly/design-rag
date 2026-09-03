import { access, writeFile } from "node:fs/promises";
import path from "node:path";
import { KnowledgeBaseService } from "../dist/core/service.js";

const questions = [
  "找到最新的一个 888活动，说明一下里面的玩法和产出逻辑",
  "我要新增一个扭蛋机，需要配置哪些表格",
  "我要复用妖王888，需要配哪些表，帮我把新的配置列出来",
];

const service = await KnowledgeBaseService.create();
const results = [];
try {
  for (const [index, query] of questions.entries()) {
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
    if (index === 0 && !/888/.test(searchable)) throw new Error("888 问题未召回标题匹配候选");
    if (index === 1 && !/(newLottery|newPrizePool|扭蛋)/i.test(searchable)) throw new Error("扭蛋机问题未召回核心配表或策划");
    if (index === 2 && !/(妖王.*888|888.*妖王)/i.test(searchable)) throw new Error("妖王888复用问题未召回目标活动");
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
