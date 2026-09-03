import assert from "node:assert/strict";
import test from "node:test";
import { parsePdfPage, parseSpreadsheetLocation } from "../src/shared/source-location.js";

test("解析工作表范围与 PDF 页码定位", () => {
  assert.deepEqual(parseSpreadsheetLocation("玩法&逻辑!C92:F176"), { sheetName: "玩法&逻辑", range: "C92:F176" });
  assert.deepEqual(parseSpreadsheetLocation("'版本 修改记录'!a1:d18"), { sheetName: "版本 修改记录", range: "A1:D18" });
  assert.equal(parseSpreadsheetLocation("第 12 页"), null);
  assert.equal(parsePdfPage("page 12"), 12);
  assert.equal(parsePdfPage("页: 8"), 8);
  assert.equal(parsePdfPage("无页码"), null);
});
