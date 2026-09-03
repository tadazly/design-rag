import assert from "node:assert/strict";
import { mkdir, rm, stat, writeFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { IndexWorkerPool } from "../src/core/index-worker-pool.js";
import { sourceIndexIdentity } from "../src/core/paths.js";
import type { FileCandidate } from "../src/core/types.js";

test("worker 达到任务上限后回收并继续处理后续文档", async () => {
  const root = path.resolve("tests/.tmp/worker-recycle");
  await rm(root, { recursive: true, force: true });
  await mkdir(root, { recursive: true });
  const candidates: FileCandidate[] = [];
  for (let index = 0; index < 5; index += 1) {
    const absolutePath = path.join(root, `轮盘_${index}_20260819.md`);
    await writeFile(absolutePath, `# 玩法\n轮盘抽奖奖励 ${index}`, "utf8");
    const info = await stat(absolutePath);
    candidates.push({
      sourceId: "plans",
      sourceLabel: "策划案",
      sourceKind: "design",
      sourceIdentity: sourceIndexIdentity({ kind: "design", rootPath: root }),
      rootPath: root,
      absolutePath,
      relativePath: path.basename(absolutePath),
      extension: ".md",
      sizeBytes: info.size,
      filesystemMtimeMs: Math.trunc(info.mtimeMs),
    });
  }

  const pool = new IndexWorkerPool(1, { maxJobsPerWorker: 2 });
  try {
    for (const candidate of candidates) {
      const result = await pool.run({ candidate, existingContentHash: null, full: true });
      assert.equal(result.kind, "draft");
    }
  } finally {
    await pool.close();
  }
});
