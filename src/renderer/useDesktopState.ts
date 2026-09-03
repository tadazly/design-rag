import { startTransition, useCallback, useEffect, useMemo, useState } from "react";
import type { AppEvent, AppNotice, AppSnapshot, DragDesktopApi } from "../shared/contracts.js";
import { createDemoApi } from "./demo.js";

function resolveApi(): DragDesktopApi {
  if (window.drag) return window.drag;
  if (import.meta.env.DEV && new URLSearchParams(window.location.search).get("preview") === "1") return createDemoApi();
  throw new Error("桌面桥接不可用。请在 Electron 中运行，或在开发环境使用 ?preview=1 做视觉预览。");
}

function reduceEvent(previous: AppSnapshot, event: AppEvent): AppSnapshot {
  switch (event.type) {
    case "snapshot":
      return event.snapshot;
    case "index-progress":
      return { ...previous, index: { ...previous.index, activeRun: event.run } };
    case "account":
      return { ...previous, account: event.account };
    case "threads":
      return { ...previous, threads: event.threads, activeThreadId: event.activeThreadId };
    case "messages":
      return { ...previous, messages: event.messages };
    case "evidence":
      return { ...previous, evidence: event.evidence };
    case "retrieval":
      return { ...previous, retrieval: event.retrieval };
    case "notice":
      return previous;
    case "error":
      return previous;
  }
}

export function useDesktopState() {
  const api = useMemo(resolveApi, []);
  const [snapshot, setSnapshot] = useState<AppSnapshot | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [eventNotice, setEventNotice] = useState<AppNotice | null>(null);

  useEffect(() => {
    let disposed = false;
    api.getSnapshot()
      .then((value) => { if (!disposed) setSnapshot(value); })
      .catch((reason) => { if (!disposed) setError(reason instanceof Error ? reason.message : String(reason)); });
    const unsubscribe = api.subscribe((event) => {
      startTransition(() => setSnapshot((previous) => previous ? reduceEvent(previous, event) : event.type === "snapshot" ? event.snapshot : previous));
      if (event.type === "error") setError(event.message);
      if (event.type === "notice") setEventNotice(event.notice);
    });
    return () => {
      disposed = true;
      unsubscribe();
    };
  }, [api]);

  const run = useCallback(async <T,>(operation: () => Promise<T>): Promise<T | undefined> => {
    setError(null);
    try {
      return await operation();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason));
      return undefined;
    }
  }, []);

  return { api, snapshot, setSnapshot, error, setError, eventNotice, setEventNotice, run };
}
