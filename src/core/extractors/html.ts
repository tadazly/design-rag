import * as cheerio from "cheerio";
import { classifySection } from "../classifier.js";
import type { ExtractedBlock } from "../types.js";

function isHeadingLike(text: string): boolean {
  if (text.length < 2 || text.length > 80) return false;
  if (/[。！？!?；;]$/.test(text)) return false;
  return /^(?:第[一二三四五六七八九十百\d]+[章节部分]|[一二三四五六七八九十]+[、.]|\d+(?:\.\d+){0,3}[、.\s]|【[^】]+】)/.test(text)
    || /(概述|版本|修订|流程|玩法|规则|面板|逻辑|配置|配表|奖励|数值|统计|美术|原画|动画|需求)$/.test(text);
}

export function blocksFromHtml(html: string, locatorPrefix = "段落"): ExtractedBlock[] {
  const $ = cheerio.load(html);
  const blocks: ExtractedBlock[] = [];
  let ordinal = 0;
  let paragraph = 0;
  let tableIndex = 0;
  const headingStack: string[] = [];

  $("body").find("h1,h2,h3,h4,h5,h6,p,table,li").each((_, element) => {
    const tag = element.tagName.toLowerCase();
    if ($(element).parents("table").length > 0 && tag !== "table") return;
    if (tag === "table") {
      tableIndex += 1;
      const rows: string[] = [];
      $(element).find("tr").each((__, row) => {
        const cells = $(row).find("th,td").map((___, cell) => $(cell).text().replace(/\s+/g, " ").trim()).get();
        if (cells.some(Boolean)) rows.push(cells.join(" | "));
      });
      if (rows.length > 0) {
        const text = rows.join("\n");
        blocks.push({
          ordinal,
          text,
          headingPath: [...headingStack],
          sectionType: classifySection(headingStack, text),
          locator: `表格 ${tableIndex} 行 1-${rows.length}`,
          metadata: { tableIndex, rowStart: 1, rowEnd: rows.length },
        });
        ordinal += 1;
      }
      return;
    }

    const text = $(element).text().replace(/\s+/g, " ").trim();
    if (!text) return;
    paragraph += 1;
    const explicitLevel = /^h[1-6]$/.test(tag) ? Number(tag.slice(1)) : null;
    if (explicitLevel !== null || isHeadingLike(text)) {
      const level = explicitLevel ?? Math.min(6, Math.max(1, (text.match(/\./g)?.length ?? 0) + 1));
      headingStack.splice(level - 1);
      headingStack[level - 1] = text;
      return;
    }
    blocks.push({
      ordinal,
      text,
      headingPath: [...headingStack],
      sectionType: classifySection(headingStack, text),
      locator: `${locatorPrefix} ${paragraph}`,
      metadata: { paragraph },
    });
    ordinal += 1;
  });
  return blocks;
}
