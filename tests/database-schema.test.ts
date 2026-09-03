import assert from "node:assert/strict";
import { mkdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { DatabaseSync } from "node:sqlite";
import test from "node:test";
import { createSourceConfig } from "../src/core/config.js";
import { KnowledgeBaseService } from "../src/core/service.js";

test("v2 缓存迁移到 source identity 时保留 disabled 行与 FTS，重新启用后再重提取", async () => {
  const root = path.resolve("tests/.tmp/schema-v2-source-identity");
  const sourceRoot = path.join(root, "source");
  const configDir = path.join(root, "config");
  const dataDir = path.join(root, "data");
  const filePath = path.join(sourceRoot, "legacy.md");
  await rm(root, { recursive: true, force: true });
  await mkdir(sourceRoot, { recursive: true });
  await writeFile(filePath, "# 玩法\n\n旧版缓存迁移后重新启用。", "utf8");

  let service = await KnowledgeBaseService.create({ configDir, dataDir });
  const enabledSource = createSourceConfig({ id: "legacy", label: "旧缓存", kind: "design", rootPath: sourceRoot });
  await service.saveConfig({ ...service.config, sources: [enabledSource] });
  await service.index({ full: true });
  await service.saveConfig({ ...service.config, sources: [{ ...enabledSource, enabled: false }] });
  const before = service.database.getDocumentByPath(filePath);
  assert(before);
  const ftsBefore = service.database.fts5Available
    ? Number(service.database.db.prepare("SELECT COUNT(*) AS count FROM chunks_terms").get()?.count)
    : null;
  service.close();

  const databasePath = path.join(dataDir, "index.sqlite");
  const v2 = new DatabaseSync(databasePath);
  v2.exec(`
    DROP TABLE source_index_state;
    ALTER TABLE documents DROP COLUMN source_identity;
    UPDATE index_meta SET value = '2' WHERE key = 'schema_version';
  `);
  v2.close();

  service = await KnowledgeBaseService.create({ configDir, dataDir });
  try {
    const migrated = service.database.getDocumentByPath(filePath);
    assert(migrated);
    assert.equal(migrated.source_identity, "");
    assert.equal(migrated.deleted, before.deleted);
    assert.equal(migrated.stale, before.stale, "迁移不得把 disabled 来源标成 stale");
    assert.equal(String(service.database.db.prepare("SELECT value FROM index_meta WHERE key = 'schema_version'").get()?.value), "3");
    if (ftsBefore !== null) {
      assert.equal(Number(service.database.db.prepare("SELECT COUNT(*) AS count FROM chunks_terms").get()?.count), ftsBefore);
    }

    const reenabled = await service.reconcileSources({
      ...service.config,
      sources: [{ ...enabledSource, enabled: true }],
    });
    assert.equal(reenabled.indexRun?.indexed, 1);
    assert.equal(reenabled.indexRun?.unchanged, 0);
    const refreshed = service.database.getDocumentByPath(filePath);
    assert(refreshed?.source_identity);
    assert.equal(refreshed?.deleted, 0);
    assert.equal(refreshed?.stale, 0);
  } finally {
    service.close();
  }
});
test("未知的未来 schema version 必须 fail closed", async () => {
  const root = path.resolve("tests/.tmp/schema-future-version");
  const configDir = path.join(root, "config");
  const dataDir = path.join(root, "data");
  await rm(root, { recursive: true, force: true });
  const service = await KnowledgeBaseService.create({ configDir, dataDir });
  service.close();
  const databasePath = path.join(dataDir, "index.sqlite");
  const future = new DatabaseSync(databasePath);
  future.prepare("UPDATE index_meta SET value = '99' WHERE key = 'schema_version'").run();
  future.close();
  await assert.rejects(
    KnowledgeBaseService.create({ configDir, dataDir }),
    /不支持的索引 schema_version=99/,
  );
});

test("index_meta 缺失 schema_version 时必须 fail closed 且不得静默补写", async () => {
  const root = path.resolve("tests/.tmp/schema-missing-version");
  const configDir = path.join(root, "config");
  const dataDir = path.join(root, "data");
  await rm(root, { recursive: true, force: true });
  const service = await KnowledgeBaseService.create({ configDir, dataDir });
  service.close();
  const databasePath = path.join(dataDir, "index.sqlite");
  const damaged = new DatabaseSync(databasePath);
  damaged.prepare("DELETE FROM index_meta WHERE key = 'schema_version'").run();
  damaged.close();
  await assert.rejects(
    KnowledgeBaseService.create({ configDir, dataDir }),
    /缺少 schema_version/,
  );
  const verify = new DatabaseSync(databasePath, { readOnly: true });
  assert.equal(Number(verify.prepare("SELECT COUNT(*) AS count FROM index_meta WHERE key = 'schema_version'").get()?.count), 0);
  verify.close();
});
