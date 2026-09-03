import { readFile } from "node:fs/promises";
import path from "node:path";
import JSZip from "jszip";
import { classifySection } from "../classifier.js";
import type { ExtractedBlock, ExtractedDocument } from "../types.js";

interface XmindTopic {
  id?: string;
  title?: string;
  notes?: { plain?: { content?: string } };
  children?: Record<string, XmindTopic[]>;
}

interface XmindSheet {
  id?: string;
  title?: string;
  rootTopic?: XmindTopic;
}

function walkTopic(topic: XmindTopic, sheetTitle: string, parents: string[], blocks: ExtractedBlock[]): void {
  const title = topic.title?.trim() || "未命名主题";
  const headingPath = [...parents, title];
  const note = topic.notes?.plain?.content?.trim();
  blocks.push({
    ordinal: blocks.length,
    text: note ? `${title}\n${note}` : title,
    headingPath,
    sectionType: classifySection(headingPath, note ?? title),
    locator: `XMind/${sheetTitle}/${headingPath.join("/")}`,
    metadata: { topicId: topic.id ?? "" },
  });
  for (const topics of Object.values(topic.children ?? {})) {
    for (const child of topics ?? []) walkTopic(child, sheetTitle, headingPath, blocks);
  }
}

export async function extractXmind(filePath: string, inputBuffer?: Buffer): Promise<ExtractedDocument> {
  const zip = await JSZip.loadAsync(inputBuffer ?? await readFile(filePath));
  const blocks: ExtractedBlock[] = [];
  const contentJson = zip.file("content.json");
  if (contentJson) {
    const sheets = JSON.parse(await contentJson.async("string")) as XmindSheet[];
    for (const sheet of sheets) {
      if (sheet.rootTopic) walkTopic(sheet.rootTopic, sheet.title ?? "工作表", [], blocks);
    }
  } else {
    const contentXml = zip.file("content.xml");
    if (contentXml) {
      const xml = await contentXml.async("string");
      const titles = [...xml.matchAll(/<title[^>]*>(?:<!\[CDATA\[)?([\s\S]*?)(?:\]\]>)?<\/title>/gi)]
        .map((match) => match[1]?.replace(/<[^>]+>/g, "").trim())
        .filter((value): value is string => Boolean(value));
      titles.forEach((title, index) => {
        blocks.push({
          ordinal: index,
          text: title,
          headingPath: [title],
          sectionType: classifySection([title], title),
          locator: `XMind 主题 ${index + 1}`,
        });
      });
    }
  }
  return {
    title: path.basename(filePath, path.extname(filePath)),
    blocks,
    embeddedModifiedAt: null,
    warnings: blocks.length === 0 ? ["XMind 未提取到主题"] : [],
    needsOcr: false,
  };
}
