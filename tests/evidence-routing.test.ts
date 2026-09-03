import assert from "node:assert/strict";
import { mkdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import type { AppConfig, AppEvent } from "../src/shared/contracts.js";
import { KnowledgeBaseService } from "../src/core/service.js";
import { ChatController, mergeSearchResponses } from "../src/main/chat-controller.js";

test("独立搜索结果进入当前 thread 的证据栏并在切换时隔离", async () => {
  const root = path.resolve("tests/.tmp/evidence-routing");
  const sourceRoot = path.join(root, "source");
  await rm(root, { recursive: true, force: true });
  await mkdir(sourceRoot, { recursive: true });
  await writeFile(path.join(sourceRoot, "幸运轮盘_20260819.md"), "# 玩法规则\n轮盘抽奖后发放奖励。\n# 配置\nturntable dropId", "utf8");

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
      excludeDirectoryNames: [],
      maxFileBytes: 1_000_000,
    }],
  };
  await service.saveConfig(config);
  await service.index({ full: true });

  const controller = new ChatController(service);
  try {
    await controller.threads.load();
    const events: AppEvent[] = [];
    controller.on("event", (event: AppEvent) => events.push(event));
    const response = await controller.search({ query: "轮盘抽奖" });
    assert(response.hits.length > 0);
    assert.equal(controller.snapshot().evidence?.hits[0]?.title, response.hits[0]?.title);
    assert(events.some((event) => event.type === "evidence" && event.evidence?.hits.length === response.hits.length));
    const firstHit = response.hits[0];
    if (!firstHit) throw new Error("缺少检索命中");
    const merged = mergeSearchResponses(
      { ...response, hits: [{ ...firstHit, documentId: "first", title: "第一批来源" }] },
      { ...response, query: "补充检索", hits: [{ ...firstHit, documentId: "second", title: "第二批来源" }] },
    );
    assert.deepEqual(new Set(merged.hits.map((hit) => hit.title)), new Set(["第一批来源", "第二批来源"]));
    assert.equal(merged.query, response.query);

    const firstThreadId = controller.snapshot().activeThreadId;
    const second = controller.createThread();
    assert.equal(controller.snapshot().evidence, null);
    if (!firstThreadId) throw new Error("缺少首个 thread");
    controller.selectThread(firstThreadId);
    assert.equal(controller.snapshot().evidence?.query, "轮盘抽奖");
    controller.selectThread(second.id);
    assert.equal(controller.snapshot().evidence, null);

    controller.clearIndexCache();
    assert.equal(controller.snapshot().index.documentCount, 0);
    assert.equal(controller.snapshot().evidence, null);
  } finally {
    controller.dispose();
    await controller.appServer.stop();
    service.close();
  }
});
