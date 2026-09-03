import { useEffect, useId, useMemo, useState } from "react";
import {
  AlertTriangle,
  Check,
  CheckCircle2,
  FileWarning,
  Folder,
  FolderInput,
  FolderOpen,
  FolderPlus,
  LockKeyhole,
  Pause,
  Play,
  Plus,
  RefreshCw,
  Search,
  Trash2,
  X,
} from "lucide-react";
import type { AppConfig, IndexStatus, KnowledgeSourceConfig, SourceKind } from "../../shared/contracts.js";

interface SettingsPageProps {
  config: AppConfig;
  index: IndexStatus;
  onSaveConfig: (config: AppConfig) => Promise<boolean>;
  onChooseDirectory: (sourceId: string) => Promise<string | null>;
  onChooseNewDirectory?: () => Promise<string | null>;
  onResolveDroppedPath?: (file: File) => Promise<string | null>;
  onRebuild: (full: boolean) => Promise<void>;
  onPause: () => Promise<void>;
  onResume: () => Promise<void>;
  onClearCache: () => Promise<void>;
  onCloseDetail?: () => void;
}

interface SourceDraft {
  kind: SourceKind;
  label: string;
  rootPath: string;
}

const COMMON_EXCLUDES = [".git", ".svn", "node_modules", "dist", "build", "temp", "tmp", "__macosx"];
const DEFAULT_EXTENSIONS: Record<SourceKind, string[]> = {
  design: [".docx", ".xlsx", ".xlsm", ".xls", ".pdf", ".xmind", ".md", ".markdown", ".txt", ".html", ".json", ".yaml", ".yml"],
  table: [".xlsx", ".xlsm", ".xls", ".csv", ".pdf"],
};
const DEFAULT_SOURCE_LABEL: Record<SourceKind, string> = { design: "策划案", table: "配置表" };

function formatCount(value: number): string {
  return new Intl.NumberFormat("zh-CN").format(value);
}

function formatDateTime(value: string): string {
  return new Intl.DateTimeFormat("zh-CN", {
    month: "numeric",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(value));
}

function normalizePath(value: string): string {
  return value.trim().replace(/[\\/]+$/, "").replaceAll("\\", "/").toLocaleLowerCase();
}

function createSourceId(kind: SourceKind, sources: KnowledgeSourceConfig[]): string {
  const existing = new Set(sources.map((source) => source.id));
  const base = `${kind}-${Date.now().toString(36)}`;
  let candidate = base;
  let suffix = 2;
  while (existing.has(candidate)) candidate = `${base}-${suffix++}`;
  return candidate;
}

function SourceRow({ source, count, selected, busy, onSelect, onChoose, onToggle, onDelete }: {
  source: KnowledgeSourceConfig;
  count: number;
  selected: boolean;
  busy: boolean;
  onSelect: () => void;
  onChoose: () => void;
  onToggle: () => void;
  onDelete: () => void;
}) {
  const visibleExtensions = source.includeExtensions.slice(0, 6);
  const hiddenExtensionCount = source.includeExtensions.length - visibleExtensions.length;
  return (
    <article
      className={`source-row${source.enabled ? "" : " is-disabled"}${selected ? " is-selected" : ""}`}
      tabIndex={0}
      aria-label={`${source.label}，${source.kind === "design" ? "策划案" : "配置表"}来源`}
      onClick={onSelect}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          onSelect();
        }
      }}
    >
      <Folder size={26} strokeWidth={1.6} aria-hidden="true" />
      <div className="source-primary">
        <span className="source-title-line"><strong>{source.label}</strong><em>{source.kind === "design" ? "策划案" : "配置表"}</em></span>
        <span title={source.rootPath}>{source.rootPath}</span>
      </div>
      <div className="source-patterns">
        <span>格式：{visibleExtensions.join(" · ")}{hiddenExtensionCount > 0 ? ` · +${hiddenExtensionCount}` : ""}</span>
        <span>{source.enabled ? "已启用 · 自动检查新增与修改" : "已停用 · 本地索引缓存仍保留"}</span>
      </div>
      <div className="source-count"><strong>{formatCount(count)}</strong><span>{source.enabled ? "已索引文档" : "缓存文档"}</span></div>
      <div className="source-row-actions">
        <button className="secondary-button" type="button" disabled={busy} onClick={(event) => { event.stopPropagation(); onChoose(); }}>
          <FolderOpen size={14} />更改位置
        </button>
        <button className="source-delete-button" type="button" disabled={busy} onClick={(event) => { event.stopPropagation(); onDelete(); }} aria-label={`删除来源 ${source.label}`} title="删除来源">
          <Trash2 size={15} />
        </button>
      </div>
      <button
        className={`switch${source.enabled ? " is-on" : ""}`}
        type="button"
        role="switch"
        aria-checked={source.enabled}
        disabled={busy}
        onClick={(event) => { event.stopPropagation(); onToggle(); }}
        aria-label={`${source.enabled ? "停用" : "启用"}${source.label}`}
      ><span /></button>
    </article>
  );
}

function SourceDropAction({ disabled, dragActive, empty, onClick, onDrop, onDragActiveChange }: {
  disabled: boolean;
  dragActive: boolean;
  empty: boolean;
  onClick: () => void;
  onDrop: (file: File) => void;
  onDragActiveChange: (active: boolean) => void;
}) {
  return (
    <button
      className={`source-drop-action${dragActive ? " is-dragging" : ""}${empty ? " is-empty" : ""}`}
      type="button"
      disabled={disabled}
      onClick={onClick}
      onDragEnter={(event) => { event.preventDefault(); if (!disabled) onDragActiveChange(true); }}
      onDragOver={(event) => { event.preventDefault(); if (!disabled) event.dataTransfer.dropEffect = "copy"; }}
      onDragLeave={(event) => {
        const nextTarget = event.relatedTarget;
        if (!(nextTarget instanceof Node) || !event.currentTarget.contains(nextTarget)) onDragActiveChange(false);
      }}
      onDrop={(event) => {
        event.preventDefault();
        onDragActiveChange(false);
        if (disabled) return;
        const file = event.dataTransfer.files.item(0);
        if (file) onDrop(file);
      }}
    >
      <span className="source-drop-icon">{empty ? <FolderPlus size={25} /> : <Plus size={17} />}</span>
      <span><strong>{empty ? "添加第一个资料源" : "添加资料源"}</strong><small>{dragActive ? "松开以读取文件夹位置" : "选择目录，或将文件夹拖到这里"}</small></span>
    </button>
  );
}

function StrategyRow({ label, description, children }: { label: string; description: string; children: React.ReactNode }) {
  return <div className="strategy-row"><strong>{label}</strong><span>{description}</span><div className="strategy-control">{children}</div></div>;
}

function AddSourceDialog({ draft, busy, resolvingDrop, error, onDraftChange, onCancel, onBrowse, onDrop, onSubmit }: {
  draft: SourceDraft;
  busy: boolean;
  resolvingDrop: boolean;
  error: string | null;
  onDraftChange: (draft: SourceDraft) => void;
  onCancel: () => void;
  onBrowse?: () => void;
  onDrop: (file: File) => void;
  onSubmit: () => void;
}) {
  const labelId = useId();
  const pathId = useId();
  const [dragActive, setDragActive] = useState(false);
  const switchKind = (kind: SourceKind) => {
    const previousDefault = DEFAULT_SOURCE_LABEL[draft.kind];
    onDraftChange({ ...draft, kind, label: draft.label === previousDefault ? DEFAULT_SOURCE_LABEL[kind] : draft.label });
  };
  return (
    <div className="dialog-scrim" role="presentation" onMouseDown={(event) => { if (!busy && event.currentTarget === event.target) onCancel(); }}>
      <form className="source-dialog" role="dialog" aria-modal="true" aria-labelledby="add-source-title" onSubmit={(event) => { event.preventDefault(); onSubmit(); }}>
        <header className="source-dialog-header">
          <div className="dialog-icon source-dialog-icon"><FolderPlus size={20} /></div>
          <div><h2 id="add-source-title">添加资料源</h2><p>添加后会自动执行增量索引；源文件始终只读。</p></div>
          <button className="icon-button" type="button" disabled={busy} onClick={onCancel} aria-label="关闭添加来源"><X size={18} /></button>
        </header>

        <div className="source-dialog-body">
          <fieldset className="source-kind-field">
            <legend>资料类型</legend>
            <div className="source-kind-options">
              <button type="button" className={draft.kind === "design" ? "is-selected" : ""} aria-pressed={draft.kind === "design"} onClick={() => switchKind("design")}>
                <Folder size={18} /><span><strong>策划案</strong><small>玩法、流程、历史改动与活动方案</small></span>
              </button>
              <button type="button" className={draft.kind === "table" ? "is-selected" : ""} aria-pressed={draft.kind === "table"} onClick={() => switchKind("table")}>
                <FolderInput size={18} /><span><strong>配置表</strong><small>业务表、字段、参数与版本配置</small></span>
              </button>
            </div>
          </fieldset>

          <label className="dialog-field" htmlFor={labelId}><span>来源名称</span><input id={labelId} autoFocus value={draft.label} onChange={(event) => onDraftChange({ ...draft, label: event.target.value })} placeholder="例如：历史策划案" /></label>
          <label className="dialog-field" htmlFor={pathId}>
            <span>文件夹位置</span>
            <span className="path-input-row"><input id={pathId} value={draft.rootPath} onChange={(event) => onDraftChange({ ...draft, rootPath: event.target.value })} placeholder="输入完整路径，或浏览/拖入文件夹" /><button className="secondary-button" type="button" disabled={!onBrowse || busy} onClick={onBrowse}><FolderOpen size={15} />浏览文件夹</button></span>
          </label>

          <div
            className={`folder-dropzone${dragActive ? " is-dragging" : ""}${resolvingDrop ? " is-resolving" : ""}`}
            onDragEnter={(event) => { event.preventDefault(); setDragActive(true); }}
            onDragOver={(event) => { event.preventDefault(); event.dataTransfer.dropEffect = "copy"; }}
            onDragLeave={(event) => {
              const nextTarget = event.relatedTarget;
              if (!(nextTarget instanceof Node) || !event.currentTarget.contains(nextTarget)) setDragActive(false);
            }}
            onDrop={(event) => {
              event.preventDefault();
              setDragActive(false);
              const file = event.dataTransfer.files.item(0);
              if (file) onDrop(file);
            }}
          >
            {resolvingDrop ? <RefreshCw className="spin" size={20} /> : <FolderInput size={21} />}
            <span><strong>{resolvingDrop ? "正在确认文件夹…" : dragActive ? "松开以使用这个文件夹" : "也可以直接拖入文件夹"}</strong><small>目录会在本机校验，文件不会被上传或修改。</small></span>
          </div>
          {error ? <p className="source-form-error" role="alert"><AlertTriangle size={15} />{error}</p> : null}
        </div>

        <footer className="dialog-actions source-dialog-actions">
          <button type="button" disabled={busy} onClick={onCancel}>取消</button>
          <button className="primary-button" type="submit" disabled={busy || resolvingDrop}>{busy ? <RefreshCw className="spin" size={15} /> : <Plus size={15} />}{busy ? "正在添加" : "添加并增量索引"}</button>
        </footer>
      </form>
    </div>
  );
}

export function SettingsPage({ config, index, onSaveConfig, onChooseDirectory, onChooseNewDirectory, onResolveDroppedPath, onRebuild, onPause, onResume, onClearCache, onCloseDetail }: SettingsPageProps) {
  const [selectedSourceId, setSelectedSourceId] = useState(config.sources[0]?.id ?? "");
  const [rebuilding, setRebuilding] = useState(false);
  const [clearing, setClearing] = useState(false);
  const [pendingSourceId, setPendingSourceId] = useState<string | null>(null);
  const [pendingDeleteSource, setPendingDeleteSource] = useState<KnowledgeSourceConfig | null>(null);
  const [addDialogOpen, setAddDialogOpen] = useState(false);
  const [sourceDraft, setSourceDraft] = useState<SourceDraft>({ kind: "design", label: DEFAULT_SOURCE_LABEL.design, rootPath: "" });
  const [sourceFormError, setSourceFormError] = useState<string | null>(null);
  const [savingSource, setSavingSource] = useState(false);
  const [resolvingDrop, setResolvingDrop] = useState(false);
  const [listDragActive, setListDragActive] = useState(false);
  const selected = config.sources.find((source) => source.id === selectedSourceId) ?? config.sources[0] ?? null;
  const activeRun = index.activeRun;
  const run = activeRun ?? index.lastRun;
  const processed = (activeRun?.indexed ?? 0) + (activeRun?.unchanged ?? 0) + (activeRun?.failed ?? 0);
  const progressPercent = activeRun && activeRun.discovered > 0
    ? Math.min(100, Math.round((processed / activeRun.discovered) * 100))
    : activeRun?.phase === "discover" ? null : 100;
  const issues = index.recentIssues;
  const paused = activeRun?.phase === "paused";
  const pausing = activeRun?.phase === "pausing";
  const sourceActionsDisabled = Boolean(activeRun);
  const allSourcesDisabled = config.sources.length > 0 && config.sources.every((source) => !source.enabled);

  useEffect(() => {
    if (selectedSourceId && config.sources.some((source) => source.id === selectedSourceId)) return;
    setSelectedSourceId(config.sources[0]?.id ?? "");
  }, [config.sources, selectedSourceId]);

  useEffect(() => {
    if (!addDialogOpen && !pendingDeleteSource) return;
    const close = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || savingSource) return;
      setAddDialogOpen(false);
      setPendingDeleteSource(null);
    };
    document.addEventListener("keydown", close);
    return () => document.removeEventListener("keydown", close);
  }, [addDialogOpen, pendingDeleteSource, savingSource]);

  const update = async (mutator: (draft: AppConfig) => void): Promise<boolean> => {
    const draft = structuredClone(config);
    mutator(draft);
    try {
      return await onSaveConfig(draft);
    } catch {
      return false;
    }
  };

  const openAddDialog = () => {
    setSourceDraft({ kind: "design", label: DEFAULT_SOURCE_LABEL.design, rootPath: "" });
    setSourceFormError(null);
    setAddDialogOpen(true);
  };

  const changeDirectory = async (source: KnowledgeSourceConfig) => {
    setSelectedSourceId(source.id);
    setPendingSourceId(source.id);
    try {
      const chosen = await onChooseDirectory(source.id);
      if (!chosen) return;
      const saved = await update((draft) => {
        const target = draft.sources.find((item) => item.id === source.id);
        if (target) target.rootPath = chosen;
      });
      if (!saved) return;
    } finally {
      setPendingSourceId(null);
    }
  };

  const resolveDroppedFolder = async (file: File) => {
    if (!onResolveDroppedPath) {
      setSourceFormError("当前运行环境无法读取拖入目录，请改用“浏览文件夹”或手动输入路径。");
      return;
    }
    setResolvingDrop(true);
    setSourceFormError(null);
    try {
      const resolved = await onResolveDroppedPath(file);
      if (!resolved) {
        setSourceFormError("拖入的项目不是可读取的文件夹，请重新选择。");
        return;
      }
      setSourceDraft((current) => ({ ...current, rootPath: resolved }));
    } finally {
      setResolvingDrop(false);
    }
  };

  const openAddDialogFromDrop = (file: File) => {
    openAddDialog();
    void resolveDroppedFolder(file);
  };

  const browseNewSource = async () => {
    if (!onChooseNewDirectory) return;
    setResolvingDrop(true);
    setSourceFormError(null);
    try {
      const chosen = await onChooseNewDirectory();
      if (chosen) setSourceDraft((current) => ({ ...current, rootPath: chosen }));
    } finally {
      setResolvingDrop(false);
    }
  };

  const addSource = async () => {
    const label = sourceDraft.label.trim();
    const rootPath = sourceDraft.rootPath.trim();
    if (!label) {
      setSourceFormError("请填写便于识别的来源名称。");
      return;
    }
    if (!rootPath) {
      setSourceFormError("请选择、拖入或手动输入资料文件夹。");
      return;
    }
    if (config.sources.some((source) => normalizePath(source.rootPath) === normalizePath(rootPath))) {
      setSourceFormError("这个文件夹已经作为资料源添加，无需重复建立索引。");
      return;
    }

    const id = createSourceId(sourceDraft.kind, config.sources);
    const template = config.sources.find((source) => source.kind === sourceDraft.kind);
    const source: KnowledgeSourceConfig = {
      id,
      label,
      kind: sourceDraft.kind,
      rootPath,
      enabled: true,
      includeExtensions: [...(template?.includeExtensions ?? DEFAULT_EXTENSIONS[sourceDraft.kind])],
      excludeDirectoryNames: [...(template?.excludeDirectoryNames ?? COMMON_EXCLUDES)],
      maxFileBytes: template?.maxFileBytes ?? 128 * 1024 * 1024,
    };
    setSavingSource(true);
    setSourceFormError(null);
    try {
      const saved = await update((draft) => { draft.sources.push(source); });
      if (!saved) {
        setSourceFormError("来源未能保存，请查看右下角错误信息后重试。");
        return;
      }
      setSelectedSourceId(id);
      setAddDialogOpen(false);
    } finally {
      setSavingSource(false);
    }
  };

  const toggleSource = async (source: KnowledgeSourceConfig) => {
    setPendingSourceId(source.id);
    try {
      const saved = await update((draft) => {
        const target = draft.sources.find((item) => item.id === source.id);
        if (target) target.enabled = !target.enabled;
      });
      if (!saved) return;
    } finally {
      setPendingSourceId(null);
    }
  };

  const deleteSource = async () => {
    if (!pendingDeleteSource) return;
    const source = pendingDeleteSource;
    setSavingSource(true);
    try {
      const saved = await update((draft) => { draft.sources = draft.sources.filter((item) => item.id !== source.id); });
      if (!saved) return;
      setPendingDeleteSource(null);
      setSelectedSourceId(config.sources.find((item) => item.id !== source.id)?.id ?? "");
    } finally {
      setSavingSource(false);
    }
  };

  const rebuild = async (full: boolean) => {
    setRebuilding(true);
    try { await onRebuild(full); } finally { setRebuilding(false); }
  };

  const clearCache = async () => {
    setClearing(true);
    try { await onClearCache(); } finally { setClearing(false); }
  };

  const progressTitle = paused
    ? "索引已暂停"
    : pausing
      ? "正在暂停，等待当前文件处理完成"
      : activeRun?.phase === "discover"
        ? "正在发现文件"
        : `正在处理 ${formatCount(processed)} / ${formatCount(activeRun?.discovered ?? 0)}`;

  const stages = useMemo(() => [
    { phase: "discover", label: "发现文件", detail: "扫描已配置资料源", count: run?.discovered ?? 0 },
    { phase: "extract", label: "内容提取", detail: "读取正文、表格与元数据", count: (run?.indexed ?? 0) + (run?.unchanged ?? 0) },
    { phase: "chunk", label: "结构分块", detail: "保留章节、工作表与定位", count: index.chunkCount },
    { phase: "index", label: "建立索引", detail: "写入 SQLite FTS5", count: index.documentCount },
  ], [index.chunkCount, index.documentCount, run]);

  return (
    <main className="settings-page">
      <section className="settings-content">
        <header className="settings-header">
          <div><h1>资料位置与索引</h1><p>配置资料来源目录与索引策略。源文件只读，索引保存在本机。</p></div>
          <div className="settings-header-actions">
            <button className="secondary-button add-source-button" type="button" disabled={sourceActionsDisabled} onClick={openAddDialog}><FolderPlus size={16} />添加来源</button>
            <button className="primary-button" type="button" disabled={rebuilding || Boolean(activeRun) || config.sources.length === 0} onClick={() => void rebuild(true)}>
              <RefreshCw size={16} className={rebuilding ? "spin" : ""} />{rebuilding ? "正在重建" : "立即重建索引"}
            </button>
          </div>
        </header>

        <section className="settings-section">
          <div className="section-title-row source-section-title"><h2>资料来源</h2><span>{config.sources.length > 0 ? `${config.sources.filter((source) => source.enabled).length} 个启用 · ${config.sources.length} 个来源` : "尚未配置"}</span></div>
          <div className={`source-list${config.sources.length === 0 ? " is-empty" : ""}`}>
            {config.sources.map((source) => (
              <SourceRow
                key={source.id}
                source={source}
                count={index.sourceCounts[source.id] ?? 0}
                selected={selected?.id === source.id}
                busy={sourceActionsDisabled || pendingSourceId === source.id}
                onSelect={() => setSelectedSourceId(source.id)}
                onChoose={() => void changeDirectory(source)}
                onToggle={() => void toggleSource(source)}
                onDelete={() => setPendingDeleteSource(source)}
              />
            ))}
            <SourceDropAction
              disabled={sourceActionsDisabled}
              dragActive={listDragActive}
              empty={config.sources.length === 0}
              onClick={openAddDialog}
              onDrop={openAddDialogFromDrop}
              onDragActiveChange={setListDragActive}
            />
          </div>
          {sourceActionsDisabled ? <p className="source-action-note">索引运行期间暂不修改来源；暂停或等待完成后即可操作。</p> : null}
        </section>

        <section className="settings-section">
          <h2>索引策略</h2>
          <div className="strategy-list">
            <StrategyRow label="自动增量扫描" description={`打开应用时检查一次，之后每 ${config.indexing.scanIntervalMinutes} 分钟检测新增与修改`}>
              <button className={`switch${config.indexing.automaticScan ? " is-on" : ""}`} type="button" role="switch" aria-checked={config.indexing.automaticScan} onClick={() => void update((draft) => { draft.indexing.automaticScan = !draft.indexing.automaticScan; })}><span /></button>
            </StrategyRow>
            <StrategyRow label="支持格式" description="DOCX · XLSX · XLSM · XLS · PDF · XMind · MD · TXT">
              <span className="capability-state"><Check size={14} /> 已配置</span>
            </StrategyRow>
            <StrategyRow label="检索模式" description={config.search.embedding.enabled ? "中文词法召回 + 本地语义重排" : "中文词法召回（CJK shingles + trigram）"}>
              <select className="select-control" aria-label="检索模式" value={config.search.embedding.enabled ? "hybrid" : "lexical"} onChange={(event) => void update((draft) => { draft.search.embedding.enabled = event.target.value === "hybrid"; })}>
                <option value="lexical">关键词增强</option>
                <option value="hybrid">关键词 + 本地语义</option>
              </select>
            </StrategyRow>
            <StrategyRow label="结果排序偏好" description="优先使用文件名、版本记录和版本路径中的业务日期">
              <span className="capability-state">最新内容优先</span>
            </StrategyRow>
            <StrategyRow label="本地嵌入模型（可选）" description={`${config.search.embedding.model} · ${config.search.embedding.endpoint}`}>
              <span className={config.search.embedding.enabled ? "model-status is-ready" : "model-status"}><span />{config.search.embedding.enabled ? "已启用" : "未启用"}</span>
            </StrategyRow>
          </div>
        </section>

        <section className="settings-section index-status-section">
          <div className="section-title-row"><h2>索引状态</h2><span>revision {index.indexRevision}</span></div>
          {activeRun ? (
            <div className="index-progress" aria-live="polite">
              <div className="progress-copy">
                <div className="progress-stats"><strong>{progressTitle}</strong><span>{progressPercent === null ? "扫描资料目录…" : `${progressPercent}%`} · 成功 {formatCount(activeRun.indexed)} · 未变化 {formatCount(activeRun.unchanged)} · 失败 {formatCount(activeRun.failed)}</span></div>
                <button className="index-pause-button" type="button" disabled={pausing} onClick={() => void (paused ? onResume() : onPause())}>
                  {paused ? <Play size={14} /> : <Pause size={14} />}{paused ? "继续" : pausing ? "暂停中" : "暂停"}
                </button>
              </div>
              <div className={`progress-track${progressPercent === null ? " is-indeterminate" : ""}`} role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={progressPercent ?? undefined}>
                <span style={progressPercent === null ? undefined : { width: `${progressPercent}%` }} />
              </div>
              <p title={activeRun.currentPath ?? undefined}>{activeRun.currentPath ?? "正在整理索引状态"}</p>
            </div>
          ) : (
            <div className={`index-last-update${allSourcesDisabled ? " is-muted" : ""}`} role="status">
              {config.sources.length === 0 ? <FolderPlus size={18} /> : allSourcesDisabled ? <Pause size={18} /> : <CheckCircle2 size={18} />}
              <div>
                <strong>{config.sources.length === 0 ? "等待添加资料源" : allSourcesDisabled ? "所有资料源均已停用" : index.lastRun ? "索引已更新" : "等待首次索引"}</strong>
                <span>{config.sources.length === 0
                  ? "添加策划案或配置表目录后会自动执行增量索引。"
                  : allSourcesDisabled
                    ? "缓存仍保留，但新的检索不会返回这些来源。"
                    : index.lastRun?.finishedAt
                      ? `${formatDateTime(index.lastRun.finishedAt)} 完成 · 更新 ${formatCount(index.lastRun.indexed)} · 无变化 ${formatCount(index.lastRun.unchanged)} · 失败 ${formatCount(index.lastRun.failed)}`
                      : "应用会在启动和检测到资料变化后自动增量更新。"}</span>
              </div>
            </div>
          )}
          <div className="index-table" role="table" aria-label="索引阶段">
            <div className="index-table-head" role="row"><span>阶段</span><span>状态</span><span>详情</span><span>数量</span></div>
            {stages.map((stage) => {
              const discovering = activeRun?.phase === "discover";
              const isActive = Boolean(activeRun) && (discovering ? stage.phase === "discover" : stage.phase !== "discover");
              const isPending = Boolean(activeRun) && discovering && stage.phase !== "discover";
              return (
                <div className="index-stage-row" role="row" key={stage.phase}>
                  <span><Search size={15} />{stage.label}</span>
                  <span className={isActive ? "stage-active" : isPending ? "stage-pending" : "stage-complete"}>
                    {isActive ? <RefreshCw className="spin" size={14} /> : isPending ? null : <Check size={14} />}
                    {isActive ? "进行中" : isPending ? "待处理" : "完成"}
                  </span>
                  <span>{stage.detail}</span>
                  <strong>{formatCount(stage.count)}</strong>
                </div>
              );
            })}
            <div className="index-summary-row"><span>已索引 {formatCount(run?.indexed ?? 0)}</span><span>未变化 {formatCount(run?.unchanged ?? 0)}</span><span>已跳过 {formatCount(run?.skipped ?? 0)}</span><span className={run?.failed ? "has-failure" : ""}>失败 {formatCount(run?.failed ?? 0)}</span></div>
            {issues[0] ? (
              <button className="issue-row" type="button" onClick={() => setSelectedSourceId(issues[0]?.sourceId ?? selectedSourceId)}>
                <AlertTriangle size={16} /><strong>{issues[0].code}</strong><span>{issues[0].message}</span><time>{new Date(issues[0].occurredAt).toLocaleString("zh-CN")}</time>
              </button>
            ) : null}
          </div>
          <div className="cache-actions">
            <div><strong>本地检索缓存</strong><span>只删除可重建的 SQLite 索引，不会删除策划案、配表、会话或设置。</span></div>
            <button className="danger-button" type="button" disabled={Boolean(activeRun) || clearing} onClick={() => void clearCache()}>
              <Trash2 size={14} />{clearing ? "正在删除" : "删除本地检索缓存"}
            </button>
          </div>
        </section>

        <footer className="local-privacy"><LockKeyhole size={15} />资料与索引默认仅保存在本机；只有用户发起 AI 对话时，命中片段才会发送给 ChatGPT 分析。</footer>
      </section>

      <aside className="source-detail">
        <div className="source-detail-header"><h2>来源详情</h2>{onCloseDetail ? <button className="icon-button" type="button" onClick={onCloseDetail}><X size={18} /></button> : null}</div>
        {selected ? (
          <>
            <div className="source-detail-title"><Folder size={28} /><div><strong>{selected.label}</strong><span>{selected.rootPath}</span></div></div>
            <p className={`source-detail-state${selected.enabled ? " is-enabled" : ""}`}><span />{selected.enabled ? "已启用并参与检索" : "已停用，缓存已从检索中屏蔽"}</p>
            <section><h3>资料类型</h3><p>{selected.kind === "design" ? "策划案：玩法、流程、历史改动与活动方案" : "配置表：业务表、字段、参数与版本配置"}</p></section>
            <section><h3>包含格式</h3><p className="pattern-list">{selected.includeExtensions.join("\n")}</p></section>
            <section><h3>排除目录</h3><p className="pattern-list">{selected.excludeDirectoryNames.join("\n")}</p></section>
            <section><h3>安全边界</h3><p>不跟随 symlink/junction；解析后路径必须仍位于授权 root 内。宏只读且永不执行。</p></section>
            <section><h3>最近问题</h3>{issues.filter((issue) => issue.sourceId === selected.id).slice(0, 3).map((issue) => <div className="detail-issue" key={`${issue.path}-${issue.occurredAt}`}><FileWarning size={16} /><div><strong>{issue.code}</strong><span>{issue.message}</span><small>{issue.path}</small></div></div>)}{issues.every((issue) => issue.sourceId !== selected.id) ? <p className="no-issue"><Check size={15} />没有解析问题</p> : null}</section>
          </>
        ) : (
          <div className="source-detail-empty"><FolderPlus size={25} /><strong>还没有资料源</strong><span>添加目录后可在此查看格式、安全边界和解析问题。</span><button className="secondary-button" type="button" onClick={openAddDialog}>添加来源</button></div>
        )}
      </aside>

      {addDialogOpen ? (
        <AddSourceDialog
          draft={sourceDraft}
          busy={savingSource}
          resolvingDrop={resolvingDrop}
          error={sourceFormError}
          onDraftChange={setSourceDraft}
          onCancel={() => setAddDialogOpen(false)}
          onBrowse={onChooseNewDirectory ? () => void browseNewSource() : undefined}
          onDrop={(file) => void resolveDroppedFolder(file)}
          onSubmit={() => void addSource()}
        />
      ) : null}

      {pendingDeleteSource ? (
        <div className="dialog-scrim" role="presentation" onMouseDown={(event) => { if (!savingSource && event.currentTarget === event.target) setPendingDeleteSource(null); }}>
          <section className="confirm-dialog" role="dialog" aria-modal="true" aria-labelledby="delete-source-title">
            <div className="dialog-icon"><Trash2 size={20} /></div>
            <div><h2 id="delete-source-title">删除资料源？</h2><p>将从知识库移除“{pendingDeleteSource.label}”及其 {formatCount(index.sourceCounts[pendingDeleteSource.id] ?? 0)} 份本地索引记录。源文件不会被删除或修改。</p></div>
            <div className="dialog-actions"><button type="button" autoFocus disabled={savingSource} onClick={() => setPendingDeleteSource(null)}>取消</button><button className="danger-confirm" type="button" disabled={savingSource} onClick={() => void deleteSource()}>{savingSource ? "正在删除" : "删除来源"}</button></div>
          </section>
        </div>
      ) : null}
    </main>
  );
}
