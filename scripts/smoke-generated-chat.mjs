import { writeFile } from "node:fs/promises";
import path from "node:path";
import { KnowledgeBaseService } from "../dist/core/service.js";
import { ChatController } from "../dist/main/chat-controller.js";

if (process.env.RUN_LIVE_CODEX_SMOKE !== "1") {
  throw new Error("该脚本会发起真实 Codex 回合。请显式设置 RUN_LIVE_CODEX_SMOKE=1。");
}

const prompts = process.argv.slice(2).length > 0 ? process.argv.slice(2) : [
  "找到最新的一个 888活动，说明一下里面的玩法和产出逻辑",
  "我要新增一个扭蛋机，需要配置哪些表格",
  "我要复用妖王888，需要配哪些表，帮我把新的配置列出来",
];
const reportName = process.env.DESIGN_RAG_CHAT_REPORT_NAME?.trim() || "generated-chat-report.json";

function waitForAnswer(controller, localThreadId, timeoutMs = 600_000) {
  const startedAt = Date.now();
  return new Promise((resolve, reject) => {
    const timer = setInterval(() => {
      const thread = controller.threads.get(localThreadId);
      const answer = thread?.messages.findLast((message) => message.role === "assistant");
      if (answer && answer.status !== "streaming") {
        clearInterval(timer);
        resolve({ answer, elapsedMs: Date.now() - startedAt });
        return;
      }
      if (Date.now() - startedAt > timeoutMs) {
        clearInterval(timer);
        reject(new Error(`等待生成式回答超时：${localThreadId}`));
      }
    }, 250);
  });
}

const knowledge = await KnowledgeBaseService.create();
// The smoke reuses the already-built full corpus. Starting an automatic
// incremental scan here would compete with the two latency-sensitive turns.
knowledge.config.indexing.automaticScan = false;
const controller = new ChatController(knowledge);
const results = [];
const createdThreadIds = [];

try {
  await controller.initialize();
  const account = controller.snapshot().account;
  if (!account.connected) throw new Error(account.error ?? "ChatGPT 未登录");

  for (const prompt of prompts) {
    const thread = controller.createThread();
    createdThreadIds.push(thread.id);
    await controller.sendMessage(prompt);
    const { answer, elapsedMs } = await waitForAnswer(controller, thread.id);
    const citations = answer.citationIds.map((citationId) => {
      const read = knowledge.readCitation(citationId);
      return {
        citationId,
        title: knowledge.database.getDocument(read.citation.documentId)?.title ?? null,
        locator: read.citation.locator,
        path: read.citation.relativePath,
      };
    });
    results.push({
      prompt,
      status: answer.status,
      elapsedMs,
      answer: answer.text,
      citations,
      retrievedTitles: controller.snapshot().evidence?.hits.map((hit) => hit.title) ?? [],
    });
    if (answer.status !== "complete") throw new Error(`回答失败：${answer.text}`);
    if (citations.length === 0) throw new Error(`回答没有可核验引用：${prompt}`);
    if (/DRAG:chunk_/i.test(answer.text)) throw new Error(`回答仍显示内部 chunk ID：${prompt}`);
    if (!answer.text.includes("证据事实") || !answer.text.includes("推断") || !answer.text.includes("待确认")) {
      throw new Error(`回答未明确区分证据事实、推断和待确认：${prompt}`);
    }
    if (!/\[[^\]]+\]\(<[A-Za-z]:\/[^>]+>\)\s*·\s*`[^`]+`/.test(answer.text)) {
      throw new Error(`回答没有可点击本机文件与 locator：${prompt}`);
    }
  }

  const report = {
    status: "ok",
    codexVersion: controller.snapshot().account.codexVersion,
    authMode: controller.snapshot().account.authMode,
    planType: controller.snapshot().account.planType,
    results,
  };
  const outputPath = path.join(path.dirname(knowledge.configStore.dataDir), reportName);
  await writeFile(outputPath, `${JSON.stringify(report, null, 2)}\n`, "utf8");
  process.stdout.write(`${JSON.stringify({ ...report, outputPath }, null, 2)}\n`);
} catch (error) {
  const outputPath = path.join(path.dirname(knowledge.configStore.dataDir), reportName);
  await writeFile(outputPath, `${JSON.stringify({
    status: "FAIL",
    error: error instanceof Error ? error.message : String(error),
    results,
  }, null, 2)}\n`, "utf8");
  throw error;
} finally {
  for (const threadId of createdThreadIds.reverse()) {
    await controller.deleteThread(threadId).catch(() => undefined);
  }
  controller.dispose();
  await controller.appServer.stop().catch(() => undefined);
  knowledge.close();
}
