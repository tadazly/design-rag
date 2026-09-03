import { useEffect, useMemo, useRef, useState } from "react";
import { Archive, ArchiveRestore, FolderCog, MessageSquare, MoreHorizontal, Plus, Search, Trash2 } from "lucide-react";
import type { ThreadSummary } from "../../shared/contracts.js";

interface SidebarProps {
  threads: ThreadSummary[];
  activeView: "chat" | "settings";
  onCreateThread: () => void;
  onSelectThread: (id: string) => void;
  onArchiveThread: (id: string) => void;
  onRestoreThread: (id: string, selectAfter: boolean) => void;
  onDeleteThread: (thread: ThreadSummary) => void;
  onNavigate: (view: "chat" | "settings") => void;
}

function isToday(timestamp: number): boolean {
  const date = new Date(timestamp);
  const today = new Date();
  return date.getFullYear() === today.getFullYear() && date.getMonth() === today.getMonth() && date.getDate() === today.getDate();
}

function ThreadGroup({ title, threads, menuThreadId, onMenu, onSelect, onArchive, onRestore, onDelete }: {
  title: string;
  threads: ThreadSummary[];
  menuThreadId: string | null;
  onMenu: (id: string | null) => void;
  onSelect: (thread: ThreadSummary) => void;
  onArchive: (id: string) => void;
  onRestore: (id: string) => void;
  onDelete: (thread: ThreadSummary) => void;
}) {
  if (threads.length === 0) return null;
  return (
    <section className="thread-group">
      <h2>{title}</h2>
      <div className="thread-list">
        {threads.map((thread) => (
          <div className={`thread-row${thread.active ? " is-active" : ""}`} key={thread.id}>
            <button className="thread-row-main" onClick={() => onSelect(thread)} type="button">
              <MessageSquare aria-hidden="true" size={16} strokeWidth={1.8} />
              <span>{thread.title}</span>
              <time>{isToday(thread.updatedAt) ? new Date(thread.updatedAt).toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" }) : new Date(thread.updatedAt).toLocaleDateString("zh-CN", { month: "numeric", day: "numeric" })}</time>
            </button>
            <button className="thread-more" type="button" aria-label={`${thread.title} 更多操作`} aria-haspopup="menu" aria-expanded={menuThreadId === thread.id} onClick={(event) => { event.stopPropagation(); onMenu(menuThreadId === thread.id ? null : thread.id); }}>
              <MoreHorizontal size={17} />
            </button>
            {menuThreadId === thread.id ? (
              <div className="thread-menu" role="menu">
                {thread.archived ? (
                  <button type="button" role="menuitem" onClick={() => onRestore(thread.id)}><ArchiveRestore size={16} />恢复对话</button>
                ) : (
                  <button type="button" role="menuitem" onClick={() => onArchive(thread.id)}><Archive size={16} />归档</button>
                )}
                <button className="danger-menu-item" type="button" role="menuitem" onClick={() => onDelete(thread)}><Trash2 size={16} />删除</button>
              </div>
            ) : null}
          </div>
        ))}
      </div>
    </section>
  );
}

export function Sidebar({ threads, activeView, onCreateThread, onSelectThread, onArchiveThread, onRestoreThread, onDeleteThread, onNavigate }: SidebarProps) {
  const [query, setQuery] = useState("");
  const [showArchived, setShowArchived] = useState(false);
  const [menuThreadId, setMenuThreadId] = useState<string | null>(null);
  const sidebarRef = useRef<HTMLElement>(null);
  const filtered = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return normalized ? threads.filter((thread) => `${thread.title} ${thread.preview}`.toLowerCase().includes(normalized)) : threads;
  }, [query, threads]);
  const activeThreads = filtered.filter((thread) => !thread.archived);
  const archivedThreads = filtered.filter((thread) => thread.archived);
  const today = activeThreads.filter((thread) => isToday(thread.updatedAt));
  const earlier = activeThreads.filter((thread) => !isToday(thread.updatedAt));

  useEffect(() => {
    if (!menuThreadId) return;
    const close = (event: PointerEvent) => {
      if (!sidebarRef.current?.contains(event.target as Node)) setMenuThreadId(null);
    };
    const closeOnEscape = (event: KeyboardEvent) => { if (event.key === "Escape") setMenuThreadId(null); };
    document.addEventListener("pointerdown", close);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      document.removeEventListener("pointerdown", close);
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, [menuThreadId]);

  useEffect(() => {
    if (showArchived && threads.every((thread) => !thread.archived)) setShowArchived(false);
  }, [showArchived, threads]);

  const select = (thread: ThreadSummary) => {
    setMenuThreadId(null);
    if (thread.archived) onRestoreThread(thread.id, true);
    else { onNavigate("chat"); onSelectThread(thread.id); }
  };

  return (
    <aside className="sidebar" aria-label="会话导航" ref={sidebarRef}>
      <button className="brand" type="button" onClick={() => onNavigate("chat")} aria-label="返回 DRAG 游戏策划知识库">
        <span className="brand-mark">DRAG</span><span className="brand-divider">/</span><span className="brand-product">游戏策划知识库</span>
      </button>
      <button className="new-thread-button" type="button" onClick={onCreateThread}><Plus size={18} /><span>新建对话</span></button>
      <label className="sidebar-search"><Search size={17} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索对话" aria-label="搜索对话" /></label>

      <div className="sidebar-scroll">
        {showArchived ? (
          <ThreadGroup title="已归档" threads={archivedThreads} menuThreadId={menuThreadId} onMenu={setMenuThreadId} onSelect={select} onArchive={onArchiveThread} onRestore={(id) => { setMenuThreadId(null); onRestoreThread(id, false); }} onDelete={onDeleteThread} />
        ) : (
          <>
            <ThreadGroup title="今天" threads={today} menuThreadId={menuThreadId} onMenu={setMenuThreadId} onSelect={select} onArchive={(id) => { setMenuThreadId(null); onArchiveThread(id); }} onRestore={(id) => onRestoreThread(id, false)} onDelete={onDeleteThread} />
            <ThreadGroup title="更早" threads={earlier} menuThreadId={menuThreadId} onMenu={setMenuThreadId} onSelect={select} onArchive={(id) => { setMenuThreadId(null); onArchiveThread(id); }} onRestore={(id) => onRestoreThread(id, false)} onDelete={onDeleteThread} />
          </>
        )}
        {(showArchived ? archivedThreads : activeThreads).length === 0 ? <p className="empty-threads">{showArchived ? "没有已归档对话" : "没有匹配的对话"}</p> : null}
      </div>

      <nav className="sidebar-footer" aria-label="知识库导航">
        {archivedThreads.length > 0 || showArchived ? (
          <button className={showArchived ? "is-active" : ""} type="button" onClick={() => { setShowArchived((value) => !value); setMenuThreadId(null); }}>
            <Archive size={17} /><span>{showArchived ? "返回对话" : `已归档对话 (${archivedThreads.length})`}</span>
          </button>
        ) : null}
        <button className={activeView === "settings" ? "is-active" : ""} type="button" onClick={() => onNavigate("settings")}>
          <FolderCog size={18} /><span>资料位置与索引</span>
        </button>
      </nav>
    </aside>
  );
}
