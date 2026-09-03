import assert from "node:assert/strict";
import { mkdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import JSZip from "jszip";
import * as XLSX from "xlsx";
import { extractDocx } from "../src/core/extractors/docx.js";
import { extractSpreadsheet } from "../src/core/extractors/spreadsheet.js";

const fixtureRoot = path.resolve("tests/.tmp/extractors");

async function reset(): Promise<void> {
  await rm(fixtureRoot, { recursive: true, force: true });
  await mkdir(fixtureRoot, { recursive: true });
}

async function writeMinimalDocx(filePath: string): Promise<void> {
  const zip = new JSZip();
  zip.file("[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8"?>
    <Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
      <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
      <Default Extension="xml" ContentType="application/xml"/>
      <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
      <Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>
    </Types>`);
  zip.file("_rels/.rels", `<?xml version="1.0" encoding="UTF-8"?>
    <Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
      <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
      <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>
    </Relationships>`);
  zip.file("word/_rels/document.xml.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"/>`);
  zip.file("word/document.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
    <w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
      <w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>轮盘活动流程</w:t></w:r></w:p>
      <w:p><w:r><w:t>入口 → 抽奖 → 奖励发放 → 结果展示</w:t></w:r></w:p>
      <w:tbl><w:tr><w:tc><w:p><w:r><w:t>字段</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>说明</w:t></w:r></w:p></w:tc></w:tr><w:tr><w:tc><w:p><w:r><w:t>dropId</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>掉落组</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
      <w:sectPr/></w:body></w:document>`);
  zip.file("docProps/core.xml", `<?xml version="1.0" encoding="UTF-8"?>
    <cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dcterms="http://purl.org/dc/terms/">
      <dcterms:modified>2026-08-19T10:00:00Z</dcterms:modified>
    </cp:coreProperties>`);
  await writeFile(filePath, await zip.generateAsync({ type: "nodebuffer" }));
}

test("DOCX 提取保留 heading、段落、表格和 modified 日期", async () => {
  await reset();
  const filePath = path.join(fixtureRoot, "幸运轮盘_20260819.docx");
  await writeMinimalDocx(filePath);
  const extracted = await extractDocx(filePath);
  assert(extracted.blocks.some((block) => block.text.includes("入口") && block.headingPath.includes("轮盘活动流程")));
  assert(extracted.blocks.some((block) => block.locator.includes("表格") && block.text.includes("dropId")));
  assert.equal(extracted.embeddedModifiedAt, "2026-08-19T10:00:00Z");
});

test("XLSX 忽略虚假 dimension，只枚举实际 cell，并保留 sheet range", async () => {
  await reset();
  const filePath = path.join(fixtureRoot, "幸运轮盘_20260819.xlsx");
  const workbook = XLSX.utils.book_new();
  const sheet = XLSX.utils.aoa_to_sheet([
    ["版本", "类型", "版本号", "概述"],
    ["20260311", "初版", "1.0", "幸运轮盘"],
    ["20260819", "复用", "1.6", "更新精灵奖励"],
  ]);
  sheet["!ref"] = "A1:XFD84";
  XLSX.utils.book_append_sheet(workbook, sheet, "版本修改记录");
  await writeFile(filePath, XLSX.write(workbook, { bookType: "xlsx", type: "buffer" }) as Buffer);
  const extracted = await extractSpreadsheet(filePath);
  assert(extracted.blocks.length <= 3);
  assert(extracted.blocks[0]?.locator.startsWith("版本修改记录!A1:D3"));
  assert(extracted.blocks[0]?.text.includes("版本=20260819"));
});

test("XLSX DateEvidence 精确读取修订表、版本轴与招募版本", async () => {
  await reset();
  const filePath = path.join(fixtureRoot, "date-evidence.xlsx");
  const workbook = XLSX.utils.book_new();

  XLSX.utils.book_append_sheet(workbook, XLSX.utils.aoa_to_sheet([
    ["修订号", "修订日期", "修订内容"],
    ["1.0", "2026-01-02", "初稿"],
    ["1.1", "2026-02-03", "调整"],
  ]), "修订记录");

  const weekly = XLSX.utils.aoa_to_sheet([
    ["版本", "类型"],
    [20261007, "精灵"],
  ]);
  XLSX.utils.book_append_sheet(workbook, weekly, "weekly");

  const english = XLSX.utils.aoa_to_sheet([
    ["version", new Date("2026-01-01T00:00:00.000Z"), new Date("2026-01-08T00:00:00.000Z"), new Date("2026-01-15T00:00:00.000Z")],
    ["内容", "正式版本内容", "仅主排期存在"],
  ]);
  english.D1 = { t: "d", v: new Date("2026-01-15T00:00:00.000Z"), f: "C1+7", z: "m/d/yy" };
  XLSX.utils.book_append_sheet(workbook, english, "roadmap");
  XLSX.utils.book_append_sheet(workbook, XLSX.utils.aoa_to_sheet([
    ["复用版本", new Date("2026-01-01T00:00:00.000Z"), new Date("2025-12-25T00:00:00.000Z")],
    ["内容", "复用排期内容", "历史排期内容"],
  ]), "reuse");

  XLSX.utils.book_append_sheet(workbook, XLSX.utils.aoa_to_sheet([
    ["期数", "招募版本", "玩家可进入时间"],
    [6, 20241127, "预期20241204开始"],
  ]), "体验服招募记录");
  XLSX.utils.book_append_sheet(workbook, XLSX.utils.aoa_to_sheet([
    ["版本精灵"],
    [20261231],
  ]), "near-match");
  XLSX.utils.book_append_sheet(workbook, XLSX.utils.aoa_to_sheet([["版本精灵"], ["测试内容"]]), "精灵测试内容20240626");

  await writeFile(filePath, XLSX.write(workbook, { bookType: "xlsx", type: "buffer", cellDates: true }) as Buffer);
  const extracted = await extractSpreadsheet(filePath);
  const evidence = extracted.dateEvidence ?? [];
  const has = (kind: string, isoDate: string, locator: string) => evidence.some((item) => item.kind === kind
    && item.timestampMs === Date.parse(`${isoDate}T00:00:00.000Z`)
    && item.locator.includes(locator));

  assert(has("version_field", "2026-02-03", "修订记录!B3"));
  assert(has("version_field", "2026-10-07", "weekly!A2"));
  assert(has("version_axis", "2026-01-01", "roadmap!B1"));
  assert(!has("version_axis", "2026-01-08", "roadmap!C1"), "只有单张排期支持的日期不得覆盖跨 sheet 共识");
  assert(!has("version_axis", "2026-01-15", "roadmap!D1"), "无业务内容的公式尾列不得成为版本日期");
  assert(has("version_field", "2024-11-27", "体验服招募记录!B2"));
  assert(!evidence.some((item) => item.kind === "version_field" && item.timestampMs === Date.parse("2024-12-04T00:00:00.000Z")), "同行进入时间不得冒充招募版本");
  assert(!evidence.some((item) => item.kind === "version_field" && item.timestampMs === Date.parse("2026-12-31T00:00:00.000Z")), "版本精灵等近似字段不得冒充精确版本字段");
  assert(has("dated_sheet", "2024-06-26", "精灵测试内容20240626"));
});

test("旧版 BIFF XLS 只读解析", async () => {
  await reset();
  const filePath = path.join(fixtureRoot, "turntable.xls");
  const workbook = XLSX.utils.book_new();
  XLSX.utils.book_append_sheet(workbook, XLSX.utils.aoa_to_sheet([["id", "weight"], [498, 100]]), "turntable");
  await writeFile(filePath, XLSX.write(workbook, { bookType: "biff8", type: "buffer" }) as Buffer);
  const extracted = await extractSpreadsheet(filePath);
  assert(extracted.blocks.some((block) => block.text.includes("id=498") && block.text.includes("weight=100")));
});

test("扩展名误标为 XLSX 的 BIFF 工作簿可由 SheetJS 单文件 fallback 恢复", async () => {
  await reset();
  const filePath = path.join(fixtureRoot, "legacy-mislabeled.xlsx");
  const workbook = XLSX.utils.book_new();
  XLSX.utils.book_append_sheet(workbook, XLSX.utils.aoa_to_sheet([
    ["字段", "值"],
    ["fallbackMarker", "SHEETJS_MISLABELED_BIFF_RECOVERED"],
  ]), "legacy");
  await writeFile(filePath, XLSX.write(workbook, { bookType: "biff8", type: "buffer" }) as Buffer);
  const extracted = await extractSpreadsheet(filePath);
  assert(extracted.blocks.some((block) => block.text.includes("SHEETJS_MISLABELED_BIFF_RECOVERED")));
});
