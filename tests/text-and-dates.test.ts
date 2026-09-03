import assert from "node:assert/strict";
import test from "node:test";
import { makeFamilyKey, classifySection, expandQueryTerms } from "../src/core/classifier.js";
import { findDates, resolveEffectiveDate } from "../src/core/dates.js";
import { cjkSearchTerms, normalizeText } from "../src/core/text.js";

test("中文 tokenizer 同时生成单字、双字与短语", () => {
  const terms = cjkSearchTerms("幸运轮盘抽奖 LUCKY_WHEEL_ACT");
  assert(terms.includes("轮盘"));
  assert(terms.includes("抽奖"));
  assert(terms.includes("lucky_wheel_act"));
  assert.equal(normalizeText("  幸运\u200b轮盘  "), "幸运轮盘");
});

test("查询扩展覆盖轮盘、抽奖和复用语义", () => {
  const terms = expandQueryTerms("我想复用轮盘抽奖活动");
  assert(terms.includes("转盘"));
  assert(terms.includes("幸运轮盘"));
  assert(terms.includes("沿用"));
  assert(terms.includes("奖池"));
});

test("有效业务日期优先文件名，再版本记录和版本路径", () => {
  const result = resolveEffectiveDate({
    absolutePath: "D:\\DesignRag\\examples\\design-docs\\2026年度\\2026.08.19\\幸运轮盘_20260819.xlsx",
    contentSample: "版本修改记录\n20260311 初版\n20260805 复用",
    embeddedModifiedAt: "2026-08-05T10:00:00Z",
    filesystemMtimeMs: Date.parse("2026-08-04T00:00:00Z"),
  });
  assert.equal(result.dateSource, "filename");
  assert.equal(new Date(result.effectiveUpdatedAtMs).toISOString().slice(0, 10), "2026-08-19");
  assert.deepEqual(findDates("2026.08.19 / 20260805" ).map((value) => new Date(value).toISOString().slice(0, 10)), ["2026-08-05", "2026-08-19"]);
});

test("版本记录优先于目录日期，且无效日历日期不会被归一化", () => {
  const result = resolveEffectiveDate({
    absolutePath: "D:\\DesignRag\\examples\\design-docs\\2026.09.01\\activity.docx",
    contentSample: "更新 20260805 完成复用",
    embeddedModifiedAt: "2026-08-04T00:00:00Z",
    filesystemMtimeMs: Date.parse("2026-08-03T00:00:00Z"),
  });
  assert.equal(result.dateSource, "version_log");
  assert.equal(new Date(result.effectiveUpdatedAtMs).toISOString().slice(0, 10), "2026-08-05");
  assert.deepEqual(findDates("20260100 20260231"), []);
});

test("版本表头与数据分行时仍读取修订日期", () => {
  const result = resolveEffectiveDate({
    absolutePath: "D:\\DesignRag\\examples\\design-docs\\activity.docx",
    contentSample: "修订历史\n修订号 | 修订日期 | 修订内容 | 修订人\n | 20210615 | 初稿 | Deathclock",
    embeddedModifiedAt: "2021-06-24T02:43:00Z",
    filesystemMtimeMs: Date.parse("2026-08-04T00:00:00Z"),
  });
  assert.equal(result.dateSource, "version_log");
  assert.equal(new Date(result.effectiveUpdatedAtMs).toISOString().slice(0, 10), "2021-06-15");
});

test("封面版本日期是弱证据，明确迭代目录日期优先", () => {
  const result = resolveEffectiveDate({
    absolutePath: "D:\\DesignRag\\examples\\design-docs\\2020.08.19\\复用活动.docx",
    contentSample: "Version：V1.00 20200805",
    embeddedModifiedAt: null,
    filesystemMtimeMs: 1,
  });
  assert.equal(result.dateSource, "path");
  assert.equal(new Date(result.effectiveUpdatedAtMs).toISOString().slice(0, 10), "2020-08-19");
  assert.equal(new Date(result.versionLogDateMs ?? 0).toISOString().slice(0, 10), "2020-08-05");
});

test("明确版本日期行支持 SheetJS M/D/YY 显示值", () => {
  const result = resolveEffectiveDate({
    absolutePath: "D:\\DesignRag\\examples\\design-docs\\剧情规划.xlsx",
    contentSample: "Sheet2 字段 | A=版本日期 | B=2/26/20 | C=2/12/25",
    embeddedModifiedAt: "2021-11-16T03:47:56Z",
    filesystemMtimeMs: 1,
  });
  assert.equal(result.dateSource, "version_log");
  assert.equal(new Date(result.effectiveUpdatedAtMs).toISOString().slice(0, 10), "2025-02-12");
});

test("大型版本表扫描超过旧 120k 截断位置", () => {
  const result = resolveEffectiveDate({
    absolutePath: "D:\\DesignRag\\examples\\design-docs\\weekly.xlsx",
    contentSample: "字段 | A=版本\n版本=20240528\n" + "普通说明填充。\n".repeat(20_000) + "版本=20261007",
    embeddedModifiedAt: null,
    filesystemMtimeMs: 1,
  });
  assert.equal(result.dateSource, "version_log");
  assert.equal(new Date(result.effectiveUpdatedAtMs).toISOString().slice(0, 10), "2026-10-07");
});

test("普通需求中的版本宣传不冒充版本日志", () => {
  const result = resolveEffectiveDate({
    absolutePath: "D:\\DesignRag\\examples\\design-docs\\art.xlsx",
    contentSample: "首次交付验收时间：2026.1.21，通过验收不晚于：2026.1.28，游戏内用于版本宣传或皮肤售卖。",
    embeddedModifiedAt: "2025-12-16T03:41:57Z",
    filesystemMtimeMs: Date.parse("2026-08-04T00:00:00Z"),
  });
  assert.equal(result.dateSource, "embedded_modified");
  assert.equal(result.effectiveUpdatedAtMs, Date.parse("2025-12-16T03:41:57Z"));
});

test("版本日期行与版本宣传排除规则保持 golden", () => {
  const embeddedModifiedAt = "2025-12-16T03:41:57.000Z";
  const cases = [
    {
      name: "date immediately follows version marker",
      contentSample: "版本 2026-08-20：调整奖励产出。",
      wantSource: "version_log",
      wantDate: "2026-08-20T00:00:00.000Z",
    },
    {
      name: "marketing phrase is not a version record",
      contentSample: "版本宣传排期预计于 2026-08-20 启动。",
      wantSource: "embedded_modified",
      wantDate: embeddedModifiedAt,
    },
    {
      name: "invalid leading date is not rescued by later campaign date",
      contentSample: "版本 2026-02-31：宣传档期 2026-08-20。",
      wantSource: "embedded_modified",
      wantDate: embeddedModifiedAt,
    },
  ] as const;
  for (const item of cases) {
    const result = resolveEffectiveDate({
      absolutePath: "D:\\DesignRag\\examples\\design-docs\\activity.md",
      contentSample: item.contentSample,
      embeddedModifiedAt,
      filesystemMtimeMs: 1,
    });
    assert.equal(result.dateSource, item.wantSource, item.name);
    assert.equal(new Date(result.effectiveUpdatedAtMs).toISOString(), item.wantDate, item.name);
  }
});

test("family key 去除复用标签、日期与版本号", () => {
  const left = makeFamilyKey("designer-a_【复用】幸运轮盘_星灵_20260819");
  const right = makeFamilyKey("designer-a_【复用】幸运轮盘_星灵_20260805");
  assert.equal(left.key, right.key);
  assert(left.confidence > 0.6);
});

test("真实策划章节分类覆盖版本、面板、奖励、配置与美术", () => {
  assert.equal(classifySection(["版本修改记录"], "20260819 复用"), "version_history");
  assert.equal(classifySection(["面板&逻辑"], "入口与交互"), "panel_logic");
  assert.equal(classifySection(["奖励数值"], "奖品和消耗"), "reward_value");
  assert.equal(classifySection(["配置表明细"], "版本=20260819 | turntable dropId"), "config");
  assert.equal(classifySection(["原画&动画需求"], "精灵立绘"), "animation_requirement");
});
