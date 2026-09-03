const { contextBridge, ipcRenderer, webUtils } = require("electron") as typeof import("electron");
import type { AppEvent, AppConfig, SearchRequest, DragDesktopApi } from "../shared/contracts.js";

const api: DragDesktopApi = {
  getSnapshot: () => ipcRenderer.invoke("drag:get-snapshot"),
  setActiveView: (view) => ipcRenderer.invoke("drag:set-active-view", view),
  saveConfig: (config: AppConfig) => ipcRenderer.invoke("drag:save-config", config),
  chooseSourceDirectory: (sourceId: string) => ipcRenderer.invoke("drag:choose-source-directory", sourceId),
  chooseDirectory: () => ipcRenderer.invoke("drag:choose-directory"),
  resolveDroppedPath: (file: unknown) => {
    const resolved = webUtils.getPathForFile(file as Parameters<typeof webUtils.getPathForFile>[0]);
    return ipcRenderer.invoke("drag:validate-source-directory", resolved);
  },
  rebuildIndex: (full = false) => ipcRenderer.invoke("drag:rebuild-index", full),
  pauseIndex: () => ipcRenderer.invoke("drag:pause-index"),
  resumeIndex: () => ipcRenderer.invoke("drag:resume-index"),
  clearIndexCache: () => ipcRenderer.invoke("drag:clear-index-cache"),
  search: (request: SearchRequest) => ipcRenderer.invoke("drag:search", request),
  createThread: () => ipcRenderer.invoke("drag:create-thread"),
  selectThread: (threadId: string) => ipcRenderer.invoke("drag:select-thread", threadId),
  archiveThread: (threadId: string) => ipcRenderer.invoke("drag:archive-thread", threadId),
  restoreThread: (threadId: string) => ipcRenderer.invoke("drag:restore-thread", threadId),
  deleteThread: (threadId: string) => ipcRenderer.invoke("drag:delete-thread", threadId),
  setCodexPreferences: (preferences) => ipcRenderer.invoke("drag:set-codex-preferences", preferences),
  sendMessage: (text: string, citationIds: string[] = []) => ipcRenderer.invoke("drag:send-message", { text, citationIds }),
  stopTurn: () => ipcRenderer.invoke("drag:stop-turn"),
  loginWithChatGPT: () => ipcRenderer.invoke("drag:login-chatgpt"),
  openCitation: (citationId: string) => ipcRenderer.invoke("drag:open-citation", citationId),
  subscribe: (listener: (event: AppEvent) => void) => {
    const wrapped = (_event: Electron.IpcRendererEvent, value: AppEvent) => listener(value);
    ipcRenderer.on("drag:event", wrapped);
    return () => ipcRenderer.removeListener("drag:event", wrapped);
  },
};

contextBridge.exposeInMainWorld("drag", api);
