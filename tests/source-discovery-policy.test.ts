import assert from "node:assert/strict";
import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";
import { createSourceConfig } from "../src/core/config.js";
import { KnowledgeBaseService } from "../src/core/service.js";

test("默认过滤工具目录和高置信敏感配置，同时保留正常来源与用户排除规则", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "drag-source-policy-"));
  const sourceRoot = path.join(root, "source");
  await Promise.all([
    mkdir(path.join(sourceRoot, ".cursor"), { recursive: true }),
    mkdir(path.join(sourceRoot, ".codex"), { recursive: true }),
    mkdir(path.join(sourceRoot, ".agents"), { recursive: true }),
    mkdir(path.join(sourceRoot, ".draft"), { recursive: true }),
    mkdir(path.join(sourceRoot, "manual-skip"), { recursive: true }),
  ]);
  await Promise.all([
    writeFile(path.join(sourceRoot, "正常玩法.md"), "# 玩法\n\n正常策划证据可见。", "utf8"),
    writeFile(path.join(sourceRoot, "game-config.json"), JSON.stringify({ summary: "正常配置证据可见" }), "utf8"),
    writeFile(path.join(sourceRoot, ".cursor", "rules.md"), "本地凭据泄漏标记。", "utf8"),
    writeFile(path.join(sourceRoot, ".codex", "config.json"), JSON.stringify({ token: "本地凭据泄漏标记" }), "utf8"),
    writeFile(path.join(sourceRoot, ".agents", "workflow.md"), "本地凭据泄漏标记。", "utf8"),
    writeFile(path.join(sourceRoot, ".draft", "正常隐藏业务.md"), "# 草案\n\n正常隐藏业务证据可见。", "utf8"),
    writeFile(path.join(sourceRoot, "credentials.json"), JSON.stringify({ password: "本地凭据泄漏标记" }), "utf8"),
    writeFile(path.join(sourceRoot, "token.json"), JSON.stringify({ token: "本地凭据泄漏标记" }), "utf8"),
    writeFile(path.join(sourceRoot, "config.local.json"), JSON.stringify({ token: "本地凭据泄漏标记" }), "utf8"),
    writeFile(path.join(sourceRoot, "client_secret_prod.json"), JSON.stringify({ secret: "本地凭据泄漏标记" }), "utf8"),
    writeFile(path.join(sourceRoot, "manual-skip", "ignored.md"), "本地凭据泄漏标记。", "utf8"),
  ]);

  const service = await KnowledgeBaseService.create({
    configDir: path.join(root, "config"),
    dataDir: path.join(root, "data"),
  });
  try {
    const source = createSourceConfig({ id: "policy", label: "过滤策略", kind: "design", rootPath: sourceRoot });
    source.includeExtensions = [".md", ".json"];
    source.excludeDirectoryNames = ["manual-skip"];
    await service.saveConfig({ ...service.config, sources: [source] });

    const normalized = service.config.sources[0];
    assert(normalized);
    const excludes = new Set(normalized.excludeDirectoryNames.map((value) => value.toLowerCase()));
    for (const expected of ["manual-skip", ".cursor", ".codex", ".agents", "credentials.json", "config.local.*"]) {
      assert(excludes.has(expected), `缺少默认或用户排除：${expected}`);
    }
    assert(!excludes.has(".business"), "不得把所有普通隐藏业务目录一概排除");
    assert.deepEqual(normalized.includeExtensions, [".md", ".json"], "用户显式 includeExtensions 必须保持不变");

    const run = await service.index({ full: true });
    assert.equal(run.discovered, 3);
    assert.equal(run.indexed, 3);
    assert.equal((await service.search({ query: "本地凭据泄漏标记" })).hits.length, 0);
    assert.equal((await service.retrieve({ query: "本地凭据泄漏标记" })).evidence.length, 0);
    assert.equal((await service.search({ query: "正常策划证据可见" })).hits[0]?.relativePath, "正常玩法.md");
    assert.equal((await service.search({ query: "正常配置证据可见" })).hits[0]?.relativePath, "game-config.json");
    assert.equal((await service.search({ query: "正常隐藏业务证据可见" })).hits[0]?.relativePath, path.join(".draft", "正常隐藏业务.md"));
  } finally {
    service.close();
  }
});
