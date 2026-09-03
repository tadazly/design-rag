import { execFile, spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { createServer } from "node:net";
import { appendFile, mkdir, readFile, stat, writeFile } from "node:fs/promises";
import path from "node:path";
import process from "node:process";
import { promisify } from "node:util";
import { verifyCurrentElectronArtifact } from "./electron-acceptance-artifact.mjs";

const execFileAsync = promisify(execFile);
const projectRoot = path.resolve(import.meta.dirname, "..");
const executable = path.resolve(process.argv.find((value) => value.startsWith("--exe="))?.slice(6)
  ?? path.join(projectRoot, "release", "win-unpacked", "drag-gui.exe"));
const runId = new Date().toISOString().replaceAll(/[:.]/g, "-");
const acceptanceRoot = path.join(projectRoot, "tests", ".tmp", `electron-startup-incremental-${runId}`);
const configDir = path.join(acceptanceRoot, "config");
const dataDir = path.join(acceptanceRoot, "data");
const isolatedAppData = path.join(acceptanceRoot, "appdata");
const isolatedLocalAppData = path.join(acceptanceRoot, "local-appdata");
const baselineProfileDir = path.join(acceptanceRoot, "electron-profile-baseline");
const startupProfileDir = path.join(acceptanceRoot, "electron-profile-startup");
const sourceDir = path.join(acceptanceRoot, "已有来源");
const sourceFile = path.join(sourceDir, "已有来源启动自动增量_20260901.md");
const configPath = path.join(configDir, "config.json");
const reportPath = path.join(acceptanceRoot, "electron-startup-incremental-report.json");
const marker = `STARTUP_AUTO_INCREMENTAL_E2E_${Date.now()}`;

const initialConfig = {
  schemaVersion: 1,
  sources: [{
    id: "startup-existing-source",
    label: "已有来源启动验收",
    kind: "design",
    rootPath: sourceDir,
    enabled: true,
    includeExtensions: [".md"],
    excludeDirectoryNames: [".git", ".svn", "node_modules", "dist", "build", "temp", "tmp", "__macosx"],
    maxFileBytes: 128 * 1024 * 1024,
  }],
  search: {
    defaultSort: "newest",
    defaultLimit: 12,
    maxEvidenceChars: 24_000,
    synonymExpansion: true,
    embedding: {
      enabled: false,
      provider: "ollama",
      endpoint: "http://127.0.0.1:11434/api/embed",
      model: "embeddinggemma",
      timeoutMs: 30_000,
    },
  },
  indexing: { automaticScan: true, scanIntervalMinutes: 10, concurrency: 4 },
  codex: { codexPath: null, model: null, reasoningEffort: null },
};

async function freePort() {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      if (!address || typeof address === "string") return reject(new Error("无法分配调试端口"));
      server.close((error) => error ? reject(error) : resolve(address.port));
    });
  });
}

async function waitFor(predicate, label, timeoutMs = 30_000, intervalMs = 100) {
  const deadline = Date.now() + timeoutMs;
  let lastError;
  while (Date.now() < deadline) {
    try {
      const value = await predicate();
      if (value) return value;
    } catch (error) {
      lastError = error;
    }
    await new Promise((resolve) => setTimeout(resolve, intervalMs));
  }
  throw new Error(`等待超时：${label}${lastError ? `；最后错误：${lastError}` : ""}`);
}

async function sha256(filePath) {
  return createHash("sha256").update(await readFile(filePath)).digest("hex");
}

async function describeFile(filePath) {
  const info = await stat(filePath);
  return {
    path: filePath,
    sizeBytes: info.size,
    modifiedAt: info.mtime.toISOString(),
    mtimeMs: info.mtimeMs,
    sha256: await sha256(filePath),
  };
}

class CdpClient {
  constructor(url) {
    this.nextId = 1;
    this.pending = new Map();
    this.socket = new WebSocket(url);
  }

  async open() {
    await new Promise((resolve, reject) => {
      this.socket.addEventListener("open", resolve, { once: true });
      this.socket.addEventListener("error", reject, { once: true });
    });
    this.socket.addEventListener("message", (event) => {
      const message = JSON.parse(String(event.data));
      if (!message.id) return;
      const waiter = this.pending.get(message.id);
      if (!waiter) return;
      this.pending.delete(message.id);
      if (message.error) waiter.reject(new Error(`${waiter.method}: ${message.error.message}`));
      else waiter.resolve(message.result ?? {});
    });
  }

  send(method, params = {}) {
    const id = this.nextId++;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { method, resolve, reject });
      this.socket.send(JSON.stringify({ id, method, params }));
    });
  }

  close() {
    this.socket.close();
  }
}

async function evaluate(client, expression) {
  const result = await client.send("Runtime.evaluate", {
    expression,
    awaitPromise: true,
    returnByValue: true,
    userGesture: true,
  });
  if (result.exceptionDetails) throw new Error(result.exceptionDetails.text ?? "renderer evaluate 失败");
  return result.result?.value;
}

async function screenshot(client, fileName) {
  const result = await client.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false });
  const outputPath = path.join(acceptanceRoot, fileName);
  await writeFile(outputPath, Buffer.from(result.data, "base64"));
  return outputPath;
}

async function navigateToSettings(client) {
  const navigated = await evaluate(client, `(() => {
    const button = [...document.querySelectorAll('button')].find((item) => item.textContent?.includes('资料位置与索引'));
    if (!button) return false;
    button.click();
    return true;
  })()`);
  if (!navigated) throw new Error("未找到资料位置与索引导航按钮");
  await waitFor(() => evaluate(client, "Boolean(document.querySelector('.settings-page'))"), "settings page");
}

async function launchElectron(label, profileDir) {
  await mkdir(profileDir, { recursive: true });
  const port = await freePort();
  const launchedAt = new Date().toISOString();
  const child = spawn(executable, [
    `--user-data-dir=${profileDir}`,
    `--remote-debugging-port=${port}`,
    "--remote-allow-origins=*",
  ], {
    cwd: projectRoot,
    env: {
      ...process.env,
      APPDATA: isolatedAppData,
      LOCALAPPDATA: isolatedLocalAppData,
      DESIGN_RAG_CONFIG_DIR: configDir,
      DESIGN_RAG_DATA_DIR: dataDir,
    },
    shell: false,
    windowsHide: false,
    stdio: ["ignore", "pipe", "pipe"],
  });

  let stdout = "";
  let stderr = "";
  child.stdout.setEncoding("utf8");
  child.stderr.setEncoding("utf8");
  child.stdout.on("data", (chunk) => { stdout += chunk; });
  child.stderr.on("data", (chunk) => { stderr += chunk; });

  const page = await waitFor(async () => {
    const response = await fetch(`http://127.0.0.1:${port}/json/list`);
    if (!response.ok) return null;
    const pages = await response.json();
    return pages.find((item) => item.type === "page" && item.webSocketDebuggerUrl) ?? null;
  }, `${label} Electron DevTools page`, 45_000);

  const client = new CdpClient(page.webSocketDebuggerUrl);
  await client.open();
  await Promise.all([client.send("Runtime.enable"), client.send("Page.enable")]);
  await waitFor(() => evaluate(client, "document.readyState === 'complete'"), `${label} document ready`, 15_000);
  await waitFor(() => evaluate(client, "Boolean(document.querySelector('.app-shell'))"), `${label} drag application shell`, 60_000);
  return {
    label,
    child,
    client,
    launchedAt,
    page,
    get stdout() { return stdout; },
    get stderr() { return stderr; },
  };
}

function pidExists(pid) {
  if (!pid) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

async function waitForChildExit(child, timeoutMs = 10_000) {
  if (child.exitCode !== null) return;
  await new Promise((resolve) => {
    const timer = setTimeout(resolve, timeoutMs);
    child.once("exit", () => {
      clearTimeout(timer);
      resolve();
    });
  });
}

async function stopElectron(run) {
  if (!run) return null;
  run.client?.close();
  const pid = run.child.pid;
  let taskkillStdout = "";
  let taskkillStderr = "";
  let taskkillExit = "not-needed";
  if (pidExists(pid)) {
    try {
      const result = await execFileAsync("taskkill.exe", ["/PID", String(pid), "/T", "/F"], {
        encoding: "utf8",
        windowsHide: true,
      });
      taskkillStdout = result.stdout ?? "";
      taskkillStderr = result.stderr ?? "";
      taskkillExit = "success";
    } catch (error) {
      taskkillStdout = error.stdout ?? "";
      taskkillStderr = error.stderr ?? String(error);
      taskkillExit = "error";
    }
  }
  await waitForChildExit(run.child);
  await waitFor(() => !pidExists(pid), `${run.label} exact PID ${pid} exit`, 10_000);
  if (taskkillExit === "error") {
    throw new Error(`${run.label} taskkill /T 失败：${taskkillStderr || taskkillStdout}`);
  }
  return {
    label: run.label,
    rootPid: pid,
    exactTreeKill: true,
    taskkillExit,
    taskkillStdout: taskkillStdout.trim(),
    taskkillStderr: taskkillStderr.trim(),
    rootPidAbsent: !pidExists(pid),
    exitedAt: new Date().toISOString(),
  };
}

await Promise.all([
  mkdir(configDir, { recursive: true }),
  mkdir(dataDir, { recursive: true }),
  mkdir(isolatedAppData, { recursive: true }),
  mkdir(isolatedLocalAppData, { recursive: true }),
  mkdir(sourceDir, { recursive: true }),
]);
await writeFile(configPath, `${JSON.stringify(initialConfig, null, 2)}\n`, "utf8");
await writeFile(sourceFile, "# 已有来源启动自动增量验收\n\n基线内容：STARTUP_BASELINE_E2E_20260901。\n", "utf8");

let baselineRun = null;
let startupRun = null;
let baselineCleanup = null;
let startupCleanup = null;
let report;
let artifact = null;

try {
  artifact = await verifyCurrentElectronArtifact(projectRoot, executable);
  baselineRun = await launchElectron("baseline", baselineProfileDir);
  await navigateToSettings(baselineRun.client);
  const baselineStatus = await waitFor(async () => {
    const snapshot = await evaluate(baselineRun.client, "window.drag.getSnapshot()");
    const lastRun = snapshot?.index?.lastRun;
    return snapshot?.index?.activeRun === null
      && lastRun?.phase === "complete"
      && lastRun.indexed >= 1
      && snapshot.index.documentCount >= 1
      ? snapshot.index
      : null;
  }, "baseline startup index complete", 60_000);
  const baselineNoticeVisible = await evaluate(baselineRun.client, "document.body.innerText.includes('索引已自动更新')");
  if (!baselineNoticeVisible) throw new Error("基线启动索引完成，但 UI 未显示自动更新 notice");
  const baselineScreenshot = await screenshot(baselineRun.client, "baseline-startup-indexed.png");
  const baselineSearch = await evaluate(
    baselineRun.client,
    `window.drag.search(${JSON.stringify({ query: "STARTUP_BASELINE_E2E_20260901", sort: "relevance", limit: 5 })})`,
  );
  if (!baselineSearch?.hits?.length) throw new Error("基线启动索引完成后未检索到基线内容");
  const baselineLaunchedAt = baselineRun.launchedAt;
  const baselineStdout = baselineRun.stdout;
  const baselineStderr = baselineRun.stderr;
  baselineCleanup = await stopElectron(baselineRun);
  baselineRun = null;

  const sourceBeforeOfflineMutation = await describeFile(sourceFile);
  const offlineMutationStartedAt = new Date().toISOString();
  await appendFile(sourceFile, `\n启动前离线修改：${marker}。\n`, "utf8");
  const sourceAtSecondLaunch = await describeFile(sourceFile);
  if (sourceAtSecondLaunch.sha256 === sourceBeforeOfflineMutation.sha256) {
    throw new Error("离线修改没有改变源文件哈希");
  }
  const offlineMutationCompletedAt = new Date().toISOString();
  const mtimeSettleMs = Math.max(0, Math.ceil(sourceAtSecondLaunch.mtimeMs - Date.now()) + 25);
  if (mtimeSettleMs > 0) await new Promise((resolve) => setTimeout(resolve, mtimeSettleMs));

  startupRun = await launchElectron("startup", startupProfileDir);
  await navigateToSettings(startupRun.client);
  const startupStatus = await waitFor(async () => {
    const snapshot = await evaluate(startupRun.client, "window.drag.getSnapshot()");
    const lastRun = snapshot?.index?.lastRun;
    return snapshot?.index?.activeRun === null
      && snapshot.index.indexRevision > baselineStatus.indexRevision
      && lastRun?.phase === "complete"
      && lastRun.runId !== baselineStatus.lastRun?.runId
      && lastRun.indexed >= 1
      ? snapshot.index
      : null;
  }, "second launch startup incremental complete", 60_000);

  const bodyTextAfterStartup = await evaluate(startupRun.client, "document.body.innerText");
  const startupNoticeVisible = bodyTextAfterStartup.includes("索引已自动更新");
  if (!startupNoticeVisible) throw new Error("第二次启动自动增量完成，但 UI notice 不可见");
  const startupScreenshot = await screenshot(startupRun.client, "startup-auto-incremental-complete.png");
  const startupSearch = await evaluate(
    startupRun.client,
    `window.drag.search(${JSON.stringify({ query: marker, sort: "relevance", limit: 5 })})`,
  );
  const citationId = startupSearch?.hits?.[0]?.excerpts?.[0]?.citation?.citationId;
  if (!citationId) throw new Error("第二次启动自动增量后未检索到新内容 citation");

  const { KnowledgeBaseService } = await import("../dist/core/service.js");
  const knowledge = await KnowledgeBaseService.create({ configDir, dataDir });
  let startupCitation;
  try {
    startupCitation = knowledge.readCitation(citationId, startupSearch.indexRevision);
  } finally {
    knowledge.close();
  }
  if (!startupCitation.content.includes(marker) || startupCitation.changed) {
    throw new Error("第二次启动的 citation 未命中新内容，或引用已发生变化");
  }

  const sourceAfterAcceptance = await describeFile(sourceFile);
  const noSourceWritesAfterSecondLaunch = sourceAfterAcceptance.sha256 === sourceAtSecondLaunch.sha256
    && sourceAfterAcceptance.mtimeMs === sourceAtSecondLaunch.mtimeMs;
  if (!noSourceWritesAfterSecondLaunch) {
    throw new Error("第二次 GUI 启动后源文件发生了额外写入，无法排除 watcher 事件");
  }

  const startupStdout = startupRun.stdout;
  const startupStderr = startupRun.stderr;
  const rendererTitle = await evaluate(startupRun.client, "document.title");
  const secondLaunchAt = startupRun.launchedAt;
  startupCleanup = await stopElectron(startupRun);
  startupRun = null;

  const startupStartedAtMs = Date.parse(startupStatus.lastRun.startedAt);
  const secondLaunchAtMs = Date.parse(secondLaunchAt);
  const offlineMutationCompletedBeforeSecondLaunch = Date.parse(offlineMutationCompletedAt) <= secondLaunchAtMs
    && sourceAtSecondLaunch.mtimeMs <= secondLaunchAtMs;
  if (!offlineMutationCompletedBeforeSecondLaunch) {
    throw new Error("离线修改时间未稳定在第二次 GUI 启动前，无法排除 watcher 冒充 startup 增量");
  }
  const scheduledIntervalMs = initialConfig.indexing.scanIntervalMinutes * 60_000;
  const startupRunBeganBeforeScheduledInterval = startupStartedAtMs >= secondLaunchAtMs
    && startupStartedAtMs - secondLaunchAtMs < scheduledIntervalMs;
  if (!startupRunBeganBeforeScheduledInterval) {
    throw new Error("第二次索引运行未在启动窗口内开始，无法证明是 startup 自动增量");
  }

  report = {
    status: "PASS",
    acceptance: "existing-source-startup-auto-incremental",
    executable,
    artifact,
    acceptanceRoot,
    isolatedState: {
      configDir,
      dataDir,
      appData: isolatedAppData,
      localAppData: isolatedLocalAppData,
      baselineProfileDir,
      startupProfileDir,
    },
    configPath,
    dataDir,
    sourceDir,
    sourceFile,
    marker,
    automaticScan: initialConfig.indexing.automaticScan,
    scheduledIntervalMinutes: initialConfig.indexing.scanIntervalMinutes,
    baseline: {
      launchedAt: baselineLaunchedAt,
      indexStatus: baselineStatus,
      noticeVisible: baselineNoticeVisible,
      searchHit: baselineSearch.hits[0],
      screenshot: baselineScreenshot,
      stdout: baselineStdout,
      stderr: baselineStderr,
      cleanup: baselineCleanup,
    },
    offlineMutation: {
      startedAt: offlineMutationStartedAt,
      completedAt: offlineMutationCompletedAt,
      completedBeforeSecondLaunch: offlineMutationCompletedBeforeSecondLaunch,
      before: sourceBeforeOfflineMutation,
      atSecondLaunch: sourceAtSecondLaunch,
    },
    startup: {
      launchedAt: secondLaunchAt,
      indexStatus: startupStatus,
      noticeVisible: startupNoticeVisible,
      searchHit: startupSearch.hits[0],
      citation: startupCitation,
      screenshot: startupScreenshot,
      rendererTitle,
      stdout: startupStdout,
      stderr: startupStderr,
      cleanup: startupCleanup,
    },
    startupProof: {
      baselineRunId: baselineStatus.lastRun?.runId,
      startupRunId: startupStatus.lastRun?.runId,
      runIdChanged: startupStatus.lastRun?.runId !== baselineStatus.lastRun?.runId,
      baselineRevision: baselineStatus.indexRevision,
      startupRevision: startupStatus.indexRevision,
      revisionIncreased: startupStatus.indexRevision > baselineStatus.indexRevision,
      startupRunBeganAfterSecondLaunch: startupStartedAtMs >= secondLaunchAtMs,
      startupRunBeganBeforeScheduledInterval,
      startupRunDelayMs: startupStartedAtMs - secondLaunchAtMs,
      noSourceWritesAfterSecondLaunch,
      sourceAfterAcceptance,
      watcherExcludedByTestProtocol: noSourceWritesAfterSecondLaunch,
    },
    evidenceRetained: true,
    cleanupComplete: Boolean(baselineCleanup?.rootPidAbsent && startupCleanup?.rootPidAbsent),
  };
} catch (error) {
  let cleanupError = null;
  try {
    if (baselineRun) baselineCleanup = await stopElectron(baselineRun);
    if (startupRun) startupCleanup = await stopElectron(startupRun);
  } catch (cleanupFailure) {
    cleanupError = cleanupFailure instanceof Error ? cleanupFailure.stack ?? cleanupFailure.message : String(cleanupFailure);
  }
  const message = error instanceof Error ? error.stack ?? error.message : String(error);
  const staleArtifact = message.includes("拒绝用 stale EXE 验收");
  report = {
    status: staleArtifact ? "BLOCKED" : "FAIL",
    acceptance: "existing-source-startup-auto-incremental",
    executable,
    artifact,
    acceptanceRoot,
    configPath,
    dataDir,
    isolatedState: {
      configDir,
      dataDir,
      appData: isolatedAppData,
      localAppData: isolatedLocalAppData,
      baselineProfileDir,
      startupProfileDir,
    },
    sourceDir,
    sourceFile,
    marker,
    error: message,
    blockedReason: staleArtifact ? "release/win-unpacked/drag-gui.exe 与当前 dist 不一致，未启动 GUI" : null,
    cleanupError,
    baselineCleanup,
    startupCleanup,
  };
  process.exitCode = 1;
}

await writeFile(reportPath, `${JSON.stringify(report, null, 2)}\n`, "utf8");
if (report.status === "PASS") process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
else process.stderr.write(`${JSON.stringify(report, null, 2)}\n`);
