import { CodexAppServerClient } from "../dist/main/app-server-client.js";

const client = new CodexAppServerClient();
const timeout = setTimeout(() => {
  process.stderr.write("app-server smoke 总超时\n");
  process.exitCode = 1;
  void client.stop();
}, 25_000);

try {
  await client.start(process.env.DESIGN_RAG_CODEX_PATH || null);
  const account = await client.accountStatus();
  const models = await client.listModels();
  const threads = await client.request("thread/list", {
    limit: 1,
    sourceKinds: ["appServer"],
    useStateDbOnly: true,
  }, 10_000);
  process.stdout.write(`${JSON.stringify({
    status: "ok",
    codexVersion: account.codexVersion,
    account: {
      connected: account.connected,
      authMode: account.authMode,
      planType: account.planType,
    },
    threadListShape: threads && typeof threads === "object" ? Object.keys(threads) : [],
    models: models.map((model) => ({
      model: model.model,
      displayName: model.displayName,
      isDefault: model.isDefault,
      reasoningEfforts: model.supportedReasoningEfforts.map((option) => option.value),
    })),
  }, null, 2)}\n`);
} finally {
  clearTimeout(timeout);
  await client.stop();
}
