import assert from "node:assert/strict";
import { mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { KnowledgeBaseService } from "../src/core/service.js";

test("损坏的可重建索引会保留原文件并切换到新的活动索引", async () => {
  const root = path.resolve("tests/.tmp/database-recovery");
  const configDir = path.join(root, "config");
  const dataDir = path.join(root, "data");
  await rm(root, { recursive: true, force: true });
  await mkdir(dataDir, { recursive: true });
  await writeFile(path.join(dataDir, "index.sqlite"), "not-a-sqlite-database", "utf8");

  const service = await KnowledgeBaseService.create({ configDir, dataDir });
  try {
    const status = service.status();
    assert.equal(status.documentCount, 0);
    assert(status.recentIssues.some((issue) => issue.code === "cache_corrupt_recovered"));
    const files = await readdir(dataDir);
    assert(files.includes("index.sqlite"));
    assert(files.some((file) => file.startsWith("index.recovered-") && file.endsWith(".sqlite")));
    assert.notEqual(service.database.databasePath, path.join(dataDir, "index.sqlite"));
    const pointer = JSON.parse(await readFile(path.join(dataDir, "index.active.json"), "utf8")) as { fileName: string };
    assert.equal(path.join(dataDir, pointer.fileName), service.database.databasePath);
  } finally {
    service.close();
  }
});

test("并发进程恢复同一损坏索引时会收敛到同一个活动文件", async () => {
  const root = path.resolve("tests/.tmp/database-recovery-concurrent");
  const configDir = path.join(root, "config");
  const dataDir = path.join(root, "data");
  await rm(root, { recursive: true, force: true });
  await mkdir(dataDir, { recursive: true });
  await writeFile(path.join(dataDir, "index.sqlite"), "not-a-sqlite-database", "utf8");

  const [first, second] = await Promise.all([
    KnowledgeBaseService.create({ configDir, dataDir }),
    KnowledgeBaseService.create({ configDir, dataDir }),
  ]);
  const activePath = first.database.databasePath;
  try {
    assert.equal(activePath, second.database.databasePath);
    assert.notEqual(activePath, path.join(dataDir, "index.sqlite"));
    assert.equal(first.status().documentCount, 0);
    assert.equal(second.status().documentCount, 0);
  } finally {
    first.close();
    second.close();
  }

  const reopened = await KnowledgeBaseService.create({ configDir, dataDir });
  try {
    assert.equal(reopened.database.databasePath, activePath);
  } finally {
    reopened.close();
  }
});

test("当前 schema 可用只读连接执行状态与检索而不触发初始化写入", async () => {
  const root = path.resolve("tests/.tmp/database-read-only");
  const configDir = path.join(root, "config");
  const dataDir = path.join(root, "data");
  await rm(root, { recursive: true, force: true });
  const writable = await KnowledgeBaseService.create({ configDir, dataDir });
  writable.close();

  const readOnly = await KnowledgeBaseService.create({ configDir, dataDir, readOnly: true });
  try {
    assert.equal(readOnly.status().documentCount, 0);
    assert.deepEqual((await readOnly.search({ query: "不存在的玩法" })).hits, []);
  } finally {
    readOnly.close();
  }
});
