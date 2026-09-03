import { useEffect, useMemo, useState } from "react";
import { BookOpenCheck, CalendarDays, Check, ExternalLink, FileSpreadsheet, FileText, LoaderCircle, Search, X } from "lucide-react";
import type { RetrievalActivity, SearchHit, SearchResponse, SourceKind } from "../../shared/contracts.js";

const SECTION_LABELS: Record<string, string> = {
  overview: "概述",
  version_history: "历史改动",
  flow: "流程",
  gameplay: "玩法",
  panel_logic: "面板逻辑",
  config: "配置",
  reward_value: "奖励数值",
  statistics: "统计",
  art_requirement: "美术",
  animation_requirement: "动画",
  other: "其他",
};

interface EvidenceRailProps {
  evidence: SearchResponse | null;
  retrieval: RetrievalActivity;
  selectedDocumentId: string | null;
  pinnedCitationIds: string[];
  onSelectDocument: (id: string) => void;
  onToggleCitation: (id: string) => void;
  onOpenCitation: (id: string) => void;
  onClose?: () => void;
}

function dateLabel(value: string): string {
  return new Date(value).toLocaleDateString("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit" });
}

function EvidenceRow({ hit, selected, onSelect }: { hit: SearchHit; selected: boolean; onSelect: () => void }) {
  return (
    <button className={`evidence-row${selected ? " is-selected" : ""}`} type="button" onClick={onSelect}>
      <span className="evidence-type-icon">{hit.sourceKind === "table" ? <FileSpreadsheet size={16} /> : <FileText size={16} />}</span>
      <span className="evidence-row-main">
        <strong>{hit.title}</strong>
        <span className="evidence-meta"><CalendarDays size={12} /> {dateLabel(hit.effectiveUpdatedAt)} · {hit.dateSource === "filename" ? "文件名日期" : "业务日期"}</span>
        <span className="tag-line">{hit.sectionTypes.slice(0, 4).map((type) => <em key={type}>{SECTION_LABELS[type] ?? type}</em>)}</span>
      </span>
      <span className="relevance">{Math.round(hit.relevance * 100)}%</span>
    </button>
  );
}

export function EvidenceRail({ evidence, retrieval, selectedDocumentId, pinnedCitationIds, onSelectDocument, onToggleCitation, onOpenCitation, onClose }: EvidenceRailProps) {
  const [kind, setKind] = useState<SourceKind | "all">("all");
  const [selectedChunkId, setSelectedChunkId] = useState<string | null>(null);
  useEffect(() => setKind("all"), [evidence]);
  const filtered = useMemo(() => evidence?.hits.filter((hit) => kind === "all" || hit.sourceKind === kind) ?? [], [evidence, kind]);
  useEffect(() => {
    if (filtered.length > 0 && !filtered.some((hit) => hit.documentId === selectedDocumentId)) onSelectDocument(filtered[0]?.documentId ?? "");
  }, [filtered, onSelectDocument, selectedDocumentId]);
  const selected = filtered.find((hit) => hit.documentId === selectedDocumentId) ?? filtered[0] ?? null;
  useEffect(() => setSelectedChunkId(null), [evidence, selectedDocumentId]);
  const excerpt = selected?.excerpts.find((item) => item.chunkId === selectedChunkId) ?? selected?.excerpts[0] ?? null;
  const pinned = excerpt ? pinnedCitationIds.includes(excerpt.citation.citationId) : false;
  const working = retrieval.phase === "searching" || retrieval.phase === "partial";

  return (
    <aside className="evidence-rail" aria-label="检索证据" aria-busy={working}>
      <div className="rail-header">
        <div><h2>检索证据</h2><span className={working ? "rail-status is-working" : "rail-status"}>{working ? <LoaderCircle className="spin" size={13} /> : null}{evidence ? `${working ? "检索中" : "检索完成"} · ${evidence.hits.length} 份来源` : retrieval.phase === "searching" ? "正在检索" : "等待检索"}</span></div>
        {onClose ? <button className="icon-button rail-close" type="button" onClick={onClose} aria-label="关闭证据栏"><X size={18} /></button> : null}
      </div>
      {evidence ? (
        <>
          <div className="query-box"><Search size={15} /><span>{evidence.query}</span></div>
          <div className="evidence-tabs" role="tablist" aria-label="证据来源">
            <button className={kind === "all" ? "is-active" : ""} type="button" onClick={() => setKind("all")}>全部</button>
            <button className={kind === "design" ? "is-active" : ""} type="button" onClick={() => setKind("design")}>策划案</button>
            <button className={kind === "table" ? "is-active" : ""} type="button" onClick={() => setKind("table")}>配表</button>
          </div>
          <div className="evidence-results">
            {filtered.map((hit) => <EvidenceRow key={hit.documentId} hit={hit} selected={hit.documentId === selected?.documentId} onSelect={() => onSelectDocument(hit.documentId)} />)}
            {filtered.length === 0 ? <p className="rail-empty">该来源没有命中</p> : null}
          </div>
          {selected && excerpt ? (
            <section className="evidence-detail">
              <div className="detail-title-row">
                <div><h3>{selected.title}</h3><span>{selected.sourceLabel} · {selected.extension.toUpperCase()}</span></div>
                <div className="detail-actions">
                  <button className={pinned ? "context-button is-added" : "context-button"} type="button" onClick={() => onToggleCitation(excerpt.citation.citationId)}>
                    {pinned ? <Check size={14} /> : <BookOpenCheck size={14} />}{pinned ? "已加入" : "加入上下文"}
                  </button>
                  <button className="open-source-button" type="button" onClick={() => onOpenCitation(excerpt.citation.citationId)}><ExternalLink size={14} />打开源文件</button>
                </div>
              </div>
              <p className="citation-path" title={selected.absolutePath}>{selected.relativePath}</p>
              <div className="detail-metadata"><span>有效日期 {dateLabel(selected.effectiveUpdatedAt)}</span><span>{excerpt.locator}</span></div>
              <div className="excerpt-box">
                <h4>{excerpt.headingPath.at(-1) ?? SECTION_LABELS[excerpt.sectionType]}</h4>
                <p>{excerpt.text}</p>
              </div>
              {selected.excerpts.length > 1 ? (
                <div className="more-excerpts" aria-label="同文件其他原文位置">{selected.excerpts.map((item) => <button className={item.chunkId === excerpt.chunkId ? "is-active" : ""} key={item.chunkId} type="button" onClick={() => setSelectedChunkId(item.chunkId)}>{item.headingPath.at(-1) ?? SECTION_LABELS[item.sectionType]} · {item.locator}</button>)}</div>
              ) : null}
            </section>
          ) : null}
        </>
      ) : (
        <div className="rail-empty-state">{retrieval.phase === "searching" ? <LoaderCircle className="spin" size={26} /> : <Search size={24} />}<h3>{retrieval.phase === "searching" ? "正在检索知识库" : "还没有检索证据"}</h3><p>{retrieval.phase === "searching" ? "首批来源返回后会立即显示，不必等待回答生成完成。" : "发送问题后，这里会显示按新到旧排列的策划案、配表和精确引用。"}</p></div>
      )}
    </aside>
  );
}
