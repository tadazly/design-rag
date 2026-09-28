import assert from "node:assert/strict";
import { readFile, stat } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { processIndexTask } from "../src/core/index-task.js";
import type { FileCandidate } from "../src/core/types.js";

// 用例列出真实文档路径，只保存在本地未跟踪的 JSON 中，与 Go 的 TestRealCorpusDateParity 共用。
const casesPath = process.env.DRAG_DATE_PARITY_CASES?.trim() || path.resolve("tests/.tmp/real-corpus/date-parity-cases.json");

test("真实语料日期投影与 Go shared golden 一致", async (context) => {
  const root = process.env.DRAG_DATE_PARITY_ROOT?.trim();
  if (!root) {
    context.skip("设置 DRAG_DATE_PARITY_ROOT 后运行只读真实语料日期门禁");
    return;
  }
  const cases = JSON.parse(await readFile(casesPath, "utf8")) as Array<{ relativePath: string; expected: string; dateSource: string }>;
  assert(cases.length > 0, `本地日期门禁用例为空：${casesPath}`);
  for (const { relativePath: slashPath, expected, dateSource } of cases) {
    await context.test(slashPath, async () => {
      const relativePath = path.join(...slashPath.split("/"));
      const absolutePath = path.join(root, relativePath);
      const info = await stat(absolutePath);
      const candidate: FileCandidate = {
        sourceId: "plans",
        sourceLabel: "策划案",
        sourceKind: "design",
        sourceIdentity: "date-corpus-test",
        rootPath: root,
        absolutePath,
        relativePath,
        extension: path.extname(absolutePath).toLowerCase(),
        sizeBytes: info.size,
        filesystemMtimeMs: info.mtimeMs,
      };
      const result = await processIndexTask({ candidate, existingContentHash: null, full: true });
      assert.equal(result.kind, "draft");
      if (result.kind !== "draft") return;
      assert.equal(result.draft.date.dateSource, dateSource);
      assert.equal(new Date(result.draft.date.effectiveUpdatedAtMs).toISOString(), new Date(expected).toISOString());
    });
  }
});
