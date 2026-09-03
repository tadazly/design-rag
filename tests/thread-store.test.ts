import assert from "node:assert/strict";
import { mkdir, readFile, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { ThreadStore } from "../src/main/thread-store.js";
import type { SearchResponse } from "../src/shared/contracts.js";

function evidence(query: string, revision: number): SearchResponse {
  return {
    query,
    expandedTerms: [query],
    requestedMode: "auto",
    actualMode: "lexical",
    semanticUsed: false,
    semanticCoverage: 0,
    sort: "newest",
    indexRevision: revision,
    totalCandidates: 1,
    tookMs: 1,
    hits: [],
    warnings: [],
  };
}

test("流式消息只更新内存，最终会话持久化串行合并", async () => {
  const root = path.resolve("tests/.tmp/thread-store");
  await rm(root, { recursive: true, force: true });
  const store = new ThreadStore(root);
  await store.load();
  const thread = store.active();

  for (let index = 0; index < 200; index += 1) {
    store.update(thread.id, {
      messages: [{
        id: "assistant",
        role: "assistant",
        text: `stream-${index}`,
        createdAt: Date.now(),
        status: "streaming",
        citationIds: [],
      }],
    }, { persist: false });
  }
  store.update(thread.id, {
    messages: [{
      id: "assistant",
      role: "assistant",
      text: "complete",
      createdAt: Date.now(),
      status: "complete",
      citationIds: [],
    }],
  });
  await Promise.all(Array.from({ length: 20 }, () => store.save()));

  const persisted = JSON.parse(await readFile(path.join(root, "threads.json"), "utf8")) as {
    threads: Array<{ messages: Array<{ text: string; status: string }> }>;
  };
  assert.equal(persisted.threads[0]?.messages[0]?.text, "complete");
  assert.equal(persisted.threads[0]?.messages[0]?.status, "complete");
});

test("检索证据按 thread 隔离持久化，并兼容旧 schema", async () => {
  const root = path.resolve("tests/.tmp/thread-evidence");
  await rm(root, { recursive: true, force: true });
  const store = new ThreadStore(root);
  await store.load();
  const first = store.active();
  store.update(first.id, { evidence: evidence("轮盘", 7) });
  const second = store.create();
  store.update(second.id, { evidence: evidence("扭蛋", 8) });
  store.select(first.id);
  await store.save();

  const reloaded = new ThreadStore(root);
  await reloaded.load();
  assert.equal(reloaded.active().evidence?.query, "轮盘");
  assert.equal(reloaded.get(second.id)?.evidence?.query, "扭蛋");
  reloaded.clearEvidence();
  await reloaded.save();
  assert(reloaded.list().every((thread) => reloaded.get(thread.id)?.evidence === null));

  const legacyRoot = path.resolve("tests/.tmp/thread-evidence-legacy");
  await rm(legacyRoot, { recursive: true, force: true });
  await mkdir(legacyRoot, { recursive: true });
  await writeFile(path.join(legacyRoot, "threads.json"), JSON.stringify({
    schemaVersion: 1,
    activeThreadId: "legacy",
    threads: [{
      id: "legacy",
      codexThreadId: null,
      title: "旧会话",
      preview: "",
      createdAt: 1,
      updatedAt: 1,
      messages: [],
    }],
  }), "utf8");
  const legacy = new ThreadStore(legacyRoot);
  await legacy.load();
  assert.equal(legacy.active().evidence, null);
  const migrated = JSON.parse(await readFile(path.join(legacyRoot, "threads.json"), "utf8")) as { schemaVersion: number };
  assert.equal(migrated.schemaVersion, 3);
});

test("会话归档、恢复与删除始终保留一个可用活动会话", async () => {
  const root = path.resolve("tests/.tmp/thread-lifecycle");
  await rm(root, { recursive: true, force: true });
  const store = new ThreadStore(root);
  await store.load();
  const first = store.active();
  const second = store.create();

  store.archive(second.id);
  assert.equal(store.get(second.id)?.archivedAt === null, false);
  assert.equal(store.active().id, first.id);
  assert.equal(store.list().find((thread) => thread.id === second.id)?.archived, true);

  store.restore(second.id);
  assert.equal(store.get(second.id)?.archivedAt, null);
  store.select(second.id);
  assert.equal(store.active().id, second.id);

  store.remove(second.id);
  assert.equal(store.get(second.id), null);
  assert.equal(store.active().id, first.id);
  store.remove(first.id);
  assert.equal(store.list().filter((thread) => !thread.archived).length, 1);
  await store.save();
});
