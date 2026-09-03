import assert from "node:assert/strict";
import { execFile, spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { createServer } from "node:net";
import { access, appendFile, mkdir, readFile, stat, writeFile } from "node:fs/promises";
import path from "node:path";
import process from "node:process";
import { promisify } from "node:util";
import { verifyCurrentElectronArtifact } from "./electron-acceptance-artifact.mjs";

const execFileAsync = promisify(execFile);
const projectRoot = path.resolve(import.meta.dirname, "..");
const executable = path.resolve(process.argv.find((value) => value.startsWith("--exe="))?.slice(6)
  ?? path.join(projectRoot, "release", "win-unpacked", "drag-gui.exe"));
const runId = new Date().toISOString().replaceAll(/[:.]/g, "-");
const acceptanceRoot = path.join(projectRoot, "tests", ".tmp", `electron-settings-${runId}`);
const configDir = path.join(acceptanceRoot, "config");
const dataDir = path.join(acceptanceRoot, "data");
const profileDir = path.join(acceptanceRoot, "electron-profile");
const isolatedAppData = path.join(acceptanceRoot, "appdata");
const isolatedLocalAppData = path.join(acceptanceRoot, "local-appdata");
const primarySourceDir = path.join(acceptanceRoot, "策划案 拖入来源");
const controlSourceDir = path.join(acceptanceRoot, "配置表 控制来源");
const partialSourceDir = path.join(acceptanceRoot, "部分失败 来源");
const primarySourceFile = path.join(primarySourceDir, "Electron拖入验收_20260901.md");
const controlSourceFile = path.join(controlSourceDir, "Electron控制来源_20260901.csv");
const partialGoodFile = path.join(partialSourceDir, "部分成功_20260901.md");
const partialBrokenFile = path.join(partialSourceDir, "部分失败_20260901.docx");
const configPath = path.join(configDir, "config.json");
const reportPath = path.join(acceptanceRoot, "electron-settings-report.json");
const exerciseNativeBrowse = process.env.DESIGN_RAG_GUI_NATIVE_BROWSE === "1";
const primaryMarker = "ELECTRON_DROP_E2E_20260901";
const watcherMarker = "WATCHER_UPDATE_E2E_20260901";
const controlMarker = "CONTROL_SOURCE_E2E_20260901";
const partialMarker = "PARTIAL_SUCCESS_E2E_20260901";

const initialConfig = {
  schemaVersion: 1,
  sources: [],
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

async function readConfig() {
  return JSON.parse(await readFile(configPath, "utf8"));
}

async function describeFile(filePath) {
  const [value, info] = await Promise.all([readFile(filePath), stat(filePath)]);
  return {
    path: filePath,
    sizeBytes: info.size,
    modifiedAt: info.mtime.toISOString(),
    sha256: createHash("sha256").update(value).digest("hex"),
  };
}

async function snapshot(client) {
  return evaluate(client, "window.drag.getSnapshot()");
}

async function search(client, query, sourceIds) {
  return evaluate(client, `window.drag.search(${JSON.stringify({ query, sort: "relevance", limit: 5, sourceIds })})`);
}

async function openAddDialog(client) {
  const clicked = await evaluate(client, `(() => {
    const button = document.querySelector('.source-drop-action');
    if (!button || button.disabled) return false;
    button.click();
    return true;
  })()`);
  assert.equal(clicked, true, "未找到可用的添加资料源入口");
  await waitFor(() => evaluate(client, "Boolean(document.querySelector('.source-dialog'))"), "source dialog");
}

async function closeAddDialog(client) {
  const canceled = await evaluate(client, `(() => {
    const button = [...document.querySelectorAll('.source-dialog button')].find((item) => item.textContent?.trim() === '取消');
    if (!button) return false;
    button.click();
    return true;
  })()`);
  assert.equal(canceled, true, "添加来源对话框缺少取消按钮");
  await waitFor(() => evaluate(client, "!document.querySelector('.source-dialog')"), "source dialog closed");
}

async function setDialogDraft(client, label, rootPath) {
  const values = await evaluate(client, `(() => {
    const inputs = [...document.querySelectorAll('.source-dialog input')];
    if (inputs.length < 2) return null;
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
    setter.call(inputs[0], ${JSON.stringify(label)});
    inputs[0].dispatchEvent(new Event('input', { bubbles: true }));
    inputs[0].dispatchEvent(new Event('change', { bubbles: true }));
    setter.call(inputs[1], ${JSON.stringify(rootPath)});
    inputs[1].dispatchEvent(new Event('input', { bubbles: true }));
    inputs[1].dispatchEvent(new Event('change', { bubbles: true }));
    return inputs.map((input) => input.value);
  })()`);
  assert.deepEqual(values, [label, rootPath], "添加来源表单未接受名称或路径");
}

async function submitAddDialog(client) {
  const submitted = await evaluate(client, `(() => {
    const button = [...document.querySelectorAll('.source-dialog button')].find((item) => item.textContent?.includes('添加并增量索引'));
    if (!button || button.disabled) return false;
    button.click();
    return true;
  })()`);
  assert.equal(submitted, true, "未找到可用的添加并增量索引按钮");
}

async function dispatchFolderDrop(client, folder) {
  const rect = await evaluate(client, `(() => {
    const element = document.querySelector('.source-drop-action');
    if (!element) return null;
    const box = element.getBoundingClientRect();
    return { x: box.left + box.width / 2, y: box.top + box.height / 2 };
  })()`);
  assert.ok(rect, "未找到资料源拖入区域");
  const dragData = {
    items: [{ mimeType: "text/uri-list", data: `file:///${folder.replaceAll("\\", "/")}` }],
    files: [folder],
    dragOperationsMask: 1,
  };
  await client.send("Input.dispatchDragEvent", { type: "dragEnter", x: rect.x, y: rect.y, data: dragData });
  await waitFor(
    () => evaluate(client, "document.querySelector('.source-drop-action')?.classList.contains('is-dragging')"),
    "folder drag visual feedback",
    5_000,
  );
  await client.send("Input.dispatchDragEvent", { type: "dragOver", x: rect.x, y: rect.y, data: dragData });
  await client.send("Input.dispatchDragEvent", { type: "drop", x: rect.x, y: rect.y, data: dragData });
  return waitFor(
    () => evaluate(client, "document.querySelector('.source-dialog .path-input-row input')?.value || null"),
    "dropped folder path",
    10_000,
  );
}

async function clickSourceControl(client, sourceLabel, action) {
  const selector = action === "delete" ? ".source-delete-button" : "[role=\"switch\"]";
  const clicked = await evaluate(client, `(() => {
    const row = [...document.querySelectorAll('.source-row')].find((item) => item.innerText.includes(${JSON.stringify(sourceLabel)}));
    const control = row?.querySelector(${JSON.stringify(selector)});
    if (!control || control.disabled) return false;
    control.click();
    return true;
  })()`);
  assert.equal(clicked, true, `未找到 ${sourceLabel} 的${action === "delete" ? "删除" : "启停"}控件`);
}

async function confirmSourceDeletion(client) {
  await waitFor(() => evaluate(client, "Boolean(document.querySelector('.confirm-dialog'))"), "source delete confirmation");
  const confirmed = await evaluate(client, `(() => {
    const button = document.querySelector('.confirm-dialog .danger-confirm');
    if (!button || button.disabled) return false;
    button.click();
    return true;
  })()`);
  assert.equal(confirmed, true, "未找到来源删除确认按钮");
}

function plain(value) {
  return JSON.parse(JSON.stringify(value));
}

async function inspectSourceCache(sourceId) {
  const { KnowledgeBaseService } = await import("../dist/core/service.js");
  const knowledge = await KnowledgeBaseService.create({ configDir, dataDir });
  try {
    const db = knowledge.database.db;
    const documents = plain(db.prepare(`
      SELECT id, source_identity, absolute_path, content_hash, scan_generation, stale, deleted, chunk_count
      FROM documents WHERE source_id = ? ORDER BY id
    `).all(sourceId));
    const chunks = plain(db.prepare(`
      SELECT c.id, c.content_hash, c.ordinal
      FROM chunks c JOIN documents d ON d.id = c.document_id
      WHERE d.source_id = ? ORDER BY c.id
    `).all(sourceId));
    const embeddings = Number(db.prepare(`
      SELECT COUNT(*) AS count FROM document_embeddings e
      JOIN documents d ON d.id = e.document_id WHERE d.source_id = ?
    `).get(sourceId)?.count ?? 0);
    const issues = plain(db.prepare(`
      SELECT run_id, path, code, message, occurred_at
      FROM index_issues WHERE source_id = ? ORDER BY id
    `).all(sourceId));
    const sourceState = plain(db.prepare(`
      SELECT source_id, source_identity, ready, last_run_id, updated_at
      FROM source_index_state WHERE source_id = ?
    `).get(sourceId) ?? null);
    const terms = knowledge.database.fts5Available
      ? Number(db.prepare(`
          SELECT COUNT(*) AS count FROM chunks_terms t
          JOIN chunks c ON c.rowid = t.rowid
          JOIN documents d ON d.id = c.document_id WHERE d.source_id = ?
        `).get(sourceId)?.count ?? 0)
      : null;
    const trigrams = knowledge.database.trigramAvailable
      ? Number(db.prepare(`
          SELECT COUNT(*) AS count FROM chunks_trigram t
          JOIN chunks c ON c.rowid = t.rowid
          JOIN documents d ON d.id = c.document_id WHERE d.source_id = ?
        `).get(sourceId)?.count ?? 0)
      : null;
    return { documents, chunks, embeddings, issues, sourceState, fts: { available: knowledge.database.fts5Available, terms, trigrams } };
  } finally {
    knowledge.close();
  }
}

async function readCitation(citationId, expectedRevision) {
  const { KnowledgeBaseService } = await import("../dist/core/service.js");
  const knowledge = await KnowledgeBaseService.create({ configDir, dataDir });
  try {
    return knowledge.readCitation(citationId, expectedRevision);
  } finally {
    knowledge.close();
  }
}

async function expectCitationRejected(citationId) {
  try {
    await readCitation(citationId);
    throw new Error("停用来源 citation 仍可回读");
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    assert.match(message, /资料源已禁用/, "停用来源 citation 应以来源禁用原因拒绝");
    return message;
  }
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
  if (!child || child.exitCode !== null) return;
  await new Promise((resolve) => {
    const timer = setTimeout(resolve, timeoutMs);
    child.once("exit", () => {
      clearTimeout(timer);
      resolve();
    });
  });
}

async function stopElectron(child, client) {
  client?.close();
  if (!child) return { launched: false, rootPidAbsent: true, exactTreeKill: false };
  const pid = child.pid;
  let taskkillStdout = "";
  let taskkillStderr = "";
  let taskkillExit = "not-needed";
  if (pidExists(pid)) {
    try {
      const result = await execFileAsync("taskkill.exe", ["/PID", String(pid), "/T", "/F"], { encoding: "utf8", windowsHide: true });
      taskkillStdout = result.stdout ?? "";
      taskkillStderr = result.stderr ?? "";
      taskkillExit = "success";
    } catch (error) {
      taskkillStdout = error.stdout ?? "";
      taskkillStderr = error.stderr ?? String(error);
      taskkillExit = "error";
    }
  }
  await waitForChildExit(child);
  await waitFor(() => !pidExists(pid), `settings exact PID ${pid} exit`, 10_000);
  if (taskkillExit === "error") throw new Error(`settings taskkill /T 失败：${taskkillStderr || taskkillStdout}`);
  return {
    launched: true,
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
  mkdir(profileDir, { recursive: true }),
  mkdir(isolatedAppData, { recursive: true }),
  mkdir(isolatedLocalAppData, { recursive: true }),
  mkdir(primarySourceDir, { recursive: true }),
  mkdir(controlSourceDir, { recursive: true }),
  mkdir(partialSourceDir, { recursive: true }),
]);
await writeFile(configPath, `${JSON.stringify(initialConfig, null, 2)}\n`, "utf8");
await writeFile(primarySourceFile, `# ${primaryMarker}\n\n首次拖入内容。\n`, "utf8");
await writeFile(controlSourceFile, `key,value\nmarker,${controlMarker}\n`, "utf8");
await writeFile(partialGoodFile, `# ${partialMarker}\n\n这个文件必须成功进入索引。\n`, "utf8");
await writeFile(partialBrokenFile, "this-is-not-a-valid-docx-zip", "utf8");

let artifact = null;
let child = null;
let client = null;
let stdout = "";
let stderr = "";
let report = null;
let cleanup = null;

try {
  artifact = await verifyCurrentElectronArtifact(projectRoot, executable);
  const port = await freePort();
  child = spawn(executable, [
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
  child.stdout.setEncoding("utf8");
  child.stderr.setEncoding("utf8");
  child.stdout.on("data", (chunk) => { stdout += chunk; });
  child.stderr.on("data", (chunk) => { stderr += chunk; });

  const page = await waitFor(async () => {
    const response = await fetch(`http://127.0.0.1:${port}/json/list`);
    if (!response.ok) return null;
    const pages = await response.json();
    return pages.find((item) => item.type === "page" && item.webSocketDebuggerUrl) ?? null;
  }, "Electron DevTools page", 45_000);

  client = new CdpClient(page.webSocketDebuggerUrl);
  await client.open();
  await Promise.all([client.send("Runtime.enable"), client.send("Page.enable")]);
  await waitFor(() => evaluate(client, "document.readyState === 'complete'"), "drag document ready", 15_000);
  await waitFor(() => evaluate(client, "Boolean(document.querySelector('.app-shell'))"), "drag application shell", 60_000);

  const navigated = await evaluate(client, `(() => {
    const button = [...document.querySelectorAll('button')].find((item) => item.textContent?.includes('资料位置与索引'));
    if (!button) return false;
    button.click();
    return true;
  })()`);
  assert.equal(navigated, true, "未找到资料位置与索引导航按钮");
  await waitFor(() => evaluate(client, "Boolean(document.querySelector('.source-drop-action'))"), "settings source drop target");
  const settingsBeforeAdd = await evaluate(client, "document.body.innerText");
  assert.ok(settingsBeforeAdd.includes("尚未配置") && settingsBeforeAdd.includes("添加第一个资料源"), "无来源初始态缺少添加引导");

  await openAddDialog(client);
  const browseIpcStructureBase = await evaluate(client, `(() => {
    const dialog = document.querySelector('.source-dialog');
    const browse = [...(dialog?.querySelectorAll('button') ?? [])].find((item) => item.textContent?.includes('浏览文件夹'));
    const kindButtons = [...(dialog?.querySelectorAll('.source-kind-options button') ?? [])];
    const tableButton = kindButtons.find((item) => item.textContent?.includes('配置表'));
    const designButton = kindButtons.find((item) => item.textContent?.includes('策划案'));
    return {
      chooseDirectoryExposed: typeof window.drag?.chooseDirectory === 'function',
      resolveDroppedPathExposed: typeof window.drag?.resolveDroppedPath === 'function',
      browseButtonPresent: Boolean(browse),
      browseButtonEnabled: Boolean(browse && !browse.disabled),
      designAndTablePresent: Boolean(designButton && tableButton),
      pathInputPresent: Boolean(dialog?.querySelector('.path-input-row input')),
      folderDropzonePresent: Boolean(dialog?.querySelector('.folder-dropzone')),
    };
  })()`);
  await evaluate(client, `(() => {
    const button = [...document.querySelectorAll('.source-kind-options button')].find((item) => item.textContent?.includes('配置表'));
    button?.click();
  })()`);
  await waitFor(
    () => evaluate(client, "[...document.querySelectorAll('.source-kind-options button')].find((item) => item.textContent?.includes('配置表'))?.getAttribute('aria-pressed') === 'true'"),
    "table source kind selected",
  );
  await evaluate(client, `(() => {
    const button = [...document.querySelectorAll('.source-kind-options button')].find((item) => item.textContent?.includes('策划案'));
    button?.click();
  })()`);
  await waitFor(
    () => evaluate(client, "[...document.querySelectorAll('.source-kind-options button')].find((item) => item.textContent?.includes('策划案'))?.getAttribute('aria-pressed') === 'true'"),
    "design source kind restored",
  );
  const browseIpcStructure = { ...browseIpcStructureBase, tableSelectionAccepted: true, designRestored: true };
  assert.deepEqual(browseIpcStructure, {
    chooseDirectoryExposed: true,
    resolveDroppedPathExposed: true,
    browseButtonPresent: true,
    browseButtonEnabled: true,
    designAndTablePresent: true,
    tableSelectionAccepted: true,
    designRestored: true,
    pathInputPresent: true,
    folderDropzonePresent: true,
  });

  let nativeBrowseScreenshot = null;
  if (exerciseNativeBrowse) {
    const browseClicked = await evaluate(client, `(() => {
      const button = [...document.querySelectorAll('.source-dialog button')].find((item) => item.textContent?.includes('浏览文件夹'));
      if (!button || button.disabled) return false;
      button.click();
      return true;
    })()`);
    assert.equal(browseClicked, true, "未找到浏览文件夹按钮");
    process.stdout.write(`GUI_ACCEPTANCE_NATIVE_BROWSE_PENDING ${JSON.stringify({ pid: child.pid, primarySourceDir, acceptanceRoot })}\n`);
    const browsedPath = await waitFor(
      () => evaluate(client, "document.querySelector('.source-dialog .path-input-row input')?.value || null"),
      "native directory browse result",
      900_000,
    );
    assert.equal(path.resolve(browsedPath), path.resolve(primarySourceDir), "原生目录浏览路径不匹配");
    nativeBrowseScreenshot = await screenshot(client, "after-native-directory-browse.png");
  }
  await closeAddDialog(client);

  const droppedPath = await dispatchFolderDrop(client, primarySourceDir);
  assert.equal(path.resolve(droppedPath), path.resolve(primarySourceDir), "真实拖入链路返回路径不匹配");
  await setDialogDraft(client, "Electron拖入验收来源", primarySourceDir);
  await submitAddDialog(client);
  const configAfterPrimary = await waitFor(async () => {
    const config = await readConfig();
    return config.sources.find((source) => source.label === "Electron拖入验收来源") ? config : null;
  }, "primary source persisted", 45_000);
  const primarySource = configAfterPrimary.sources.find((source) => source.label === "Electron拖入验收来源");
  assert.ok(primarySource, "真实拖入来源未持久化");
  await waitFor(() => evaluate(client, "document.body.innerText.includes('来源已保存并完成增量索引')"), "primary incremental notice", 30_000);
  const afterAddScreenshot = await screenshot(client, "after-folder-drop.png");

  const runBeforeWatcher = (await snapshot(client)).index.lastRun?.runId;
  await appendFile(primarySourceFile, `\n文件监听增量内容：${watcherMarker}。\n`, "utf8");
  const watcherSnapshot = await waitFor(async () => {
    const value = await snapshot(client);
    return value.index.activeRun === null
      && value.index.lastRun?.runId
      && value.index.lastRun.runId !== runBeforeWatcher
      && value.index.lastRun.indexed >= 1
      ? value : null;
  }, "watcher scoped incremental", 45_000);
  await waitFor(() => evaluate(client, "document.body.innerText.includes('索引已自动更新')"), "watcher incremental notice", 15_000);
  const watcherSearch = await search(client, watcherMarker, [primarySource.id]);
  const watcherCitationId = watcherSearch.hits?.[0]?.excerpts?.[0]?.citation?.citationId;
  assert.ok(watcherCitationId, "watcher 更新后未检索到 citation");
  const watcherCitation = await readCitation(watcherCitationId, watcherSearch.indexRevision);
  assert.ok(watcherCitation.content.includes(watcherMarker) && !watcherCitation.changed, "watcher citation 未命中新内容或已变化");
  const afterWatcherScreenshot = await screenshot(client, "after-watcher-update.png");

  const controlSource = {
    id: "electron-control-source",
    label: "Electron控制来源",
    kind: "table",
    rootPath: controlSourceDir,
    enabled: true,
    includeExtensions: [".csv"],
    excludeDirectoryNames: [...primarySource.excludeDirectoryNames],
    maxFileBytes: primarySource.maxFileBytes,
  };
  const configWithControl = structuredClone(await readConfig());
  configWithControl.sources.push(controlSource);
  await evaluate(client, `window.drag.saveConfig(${JSON.stringify(configWithControl)})`);
  await waitFor(async () => (await readConfig()).sources.some((source) => source.id === controlSource.id), "control source persisted", 45_000);
  const controlSearch = await search(client, controlMarker, [controlSource.id]);
  assert.ok(controlSearch.hits?.length > 0, "控制来源未进入索引");

  const primaryCacheBeforeDisable = await inspectSourceCache(primarySource.id);
  const controlCacheBeforeDisable = await inspectSourceCache(controlSource.id);
  const revisionBeforeDisable = (await snapshot(client)).index.indexRevision;
  await clickSourceControl(client, "Electron拖入验收来源", "toggle");
  await waitFor(async () => (await readConfig()).sources.find((source) => source.id === primarySource.id)?.enabled === false, "primary disabled persistence");
  await waitFor(() => evaluate(client, "document.body.innerText.includes('来源已停用')"), "source disabled notice");
  const primaryCacheAfterDisable = await inspectSourceCache(primarySource.id);
  const controlCacheAfterDisable = await inspectSourceCache(controlSource.id);
  assert.deepEqual(primaryCacheAfterDisable, primaryCacheBeforeDisable, "停用来源改写或清除了其本地缓存");
  assert.deepEqual(controlCacheAfterDisable, controlCacheBeforeDisable, "停用主来源影响了控制来源缓存");
  const revisionAfterDisable = (await snapshot(client)).index.indexRevision;
  assert.equal(revisionAfterDisable, revisionBeforeDisable, "停用来源不应改变索引 revision");
  const disabledPrimarySearch = await search(client, watcherMarker, [primarySource.id]);
  const enabledControlSearch = await search(client, controlMarker, [controlSource.id]);
  assert.equal(disabledPrimarySearch.hits.length, 0, "停用来源仍被搜索命中");
  assert.ok(enabledControlSearch.hits.length > 0, "停用主来源错误屏蔽了控制来源");
  const disabledCitationError = await expectCitationRejected(watcherCitationId);
  const disabledScreenshot = await screenshot(client, "source-disabled-cache-retained.png");

  const beforeReenable = await snapshot(client);
  const controlCacheBeforeReenable = await inspectSourceCache(controlSource.id);
  await clickSourceControl(client, "Electron拖入验收来源", "toggle");
  const reenabledSnapshot = await waitFor(async () => {
    const config = await readConfig();
    const value = await snapshot(client);
    return config.sources.find((source) => source.id === primarySource.id)?.enabled === true
      && value.index.activeRun === null
      && value.index.lastRun?.runId
      && value.index.lastRun.runId !== beforeReenable.index.lastRun?.runId
      ? value : null;
  }, "source re-enable scoped incremental", 45_000);
  const reenableRun = reenabledSnapshot.index.lastRun;
  assert.equal(reenableRun.discovered, primaryCacheBeforeDisable.documents.length, "重新启用不应扫描其他来源");
  assert.equal(reenableRun.indexed, 0, "未修改来源重新启用应复用 last-good 缓存");
  assert.equal(reenableRun.unchanged, primaryCacheBeforeDisable.documents.length, "重新启用应确认主来源缓存未变化");
  assert.equal(reenableRun.failed, 0, "重新启用主来源不应失败");
  const controlCacheAfterReenable = await inspectSourceCache(controlSource.id);
  assert.deepEqual(controlCacheAfterReenable, controlCacheBeforeReenable, "scoped 增量不应触碰控制来源缓存");
  const restoredSearch = await search(client, watcherMarker, [primarySource.id]);
  assert.ok(restoredSearch.hits.length > 0, "重新启用后未恢复主来源检索");
  const restoredCitation = await readCitation(watcherCitationId);
  assert.equal(restoredCitation.citation.sourceId, primarySource.id, "重新启用后旧 citation 未恢复");
  const reenabledScreenshot = await screenshot(client, "source-reenabled-scoped-incremental.png");

  await openAddDialog(client);
  await setDialogDraft(client, "部分失败验收来源", partialSourceDir);
  await submitAddDialog(client);
  const configAfterPartial = await waitFor(async () => {
    const config = await readConfig();
    return config.sources.find((source) => source.label === "部分失败验收来源") ? config : null;
  }, "partial source persisted", 60_000);
  const partialSource = configAfterPartial.sources.find((source) => source.label === "部分失败验收来源");
  assert.ok(partialSource, "部分失败来源未持久化");
  const partialSnapshot = await waitFor(async () => {
    const value = await snapshot(client);
    return value.index.activeRun === null && value.index.lastRun?.failed > 0 && value.index.lastRun?.indexed > 0 ? value : null;
  }, "partial index completion", 60_000);
  await waitFor(
    () => evaluate(client, "Boolean(document.querySelector('.notice-toast.is-warning')) && document.body.innerText.includes('来源已保存，但增量索引未完整完成')"),
    "partial warning notice",
    20_000,
  );
  const partialSearch = await search(client, partialMarker, [partialSource.id]);
  assert.ok(partialSearch.hits.length > 0, "partial 运行未保留成功文档的检索结果");
  const partialCacheBeforeDelete = await inspectSourceCache(partialSource.id);
  assert.ok(partialCacheBeforeDelete.documents.length > 0 && partialCacheBeforeDelete.issues.length > 0, "partial 运行缺少成功文档或失败 issue");
  const partialWarningScreenshot = await screenshot(client, "partial-index-warning.png");

  const primaryCacheBeforeDelete = await inspectSourceCache(primarySource.id);
  const controlCacheBeforeDelete = await inspectSourceCache(controlSource.id);
  const primaryFileBeforeDelete = await describeFile(primarySourceFile);
  const primarySearchBeforeDelete = await search(client, watcherMarker, [primarySource.id]);
  assert.ok(primarySearchBeforeDelete.hits.length > 0, "删除前主来源不可检索，前置条件失效");
  await clickSourceControl(client, "Electron拖入验收来源", "delete");
  await confirmSourceDeletion(client);
  await waitFor(async () => !(await readConfig()).sources.some((source) => source.id === primarySource.id), "primary source deleted from config", 45_000);
  await waitFor(() => evaluate(client, "document.body.innerText.includes('来源及对应索引已删除')"), "source delete notice", 20_000);
  const primaryCacheAfterDelete = await inspectSourceCache(primarySource.id);
  const controlCacheAfterDelete = await inspectSourceCache(controlSource.id);
  const partialCacheAfterDelete = await inspectSourceCache(partialSource.id);
  assert.ok(primaryCacheBeforeDelete.documents.length > 0 && primaryCacheBeforeDelete.chunks.length > 0, "删除前主来源缓存为空");
  assert.deepEqual(primaryCacheAfterDelete, {
    documents: [],
    chunks: [],
    embeddings: 0,
    issues: [],
    sourceState: null,
    fts: {
      available: primaryCacheBeforeDelete.fts.available,
      terms: primaryCacheBeforeDelete.fts.available ? 0 : null,
      trigrams: primaryCacheBeforeDelete.fts.trigrams === null ? null : 0,
    },
  }, "删除来源未完整清理对应缓存");
  assert.deepEqual(controlCacheAfterDelete, controlCacheBeforeDelete, "删除主来源误清控制来源缓存");
  assert.deepEqual(partialCacheAfterDelete, partialCacheBeforeDelete, "删除主来源误清 partial 来源缓存或 issue");
  await access(primarySourceFile);
  const primaryFileAfterDelete = await describeFile(primarySourceFile);
  assert.deepEqual(primaryFileAfterDelete, primaryFileBeforeDelete, "删除来源时修改了只读源文件");
  const primarySearchAfterDelete = await search(client, watcherMarker, [primarySource.id]);
  const controlSearchAfterDelete = await search(client, controlMarker, [controlSource.id]);
  const partialSearchAfterDelete = await search(client, partialMarker, [partialSource.id]);
  assert.equal(primarySearchAfterDelete.hits.length, 0, "删除来源后仍可检索主来源");
  assert.ok(controlSearchAfterDelete.hits.length > 0 && partialSearchAfterDelete.hits.length > 0, "删除主来源影响其他来源检索");
  const sourceDeletedScreenshot = await screenshot(client, "source-deleted-scoped-cache-purge.png");

  const finalConfig = await readConfig();
  report = {
    status: "PASS",
    acceptance: "electron-source-lifecycle",
    executable,
    artifact,
    pid: child.pid,
    acceptanceRoot,
    isolatedState: { configDir, dataDir, profileDir, appData: isolatedAppData, localAppData: isolatedLocalAppData },
    fixtures: { primarySourceDir, primarySourceFile, controlSourceDir, controlSourceFile, partialSourceDir, partialGoodFile, partialBrokenFile },
    rendererTitle: page.title,
    gates: {
      initialEmptyState: "PASS",
      browseIpcStructure: "PASS",
      nativeDirectoryBrowse: exerciseNativeBrowse ? "PASS" : "NOT_TESTED",
      realCdpDragChain: "PASS",
      sourceAddIncremental: "PASS",
      watcherIncrementalAndNotice: "PASS",
      disableRetainsCache: "PASS",
      disableBlocksSearchAndCitation: "PASS",
      reenableScopedIncremental: "PASS",
      partialIndexWarning: "PASS",
      deletePurgesOnlyTargetCache: "PASS",
      sourceFilesRemainReadOnly: "PASS",
      globalCacheClear: "NOT_TESTED",
      pauseResume: "NOT_TESTED",
    },
    browseIpcStructure,
    nativeBrowseScreenshot,
    droppedPath,
    primarySource,
    controlSource,
    partialSource,
    watcher: { indexStatus: watcherSnapshot.index, searchHit: watcherSearch.hits[0], citation: watcherCitation, noticeVisible: true },
    disable: {
      revisionBeforeDisable,
      revisionAfterDisable,
      cacheBefore: primaryCacheBeforeDisable,
      cacheAfter: primaryCacheAfterDisable,
      primarySearchHits: disabledPrimarySearch.hits.length,
      controlSearchHits: enabledControlSearch.hits.length,
      citationError: disabledCitationError,
    },
    reenable: { indexRun: reenableRun, primarySearchHits: restoredSearch.hits.length, controlCacheUnaffected: true },
    partial: { indexRun: partialSnapshot.index.lastRun, cache: partialCacheBeforeDelete, searchHit: partialSearch.hits[0], warningVisible: true },
    deletion: {
      cacheBefore: primaryCacheBeforeDelete,
      cacheAfter: primaryCacheAfterDelete,
      controlCacheUnaffected: true,
      partialCacheUnaffected: true,
      primarySearchHits: primarySearchAfterDelete.hits.length,
      controlSearchHits: controlSearchAfterDelete.hits.length,
      partialSearchHits: partialSearchAfterDelete.hits.length,
      sourceFileBefore: primaryFileBeforeDelete,
      sourceFileAfter: primaryFileAfterDelete,
      sourceFileExistsAndUnchanged: true,
      finalSourceIds: finalConfig.sources.map((source) => source.id),
    },
    screenshots: {
      afterAdd: afterAddScreenshot,
      watcher: afterWatcherScreenshot,
      disabled: disabledScreenshot,
      reenabled: reenabledScreenshot,
      partialWarning: partialWarningScreenshot,
      sourceDeleted: sourceDeletedScreenshot,
    },
    stdout,
    stderr,
  };
} catch (error) {
  const message = error instanceof Error ? error.stack ?? error.message : String(error);
  const staleArtifact = message.includes("拒绝用 stale EXE 验收");
  report = {
    status: staleArtifact ? "BLOCKED" : "FAIL",
    acceptance: "electron-source-lifecycle",
    executable,
    artifact,
    pid: child?.pid ?? null,
    acceptanceRoot,
    error: message,
    blockedReason: staleArtifact ? "release/win-unpacked/drag-gui.exe 与当前 dist 不一致，未启动 GUI" : null,
    stdout,
    stderr,
  };
  process.exitCode = 1;
} finally {
  let cleanupError = null;
  try {
    cleanup = await stopElectron(child, client);
  } catch (error) {
    cleanupError = error instanceof Error ? error.stack ?? error.message : String(error);
    process.exitCode = 1;
  }
  report ??= { status: "FAIL", acceptance: "electron-source-lifecycle", executable, acceptanceRoot, error: "验收未生成报告" };
  if (cleanupError) {
    report.status = "FAIL";
    report.cleanupError = cleanupError;
  }
  report.cleanup = cleanup;
  report.cleanupComplete = Boolean(cleanup?.rootPidAbsent);
  report.stdout = stdout;
  report.stderr = stderr;
  await writeFile(reportPath, `${JSON.stringify(report, null, 2)}\n`, "utf8");
  if (report.status === "PASS") process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
  else process.stderr.write(`${JSON.stringify(report, null, 2)}\n`);
}
