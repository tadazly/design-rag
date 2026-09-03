import { useCallback, useEffect, useMemo, useState } from "react";
import { AlertCircle, AlertTriangle, CheckCircle2, Trash2, X } from "lucide-react";
import type { ThreadSummary } from "../shared/contracts.js";
import { ChatPanel } from "./components/ChatPanel.js";
import { EvidenceRail } from "./components/EvidenceRail.js";
import { SettingsPage } from "./components/SettingsPage.js";
import { Sidebar } from "./components/Sidebar.js";
import { TopBar } from "./components/TopBar.js";
import { useDesktopState } from "./useDesktopState.js";

interface NoticeState {
  id: number;
  kind: "success" | "warning";
  title: string;
  message?: string;
}

export default function App() {
  const { api, snapshot, error, setError, eventNotice, setEventNotice, run } = useDesktopState();
  const [selectedDocumentId, setSelectedDocumentId] = useState<string | null>(null);
  const [pinnedCitationIds, setPinnedCitationIds] = useState<string[]>([]);
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [evidenceOpen, setEvidenceOpen] = useState(false);
  const [pendingDeleteThread, setPendingDeleteThread] = useState<ThreadSummary | null>(null);
  const [notice, setNotice] = useState<NoticeState | null>(null);

  const showNotice = useCallback((title: string, message?: string, kind: NoticeState["kind"] = "success") => {
    setNotice({ id: Date.now(), kind, title, message });
  }, []);

  useEffect(() => {
    const first = snapshot?.evidence?.hits[0]?.documentId ?? null;
    if (first && !snapshot?.evidence?.hits.some((hit) => hit.documentId === selectedDocumentId)) setSelectedDocumentId(first);
  }, [selectedDocumentId, snapshot?.evidence]);

  useEffect(() => setPinnedCitationIds([]), [snapshot?.activeThreadId]);
  useEffect(() => {
    if (!notice) return;
    const timer = window.setTimeout(() => setNotice(null), 5_000);
    return () => window.clearTimeout(timer);
  }, [notice?.id]);
  useEffect(() => {
    if (!eventNotice) return;
    showNotice(eventNotice.title, eventNotice.message, eventNotice.kind === "warning" ? "warning" : "success");
    setEventNotice(null);
  }, [eventNotice, setEventNotice, showNotice]);
  useEffect(() => {
    if (!pendingDeleteThread) return;
    const close = (event: KeyboardEvent) => { if (event.key === "Escape") setPendingDeleteThread(null); };
    document.addEventListener("keydown", close);
    return () => document.removeEventListener("keydown", close);
  }, [pendingDeleteThread]);

  const activeTitle = useMemo(() => snapshot?.threads.find((thread) => thread.id === snapshot.activeThreadId)?.title ?? "新对话", [snapshot?.activeThreadId, snapshot?.threads]);

  if (!snapshot) {
    return <div className="boot-screen"><span className="boot-mark">d</span><p>{error ?? "正在连接 DRAG 本地知识库…"}</p></div>;
  }

  const navigate = async (view: "chat" | "settings") => {
    setSidebarOpen(false);
    await run(() => api.setActiveView(view));
  };

  const openCitation = async (citationId: string) => {
    const result = await run(() => api.openCitation(citationId));
    if (!result) return;
    showNotice(result.note ?? (result.method === "excel-range" ? "已打开源文件并定位到原文区域" : "已打开源文件"));
  };

  return (
    <div className={`app-shell view-${snapshot.activeView}`}>
      <div className={`sidebar-layer${sidebarOpen ? " is-open" : ""}`}>
        <Sidebar
          threads={snapshot.threads}
          activeView={snapshot.activeView}
          onCreateThread={() => void run(async () => { await api.createThread(); await api.setActiveView("chat"); setSidebarOpen(false); })}
          onSelectThread={(id) => void run(async () => { await api.selectThread(id); setSidebarOpen(false); })}
          onArchiveThread={(id) => void run(() => api.archiveThread(id))}
          onRestoreThread={(id, selectAfter) => void run(async () => { await api.restoreThread(id); if (selectAfter) { await api.selectThread(id); await api.setActiveView("chat"); setSidebarOpen(false); } })}
          onDeleteThread={setPendingDeleteThread}
          onNavigate={(view) => void navigate(view)}
        />
      </div>
      {sidebarOpen ? <button className="mobile-scrim" type="button" onClick={() => setSidebarOpen(false)} aria-label="关闭会话栏" /> : null}

      <section className="main-column">
        {snapshot.activeView === "chat" ? (
          <>
            <TopBar
              title={activeTitle}
              account={snapshot.account}
              models={snapshot.models}
              model={snapshot.config.codex.model}
              reasoning={snapshot.config.codex.reasoningEffort}
              onLogin={() => void run(() => api.loginWithChatGPT())}
              onPreferencesChange={(model, reasoningEffort) => void run(() => api.setCodexPreferences({ model, reasoningEffort }))}
              onToggleSidebar={() => setSidebarOpen((value) => !value)}
              onToggleEvidence={() => setEvidenceOpen((value) => !value)}
            />
            <ChatPanel
              messages={snapshot.messages}
              evidence={snapshot.evidence}
              retrieval={snapshot.retrieval}
              pinnedCitationIds={pinnedCitationIds}
              onOpenCitation={(id) => void openCitation(id)}
              onSend={async (text) => { await run(() => api.sendMessage(text, pinnedCitationIds)); }}
              onStop={async () => { await run(() => api.stopTurn()); }}
            />
          </>
        ) : (
          <SettingsPage
            config={snapshot.config}
            index={snapshot.index}
            onSaveConfig={async (config) => Boolean(await run(() => api.saveConfig(config)))}
            onChooseDirectory={async (sourceId) => (await run(() => api.chooseSourceDirectory(sourceId))) ?? null}
            onChooseNewDirectory={async () => (await run(() => api.chooseDirectory())) ?? null}
            onResolveDroppedPath={async (file) => (await run(() => api.resolveDroppedPath(file))) ?? null}
            onRebuild={async (full) => { await run(() => api.rebuildIndex(full)); }}
            onPause={async () => { await run(() => api.pauseIndex()); }}
            onResume={async () => { await run(() => api.resumeIndex()); }}
            onClearCache={async () => { await run(() => api.clearIndexCache()); }}
          />
        )}
      </section>

      {snapshot.activeView === "chat" ? (
        <div className={`evidence-layer${evidenceOpen ? " is-open" : ""}`}>
          <EvidenceRail
            evidence={snapshot.evidence}
            retrieval={snapshot.retrieval}
            selectedDocumentId={selectedDocumentId}
            pinnedCitationIds={pinnedCitationIds}
            onSelectDocument={setSelectedDocumentId}
            onToggleCitation={(id) => setPinnedCitationIds((current) => current.includes(id) ? current.filter((item) => item !== id) : [...current, id])}
            onOpenCitation={(id) => void openCitation(id)}
            onClose={() => setEvidenceOpen(false)}
          />
        </div>
      ) : null}
      {evidenceOpen && snapshot.activeView === "chat" ? <button className="mobile-scrim evidence-scrim" type="button" onClick={() => setEvidenceOpen(false)} aria-label="关闭检索证据" /> : null}

      {error ? (
        <div className="error-toast" role="alert"><AlertCircle size={17} /><span>{error}</span><button type="button" onClick={() => setError(null)} aria-label="关闭错误"><X size={16} /></button></div>
      ) : null}
      {notice ? (
        <div className={`notice-toast is-${notice.kind}`} role="status">
          {notice.kind === "warning" ? <AlertTriangle size={18} /> : <CheckCircle2 size={18} />}
          <span><strong>{notice.title}</strong>{notice.message ? <small>{notice.message}</small> : null}</span>
          <button type="button" onClick={() => setNotice(null)} aria-label="关闭提示"><X size={16} /></button>
        </div>
      ) : null}
      {pendingDeleteThread ? (
        <div className="dialog-scrim" role="presentation" onMouseDown={(event) => { if (event.currentTarget === event.target) setPendingDeleteThread(null); }}>
          <section className="confirm-dialog" role="dialog" aria-modal="true" aria-labelledby="delete-thread-title">
            <div className="dialog-icon"><Trash2 size={20} /></div>
            <div><h2 id="delete-thread-title">删除这个对话？</h2><p>“{pendingDeleteThread.title}”及其本地消息和检索证据将被删除。此操作不可撤销。</p></div>
            <div className="dialog-actions"><button type="button" autoFocus onClick={() => setPendingDeleteThread(null)}>取消</button><button className="danger-confirm" type="button" onClick={() => void run(async () => { await api.deleteThread(pendingDeleteThread.id); setPendingDeleteThread(null); })}>删除</button></div>
          </section>
        </div>
      ) : null}
    </div>
  );
}
