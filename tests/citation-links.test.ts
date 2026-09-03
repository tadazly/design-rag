import assert from "node:assert/strict";
import test from "node:test";
import type { ChatCitation } from "../src/shared/contracts.js";
import { bindCitationLinks, citationMarkdown, resolveMessageUrl } from "../src/renderer/citation-links.js";

const citation: ChatCitation = {
  citationId: "DRAG:2.test",
  label: "策划案 [WPS].xlsx · 玩法!A1:B8",
  title: "策划案 [WPS]",
  relativePath: "2026年度\\策划案 [WPS].xlsx",
  absolutePath: "D:\\资料库\\2026年度\\策划案 [WPS].xlsx",
  locator: "玩法!A1:B8",
  sourceKind: "design",
};
const lookup = new Map([[citation.citationId, citation]]);

test("回答中的已验证本地来源路径会重新绑定到 citation IPC", () => {
  assert.equal(
    resolveMessageUrl("D:/资料库/2026年度/策划案 [WPS].xlsx", lookup),
    "drag-citation:DRAG:2.test",
  );
  assert.equal(
    resolveMessageUrl("file:///D:/%E8%B5%84%E6%96%99%E5%BA%93/2026%E5%B9%B4%E5%BA%A6/%E7%AD%96%E5%88%92%E6%A1%88%20%5BWPS%5D.xlsx", lookup),
    "drag-citation:DRAG:2.test",
  );
});

test("回答链接只放行网络 URL 与本轮已验证 citation", () => {
  assert.equal(resolveMessageUrl("https://example.com/source", lookup), "https://example.com/source");
  assert.equal(resolveMessageUrl("drag-citation:DRAG:2.unknown", lookup), "");
  assert.equal(resolveMessageUrl("D:/资料库/其他.xlsx", lookup), "");
  assert.match(citationMarkdown("来源 [[DRAG:2.test]]", lookup), /drag-citation:DRAG:2\.test/);
  assert.equal(citationMarkdown("来源 [[DRAG:2.unknown]]", lookup), "来源 [未验证引用]");
});

test("同一文件的多个 locator 会精确绑定各自 citationId", () => {
  const second = { ...citation, citationId: "DRAG:2.second", label: "策划案 [WPS].xlsx · 配置!C2:D9", locator: "配置!C2:D9" };
  const repeatedLookup = new Map([[citation.citationId, citation], [second.citationId, second]]);
  const text = [
    "[策划案 \\[WPS\\].xlsx](<D:/资料库/2026年度/策划案 [WPS].xlsx>) · `玩法!A1:B8`",
    "[策划案 \\[WPS\\].xlsx](<D:/资料库/2026年度/策划案 [WPS].xlsx>) · `配置!C2:D9`",
  ].join("\n");
  const bound = bindCitationLinks(text, repeatedLookup);
  assert.match(bound, /drag-citation:DRAG:2\.test/);
  assert.match(bound, /drag-citation:DRAG:2\.second/);
  assert.equal(resolveMessageUrl("D:/资料库/2026年度/策划案 [WPS].xlsx", repeatedLookup), "");
});
