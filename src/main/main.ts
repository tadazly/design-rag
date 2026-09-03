import { app, BrowserWindow, dialog, ipcMain, session, shell } from "electron";
import { fileURLToPath } from "node:url";
import { stat } from "node:fs/promises";
import path from "node:path";
import { z } from "zod/v4";
import { APP_NAME, type AppEvent, type CodexPreferences, type SearchRequest } from "../shared/contracts.js";
import { appConfigSchema } from "../core/config.js";
import { isPathInside } from "../core/paths.js";
import { KnowledgeBaseService } from "../core/service.js";
import { ChatController } from "./chat-controller.js";
import { openSourceLocation } from "./source-opener.js";

const dirname = path.dirname(fileURLToPath(import.meta.url));

const searchSchema = z.object({
  query: z.string().min(1).max(2_000),
  sourceIds: z.array(z.string()).optional(),
  sourceKinds: z.array(z.enum(["design", "table"])).optional(),
  sectionTypes: z.array(z.string()).optional(),
  extensions: z.array(z.string()).optional(),
  updatedAfter: z.string().optional(),
  updatedBefore: z.string().optional(),
  retrievalMode: z.enum(["lexical", "semantic", "hybrid", "auto"]).optional(),
  sort: z.enum(["newest", "relevance", "hybrid"]).optional(),
  latestPerFamily: z.boolean().optional(),
  limit: z.number().int().min(1).max(100).optional(),
});

let mainWindow: BrowserWindow | null = null;
let knowledge: KnowledgeBaseService | null = null;
let controller: ChatController | null = null;

function requireController(): ChatController {
  if (!controller) throw new Error("应用尚未初始化");
  return controller;
}

function sendEvent(event: AppEvent): void {
  if (mainWindow && !mainWindow.isDestroyed()) mainWindow.webContents.send("drag:event", event);
}

async function chooseDirectory(): Promise<string | null> {
  const options = {
    title: "选择资料目录",
    properties: ["openDirectory", "createDirectory"] as Array<"openDirectory" | "createDirectory">,
  };
  const result = mainWindow
    ? await dialog.showOpenDialog(mainWindow, options)
    : await dialog.showOpenDialog(options);
  return result.canceled ? null : result.filePaths[0] ?? null;
}

async function validateSourceDirectory(value: unknown): Promise<string | null> {
  const candidate = path.resolve(z.string().min(1).max(32_768).parse(value));
  const info = await stat(candidate);
  if (!info.isDirectory()) throw new Error("拖入的项目不是文件夹");
  return candidate;
}

function registerIpc(): void {
  ipcMain.handle("drag:get-snapshot", () => requireController().snapshot());
  ipcMain.handle("drag:set-active-view", (_event, view: unknown) => {
    const parsed = z.enum(["chat", "settings"]).parse(view);
    requireController().setActiveView(parsed);
  });
  ipcMain.handle("drag:save-config", async (_event, value: unknown) => {
    const config = appConfigSchema.parse(value);
    return requireController().reconcileConfig(config);
  });
  ipcMain.handle("drag:choose-source-directory", async (_event, sourceId: unknown) => {
    z.string().min(1).parse(sourceId);
    return chooseDirectory();
  });
  ipcMain.handle("drag:choose-directory", () => chooseDirectory());
  ipcMain.handle("drag:validate-source-directory", (_event, value: unknown) => validateSourceDirectory(value));
  ipcMain.handle("drag:rebuild-index", async (_event, full: unknown) => {
    const parsed = z.boolean().parse(full);
    return requireController().runIndex(parsed);
  });
  ipcMain.handle("drag:pause-index", () => requireController().knowledge.pauseIndex());
  ipcMain.handle("drag:resume-index", () => requireController().knowledge.resumeIndex());
  ipcMain.handle("drag:clear-index-cache", async () => {
    const options = {
      type: "warning" as const,
      title: "删除本地检索缓存",
      message: "确定删除本地检索缓存吗？",
      detail: "这不会删除策划案、配表、会话或设置。删除后需要重新建立索引才能检索文档。",
      buttons: ["删除缓存", "取消"],
      defaultId: 1,
      cancelId: 1,
      noLink: true,
    };
    const result = mainWindow
      ? await dialog.showMessageBox(mainWindow, options)
      : await dialog.showMessageBox(options);
    if (result.response !== 0) return requireController().knowledge.status();
    return requireController().clearIndexCache();
  });
  ipcMain.handle("drag:search", async (_event, request: unknown) => {
    const parsed = searchSchema.parse(request) as SearchRequest;
    return requireController().search(parsed);
  });
  ipcMain.handle("drag:create-thread", () => requireController().createThread());
  ipcMain.handle("drag:select-thread", (_event, id: unknown) => requireController().selectThread(z.string().min(1).parse(id)));
  ipcMain.handle("drag:archive-thread", (_event, id: unknown) => requireController().archiveThread(z.string().min(1).parse(id)));
  ipcMain.handle("drag:restore-thread", (_event, id: unknown) => requireController().restoreThread(z.string().min(1).parse(id)));
  ipcMain.handle("drag:delete-thread", (_event, id: unknown) => requireController().deleteThread(z.string().min(1).parse(id)));
  ipcMain.handle("drag:set-codex-preferences", (_event, value: unknown) => {
    const parsed = z.object({
      model: z.string().min(1).max(200).nullable(),
      reasoningEffort: z.string().min(1).max(50).nullable(),
    }).parse(value) as CodexPreferences;
    return requireController().setCodexPreferences(parsed);
  });
  ipcMain.handle("drag:send-message", (_event, value: unknown) => {
    const parsed = z.object({
      text: z.string().min(1).max(20_000),
      citationIds: z.array(z.string().min(4)).max(20).default([]),
    }).parse(value);
    return requireController().sendMessage(parsed.text, parsed.citationIds);
  });
  ipcMain.handle("drag:stop-turn", () => requireController().stopTurn());
  ipcMain.handle("drag:login-chatgpt", async () => {
    const result = await requireController().loginWithChatGPT();
    if (result.authUrl) {
      const url = new URL(result.authUrl);
      if (url.protocol !== "https:") throw new Error("登录地址不是 HTTPS");
      await shell.openExternal(url.toString());
    }
    return result;
  });
  ipcMain.handle("drag:open-citation", async (_event, citationId: unknown) => {
    const parsed = z.string().min(4).parse(citationId);
    const result = requireController().knowledge.readCitation(parsed);
    const source = requireController().knowledge.config.sources.find((item) => item.id === result.citation.sourceId);
    if (!source || !isPathInside(source.rootPath, result.citation.absolutePath)) throw new Error("引用路径不在已授权资料目录内");
    return openSourceLocation(result.citation.absolutePath, result.citation.locator);
  });
}

async function createWindow(): Promise<void> {
  mainWindow = new BrowserWindow({
    width: 1600,
    height: 1000,
    minWidth: 720,
    minHeight: 600,
    show: false,
    backgroundColor: "#ffffff",
    autoHideMenuBar: true,
    title: APP_NAME,
    webPreferences: {
      preload: path.join(dirname, "preload.cjs"),
      nodeIntegration: false,
      contextIsolation: true,
      sandbox: true,
      webSecurity: true,
      devTools: !app.isPackaged,
    },
  });
  mainWindow.webContents.setWindowOpenHandler(({ url }) => {
    try {
      const parsed = new URL(url);
      if (parsed.protocol === "https:" || parsed.protocol === "http:") void shell.openExternal(parsed.toString());
    } catch {
      // Invalid URLs remain blocked.
    }
    return { action: "deny" };
  });
  mainWindow.webContents.on("will-navigate", (event, url) => {
    const current = mainWindow?.webContents.getURL();
    if (current && url !== current) event.preventDefault();
  });
  mainWindow.once("ready-to-show", () => mainWindow?.show());
  mainWindow.on("closed", () => { mainWindow = null; });
  const devUrl = process.env.DESIGN_RAG_VITE_DEV_SERVER_URL;
  if (devUrl) await mainWindow.loadURL(devUrl);
  else await mainWindow.loadFile(path.join(dirname, "../renderer/index.html"));
}

app.setName("drag-gui");
app.whenReady().then(async () => {
  session.defaultSession.setPermissionRequestHandler((_webContents, _permission, callback) => callback(false));
  knowledge = await KnowledgeBaseService.create();
  controller = new ChatController(knowledge);
  controller.on("event", sendEvent);
  await controller.initialize();
  registerIpc();
  await createWindow();
  app.on("activate", () => {
    if (BrowserWindow.getAllWindows().length === 0) void createWindow();
  });
}).catch((error) => {
  dialog.showErrorBox("DRAG 启动失败", error instanceof Error ? error.stack ?? error.message : String(error));
  app.quit();
});

app.on("window-all-closed", () => {
  if (process.platform !== "darwin") app.quit();
});

app.on("before-quit", (event) => {
  if (!controller) return;
  event.preventDefault();
  const currentController = controller;
  controller = null;
  currentController.dispose();
  void currentController.appServer.stop().finally(() => {
    knowledge?.close();
    knowledge = null;
    app.quit();
  });
});
