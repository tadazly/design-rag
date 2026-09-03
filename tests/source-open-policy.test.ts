import assert from "node:assert/strict";
import test from "node:test";
import { parseRegistryString, usesMicrosoftExcel } from "../src/main/source-open-policy.js";

test("Windows 文件关联可区分 Microsoft Excel 与 WPS", () => {
  assert.equal(parseRegistryString("    ProgId    REG_SZ    ET.Xlsx.6\r\n"), "ET.Xlsx.6");
  assert.equal(parseRegistryString("    ProgId    REG_SZ    Excel.Sheet.12\r\n"), "Excel.Sheet.12");
  assert.equal(usesMicrosoftExcel("ET.Xlsx.6"), false);
  assert.equal(usesMicrosoftExcel("Excel.Sheet.12"), true);
  assert.equal(usesMicrosoftExcel(null), false);
});
