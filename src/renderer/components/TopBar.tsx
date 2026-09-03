import { useEffect, useMemo, useRef, useState } from "react";
import { Check, ChevronDown, Menu, PanelRight, UserRound } from "lucide-react";
import type { AccountStatus, ModelOption } from "../../shared/contracts.js";

interface TopBarProps {
  title: string;
  account: AccountStatus;
  models: ModelOption[];
  model: string | null;
  reasoning: string | null;
  onLogin: () => void;
  onPreferencesChange: (model: string | null, reasoningEffort: string | null) => void;
  onToggleSidebar: () => void;
  onToggleEvidence: () => void;
}

const REASONING_LABELS: Record<string, string> = {
  none: "无额外推理",
  minimal: "最低",
  low: "低",
  medium: "中等",
  high: "高",
  xhigh: "很高",
  max: "最高",
  ultra: "Ultra",
};

export function TopBar({ title, account, models, model, reasoning, onLogin, onPreferencesChange, onToggleSidebar, onToggleEvidence }: TopBarProps) {
  const [openMenu, setOpenMenu] = useState<"model" | "reasoning" | null>(null);
  const controlsRef = useRef<HTMLDivElement>(null);
  const defaultModel = useMemo(() => models.find((item) => item.isDefault) ?? models[0] ?? null, [models]);
  const selectedModel = useMemo(() => model ? models.find((item) => item.model === model) ?? null : defaultModel, [defaultModel, model, models]);
  const reasoningOptions = selectedModel?.supportedReasoningEfforts ?? [];
  const selectedReasoning = reasoning ?? selectedModel?.defaultReasoningEffort ?? null;

  useEffect(() => {
    if (!openMenu) return;
    const close = (event: PointerEvent) => {
      if (!controlsRef.current?.contains(event.target as Node)) setOpenMenu(null);
    };
    const closeOnEscape = (event: KeyboardEvent) => { if (event.key === "Escape") setOpenMenu(null); };
    document.addEventListener("pointerdown", close);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      document.removeEventListener("pointerdown", close);
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, [openMenu]);

  const chooseModel = (next: ModelOption | null) => {
    const nextModel = next ?? defaultModel;
    const supported = nextModel?.supportedReasoningEfforts ?? [];
    const nextReasoning = reasoning && supported.some((item) => item.value === reasoning)
      ? reasoning
      : nextModel?.defaultReasoningEffort ?? null;
    onPreferencesChange(next?.model ?? null, nextReasoning);
    setOpenMenu(null);
  };

  return (
    <header className="topbar">
      <button className="mobile-icon-button sidebar-toggle" type="button" onClick={onToggleSidebar} aria-label="打开会话栏">
        <Menu size={19} />
      </button>
      <div className="topbar-title"><h1>{title}</h1></div>
      <div className="topbar-controls" ref={controlsRef}>
        <div className="topbar-menu-wrap">
          <button className="topbar-select" type="button" aria-haspopup="menu" aria-expanded={openMenu === "model"} disabled={models.length === 0} onClick={() => setOpenMenu((value) => value === "model" ? null : "model")}>
            <span>{selectedModel?.displayName ?? model ?? "Codex 默认模型"}</span>
            <ChevronDown size={14} aria-hidden="true" />
          </button>
          {openMenu === "model" ? (
            <div className="topbar-popover model-popover" role="menu" aria-label="选择模型">
              <button className={model === null ? "is-selected" : ""} type="button" role="menuitemradio" aria-checked={model === null} onClick={() => chooseModel(null)}>
                <span><strong>Codex 默认</strong><small>{defaultModel ? `当前为 ${defaultModel.displayName}` : "跟随登录账号"}</small></span>
                {model === null ? <Check size={16} /> : null}
              </button>
              {models.map((item) => (
                <button className={model === item.model ? "is-selected" : ""} key={item.id} type="button" role="menuitemradio" aria-checked={model === item.model} onClick={() => chooseModel(item)}>
                  <span><strong>{item.displayName}</strong><small>{item.description || item.model}</small></span>
                  {model === item.model ? <Check size={16} /> : null}
                </button>
              ))}
            </div>
          ) : null}
        </div>

        <div className="topbar-menu-wrap reasoning-control">
          <button className="topbar-select" type="button" aria-haspopup="menu" aria-expanded={openMenu === "reasoning"} disabled={reasoningOptions.length === 0} onClick={() => setOpenMenu((value) => value === "reasoning" ? null : "reasoning")}>
            <span>{selectedReasoning ? REASONING_LABELS[selectedReasoning] ?? selectedReasoning : "默认推理"}</span>
            <ChevronDown size={14} aria-hidden="true" />
          </button>
          {openMenu === "reasoning" ? (
            <div className="topbar-popover reasoning-popover" role="menu" aria-label="选择推理等级">
              {reasoningOptions.map((item) => (
                <button className={selectedReasoning === item.value ? "is-selected" : ""} key={item.value} type="button" role="menuitemradio" aria-checked={selectedReasoning === item.value} onClick={() => { onPreferencesChange(model, item.value); setOpenMenu(null); }}>
                  <span><strong>{REASONING_LABELS[item.value] ?? item.value}</strong><small>{item.description}</small></span>
                  {selectedReasoning === item.value ? <Check size={16} /> : null}
                </button>
              ))}
            </div>
          ) : null}
        </div>

        {account.connected ? (
          <div className="account-status" title={account.codexVersion ?? undefined}>
            <span className="connection-dot" />
            <span>{`ChatGPT ${account.planType ?? ""}`.trim()}</span>
            <UserRound size={17} strokeWidth={1.8} aria-hidden="true" />
          </div>
        ) : (
          <button className="account-button" type="button" onClick={onLogin} title={account.error ?? undefined}>
            <span>登录 ChatGPT</span><UserRound size={17} strokeWidth={1.8} />
          </button>
        )}
        <button className="mobile-icon-button evidence-toggle" type="button" onClick={onToggleEvidence} aria-label="打开检索证据">
          <PanelRight size={19} />
        </button>
      </div>
    </header>
  );
}
