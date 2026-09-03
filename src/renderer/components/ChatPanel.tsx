import { useEffect, useMemo, useRef, useState } from "react";
import { AlertCircle, BookOpen, Check, CircleCheck, Copy, Database, LoaderCircle, Search, Send, Square, UserRound } from "lucide-react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import type { ChatCitation, ChatMessage, RetrievalActivity, SearchResponse } from "../../shared/contracts.js";
import { bindCitationLinks, resolveMessageUrl } from "../citation-links.js";

interface ChatPanelProps {
  messages: ChatMessage[];
  evidence: SearchResponse | null;
  retrieval: RetrievalActivity;
  pinnedCitationIds: string[];
  onOpenCitation: (citationId: string) => void;
  onSend: (text: string) => Promise<void>;
  onStop: () => Promise<void>;
}

function RetrievalTrail({ activity, evidence }: { activity: RetrievalActivity; evidence: SearchResponse | null }) {
  if (activity.phase === "idle") return null;
  const working = activity.phase === "searching" || activity.phase === "partial";
  const failed = activity.phase === "error";
  const count = evidence?.hits.length ?? activity.foundCount;
  return (
    <div className={`retrieval-trail is-${activity.phase}`} aria-live="polite">
      {working ? <LoaderCircle className="spin" size={16} /> : failed ? <AlertCircle size={16} /> : <CircleCheck size={16} />}
      <div><strong>{activity.message}</strong><span>{count > 0 ? `已显示 ${count} 份来源${working ? "，可先查看当前证据" : ""}` : "正在搜索已配置的策划案与配表"}</span></div>
    </div>
  );
}

function evidenceCitationMap(evidence: SearchResponse | null): Map<string, ChatCitation> {
  const map = new Map<string, ChatCitation>();
  for (const hit of evidence?.hits ?? []) {
    for (const excerpt of hit.excerpts) {
      map.set(excerpt.citation.citationId, {
        citationId: excerpt.citation.citationId,
        label: `${hit.title} · ${excerpt.locator}`,
        title: hit.title,
        relativePath: hit.relativePath,
        absolutePath: hit.absolutePath,
        locator: excerpt.locator,
        sourceKind: hit.sourceKind,
      });
    }
  }
  return map;
}

function MessageBody({ message, fallbackCitations, onOpenCitation }: { message: ChatMessage; fallbackCitations: Map<string, ChatCitation>; onOpenCitation: (id: string) => void }) {
  const [copied, setCopied] = useState(false);
  const citationLookup = useMemo(() => {
    const merged = new Map(fallbackCitations);
    for (const citation of message.citations ?? []) merged.set(citation.citationId, citation);
    return merged;
  }, [fallbackCitations, message.citations]);
  const copy = async () => {
    await navigator.clipboard.writeText(message.text);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1_500);
  };
  if (message.role === "user") {
    return (
      <article className="message message-user">
        <span className="message-avatar user-avatar"><UserRound size={18} /></span>
        <div className="message-user-bubble">{message.text}</div>
      </article>
    );
  }
  return (
    <article className={`message message-assistant${message.status === "error" ? " is-error" : ""}`}>
      <span className="message-avatar assistant-avatar">d</span>
      <div className="assistant-content">
        <ReactMarkdown
          remarkPlugins={[remarkGfm]}
          skipHtml
          urlTransform={(url) => resolveMessageUrl(url, citationLookup)}
          components={{
            a: ({ href, children }) => href?.startsWith("drag-citation:") ? (
              <button className="citation-link" type="button" onClick={() => onOpenCitation(href.slice("drag-citation:".length))} title={citationLookup.get(href.slice("drag-citation:".length))?.relativePath}>
                <BookOpen size={13} />{children}
              </button>
            ) : <a href={href} target="_blank" rel="noreferrer">{children}</a>,
          }}
        >
          {bindCitationLinks(message.text || (message.status === "streaming" ? "正在分析检索证据…" : ""), citationLookup)}
        </ReactMarkdown>
        {message.status !== "streaming" ? (
          <div className="message-actions">
            <button type="button" onClick={copy}>{copied ? <Check size={15} /> : <Copy size={15} />} {copied ? "已复制" : "复制"}</button>
            {(message.citations?.length ?? 0) > 0 ? <span>{message.citations?.length} 条可核对来源</span> : null}
          </div>
        ) : <span className="streaming-caret" aria-label="正在生成" />}
      </div>
    </article>
  );
}

export function ChatPanel({ messages, evidence, retrieval, pinnedCitationIds, onOpenCitation, onSend, onStop }: ChatPanelProps) {
  const [value, setValue] = useState("");
  const scrollRef = useRef<HTMLDivElement>(null);
  const streaming = messages.some((message) => message.status === "streaming");
  const lastMessage = messages.at(-1);
  const fallbackCitations = useMemo(() => evidenceCitationMap(evidence), [evidence]);

  useEffect(() => {
    const frame = window.requestAnimationFrame(() => {
      const scroll = scrollRef.current;
      if (!scroll) return;
      scroll.scrollTo({ top: scroll.scrollHeight, behavior: lastMessage?.status === "streaming" ? "auto" : "smooth" });
    });
    return () => window.cancelAnimationFrame(frame);
  }, [lastMessage?.id, lastMessage?.status, lastMessage?.text]);

  const submit = async () => {
    const next = value.trim();
    if (!next || streaming) return;
    setValue("");
    await onSend(next);
  };

  return (
    <main className="chat-panel">
      <div className="chat-scroll" ref={scrollRef}>
        {messages.length === 0 ? (
          <section className="chat-empty">
            <span className="empty-mark">d</span>
            <h2>从历史策划中找到可复用依据</h2>
            <p>输入活动、玩法、流程、配置或历史改动。检索在本机完成，结果默认按业务日期从新到旧。</p>
            <div className="suggestion-list">
              {["找到最新的一个 888活动，说明一下里面的玩法和产出逻辑", "我要新增一个扭蛋机，需要配置哪些表格", "我想复用轮盘抽奖活动，有哪些方案？"].map((suggestion) => (
                <button key={suggestion} type="button" onClick={() => setValue(suggestion)}>{suggestion}</button>
              ))}
            </div>
          </section>
        ) : (
          <div className="message-stack">
            {messages.map((message, index) => (
              <div key={message.id}>
                <MessageBody message={message} fallbackCitations={fallbackCitations} onOpenCitation={onOpenCitation} />
                {message.role === "user" && index === messages.findLastIndex((item) => item.role === "user") ? <RetrievalTrail activity={retrieval} evidence={evidence} /> : null}
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="composer-wrap">
        <div className={`composer${streaming ? " is-streaming" : ""}`}>
          <textarea value={value} onChange={(event) => setValue(event.target.value)} onInput={(event) => { const target = event.currentTarget; target.style.height = "auto"; target.style.height = `${Math.min(target.scrollHeight, 180)}px`; }} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); void submit(); } }} placeholder="输入你的问题，Enter 发送；Shift + Enter 换行" aria-label="输入你的问题" rows={2} />
          <div className="composer-tools">
            <div className="composer-left-tools">
              <span className="composer-status"><Database size={16} />全部资料</span>
              <span className={pinnedCitationIds.length ? "composer-status has-context" : "composer-status"}><BookOpen size={16} />{pinnedCitationIds.length ? `已选 ${pinnedCitationIds.length} 条来源` : "未固定来源"}</span>
            </div>
            {streaming ? (
              <button className="send-button stop-button" type="button" onClick={() => void onStop()}><Square size={14} fill="currentColor" />停止</button>
            ) : (
              <button className="send-button" type="button" disabled={!value.trim()} onClick={() => void submit()}><Send size={17} />发送</button>
            )}
          </div>
        </div>
        <p className="model-disclosure"><Search size={13} />索引与检索保存在本机；发送消息时，命中证据会交给 ChatGPT 分析。</p>
      </div>
    </main>
  );
}
