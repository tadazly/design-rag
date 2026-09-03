import assert from "node:assert/strict";
import { mkdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import * as XLSX from "xlsx";
import { createSourceConfig } from "../src/core/config.js";
import { KnowledgeBaseService } from "../src/core/service.js";

test("Go 核心在进程内恢复误标 BIFF，并保留 warning、metrics 与 last-good", async () => {
  const root = path.resolve("tests/.tmp/go-core-xlsx-fallback");
  const sourceRoot = path.join(root, "source");
  const configDir = path.join(root, "config");
  const dataDir = path.join(root, "data");
  await rm(root, { recursive: true, force: true });
  await Promise.all([
    mkdir(sourceRoot, { recursive: true }),
    mkdir(configDir, { recursive: true }),
    mkdir(dataDir, { recursive: true }),
  ]);

  const spreadsheetPath = path.join(sourceRoot, "legacy-biff-mislabeled_20260901.xlsx");
  const normalPath = path.join(sourceRoot, "normal_20260901.md");
  const marker = "SHEETJS_SINGLE_FILE_FALLBACK_20260901";
  const workbook = XLSX.utils.book_new();
  XLSX.utils.book_append_sheet(workbook, XLSX.utils.aoa_to_sheet([
    ["字段", "值"],
    ["fallbackMarker", marker],
  ]), "legacy");
  await writeFile(spreadsheetPath, XLSX.write(workbook, { bookType: "biff8", type: "buffer" }) as Buffer);
  await writeFile(normalPath, `# Go 原生文档\n\n${marker}_GO_NATIVE`, "utf8");

  const service = await KnowledgeBaseService.create({ configDir, dataDir });
  try {
    const source = createSourceConfig({
      id: "fallback",
      label: "fallback",
      kind: "design",
      rootPath: sourceRoot,
    });
    source.includeExtensions = [".xlsx", ".md"];
    await service.saveConfig({
      ...service.config,
      sources: [source],
      indexing: { ...service.config.indexing, concurrency: 2 },
    });

    const first = await service.index({ full: true });
    const firstStatus = service.status();
    assert.equal(first.phase, "complete");
    assert.equal(first.indexed, 2, "compatibility 文件与普通 Go 文件应在同一 Go run 中独立成功");
    assert.equal(first.failed, 0);
    assert.equal(firstStatus.indexBackend?.engine, "go");
    assert.equal(firstStatus.indexBackend?.lastMetrics?.fallbackDocuments, 1);
    const firstDocument = service.database.getDocumentByPath(spreadsheetPath);
    assert(firstDocument);
    assert.match(String(firstDocument.warnings_json), /Go 原生 OOXML ZIP 打开失败/);
    assert.doesNotMatch(String(firstDocument.warnings_json), /TypeScript|SheetJS|Node/i);
    assert((await service.search({ query: marker, sort: "relevance" })).hits.some((hit) => hit.absolutePath === spreadsheetPath));

    const lastGoodChunkCount = Number(service.database.db.prepare(
      "SELECT COUNT(*) AS count FROM chunks WHERE document_id=?",
    ).get(firstDocument.id)?.count);
    assert(lastGoodChunkCount > 0);

    await writeFile(spreadsheetPath, Buffer.from([0x50, 0x4b, 0x03, 0x04, 0xff, 0xff, 0xff]));
    const second = await service.index({ full: false });
    const secondStatus = service.status();
    assert.equal(second.phase, "complete", "单文件纯 Go compatibility 失败不得让整个 Go run 失败");
    assert.equal(second.failed, 1);
    assert.equal(second.unchanged, 1, "普通 Go 文档应继续独立完成");
    assert.equal(secondStatus.indexBackend?.engine, "go");
    assert.equal(secondStatus.indexBackend?.lastMetrics?.fallbackDocuments, 1);

    const staleDocument = service.database.getDocumentByPath(spreadsheetPath);
    assert(staleDocument);
    assert.equal(Number(staleDocument.stale), 1);
    assert.match(String(staleDocument.extraction_error), /Go 原生 OOXML ZIP 打开失败/);
    assert.match(String(staleDocument.extraction_error), /纯 Go compatibility fallback 失败/);
    const retainedChunks = Number(service.database.db.prepare(
      "SELECT COUNT(*) AS count FROM chunks WHERE document_id=?",
    ).get(staleDocument.id)?.count);
    assert.equal(retainedChunks, lastGoodChunkCount, "compatibility 失败必须保留 last-good chunks");
    const issue = service.database.db.prepare(
      "SELECT code, message FROM index_issues WHERE path=? ORDER BY occurred_at DESC LIMIT 1",
    ).get(spreadsheetPath) as { code?: string; message?: string } | undefined;
    assert.equal(issue?.code, "extract_failed");
    assert.match(String(issue?.message), /纯 Go compatibility fallback 失败/);
  } finally {
    service.close();
  }
});

test("真实 Go XLSX 管线跨 16k 分块时重复一次表头并以无重叠 locator 覆盖每个输出行", async () => {
  const root = path.resolve("tests/.tmp/go-core-xlsx-chunk-boundary");
  const sourceRoot = path.join(root, "source");
  const configDir = path.join(root, "config");
  const dataDir = path.join(root, "data");
  await rm(root, { recursive: true, force: true });
  await Promise.all([
    mkdir(sourceRoot, { recursive: true }),
    mkdir(configDir, { recursive: true }),
    mkdir(dataDir, { recursive: true }),
  ]);

  const spreadsheetPath = path.join(sourceRoot, "chunk-boundary_20260901.xlsx");
  const rows: Array<Array<string | number>> = [["ID", "比例", "日期", "计算", "说明"]];
  for (let row = 1; row <= 420; row += 1) {
    rows.push([
      String(row).padStart(4, "0"),
      0.25,
      "2026-09-01",
      row === 1 ? 2 : row * 2,
      `第${row}行_${"边界说明".repeat(32)}`,
    ]);
  }
  const workbook = XLSX.utils.book_new();
  const sheet = XLSX.utils.aoa_to_sheet(rows);
  sheet.D2 = { t: "n", v: 2, f: "1+1" };
  XLSX.utils.book_append_sheet(workbook, sheet, "边界表");
  await writeFile(spreadsheetPath, XLSX.write(workbook, { bookType: "xlsx", type: "buffer" }) as Buffer);

  const service = await KnowledgeBaseService.create({ configDir, dataDir });
  try {
    const source = createSourceConfig({ id: "boundary", label: "boundary", kind: "table", rootPath: sourceRoot });
    source.includeExtensions = [".xlsx"];
    await service.saveConfig({
      ...service.config,
      sources: [source],
      indexing: { ...service.config.indexing, concurrency: 1 },
    });
    const run = await service.index({ full: true });
    const status = service.status();
    assert.equal(run.phase, "complete");
    assert.equal(run.indexed, 1);
    assert.equal(run.failed, 0);
    assert.equal(status.indexBackend?.engine, "go");
    assert.equal(status.indexBackend?.lastMetrics?.fallbackDocuments, 0);

    const stored = service.database.getDocumentByPath(spreadsheetPath);
    assert(stored);
    const chunks = service.database.db.prepare(
      "SELECT ordinal, locator, text FROM chunks WHERE document_id=? ORDER BY ordinal",
    ).all(stored.id) as Array<{ ordinal: number; locator: string; text: string }>;
    assert(chunks.length > 3, "fixture must cross the 16k spreadsheet chunk boundary");

    const seenRows = new Map<number, number>();
    let previousLast = 0;
    let combinedText = "";
    for (const chunk of chunks) {
      assert.equal((chunk.text.match(/字段 \| A=ID/g) ?? []).length, 1, `chunk ${chunk.ordinal} 表头必须恰好出现一次`);
      assert([...chunk.text].length <= 16_000, `chunk ${chunk.ordinal} 超过 16k：${[...chunk.text].length}`);
      const match = /^(.*)!([A-Z]+)(\d+):([A-Z]+)(\d+)$/.exec(chunk.locator);
      assert(match, `无效 locator：${chunk.locator}`);
      const startRow = Number(match[3]);
      const lastRow = Number(match[5]);
      assert.equal(startRow, previousLast + 1, `locator 必须连续且不重叠：${chunk.locator}`);
      const outputRows = chunk.text.split("\n").slice(1).map((line) => {
        const rowMatch = /^行\s+(\d+)\s*\|/.exec(line);
        assert(rowMatch, `输出行无法定位：${line}`);
        return Number(rowMatch[1]);
      });
      assert(outputRows.length > 0);
      assert.equal(outputRows[0], startRow);
      assert.equal(outputRows.at(-1), lastRow);
      assert.equal(outputRows.length, lastRow - startRow + 1);
      for (const row of outputRows) seenRows.set(row, (seenRows.get(row) ?? 0) + 1);
      previousLast = lastRow;
      combinedText += `${chunk.text}\n`;
    }
    assert.equal(previousLast, 421, "header row + 420 data rows must all be covered");
    for (let row = 1; row <= 421; row += 1) assert.equal(seenRows.get(row), 1, `row ${row} 必须恰好出现一次`);
    assert.match(combinedText, /字段 \| A=ID \| B=比例 \| C=日期 \| D=计算/);
    assert.match(combinedText, /行 2 \| A=0001 \| B=0\.25 \| C=2026-09-01 \| D=2 \{formula=1\+1\}/);
    assert.equal((combinedText.match(/formula=1\+1/g) ?? []).length, 1, "formula 只能跟随其单元格出现一次");
  } finally {
    service.close();
  }
});
