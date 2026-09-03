import assert from "node:assert/strict";
import { stat } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { processIndexTask } from "../src/core/index-task.js";
import type { FileCandidate } from "../src/core/types.js";

const cases = [
  ["新版本规划.xlsx", "2027-01-27", "version_log"],
  ["每周新精灵信息汇总.xlsx", "2026-10-07", "version_log"],
  ["体验服规划.xlsx", "2024-11-27", "version_log"],
  [path.join("文案内容", "剧情工作", "主线", "剧情规划.xlsx"), "2022-02-09", "version_log"],
  ["版本规划.xlsx", "2019-12-18", "version_log"],
  ["2024精灵冒烟测试表xlsx.xlsx", "2023-11-29", "version_log"],
  ["精灵冒烟测试表xlsx.xlsx", "2024-06-26", "version_log"],
  [path.join("2022年度", "成就界面优化_designer-a.docx"), "2022-08-10", "version_log"],
  [path.join("新手2.0", "ys_【废弃】【调整】超NO助手.docx"), "2023-05-23", "version_log"],
  [path.join("2026年度", "2026.节日版本示例", "designer-a_便利计划-资源储备功能_202560204.docx"), "2026-02-04", "version_log"],
  [path.join("2026年度", "2026.节日版本示例", "designer-a_便利计划-周常奖励补领_202560204.docx"), "2026-02-04", "version_log"],
  [path.join("美术原画插图需求", "2025年度", "2025决战洪荒插图需求.xlsx"), "2025-12-16T03:41:57.000Z", "embedded_modified"],
  [path.join("文案内容", "剧情工作", "主线", "异能星之战（中）.docx"), "2021-06-15", "version_log"],
  [path.join("文案内容", "剧情工作", "主线", "异能星之战（上）.docx"), "2021-06-11", "version_log"],
] as const;

test("真实语料日期投影与 Go shared golden 一致", async (context) => {
  const root = process.env.DRAG_DATE_PARITY_ROOT?.trim();
  if (!root) {
    context.skip("设置 DRAG_DATE_PARITY_ROOT 后运行只读真实语料日期门禁");
    return;
  }
  for (const [relativePath, expected, expectedSource] of cases) {
    await context.test(relativePath, async () => {
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
      const expectedIso = expected.includes("T") ? expected : `${expected}T00:00:00.000Z`;
      assert.equal(result.draft.date.dateSource, expectedSource);
      assert.equal(new Date(result.draft.date.effectiveUpdatedAtMs).toISOString(), expectedIso);
    });
  }
});
