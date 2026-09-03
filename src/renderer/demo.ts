import type {
  AppEvent,
  AppSnapshot,
  SearchRequest,
  DragDesktopApi,
} from "../shared/contracts.js";

const now = Date.now();

export const demoSnapshot: AppSnapshot = {
  config: {
    schemaVersion: 1,
    sources: [
      {
        id: "plans",
        label: "策划案",
        kind: "design",
        rootPath: "D:\\DesignRag\\examples\\design-docs",
        enabled: true,
        includeExtensions: [".docx", ".xlsx", ".xlsm", ".xls", ".pdf", ".xmind", ".md", ".txt"],
        excludeDirectoryNames: [".svn", ".git", "node_modules", "tmp"],
        maxFileBytes: 134_217_728,
      },
      {
        id: "tables",
        label: "配表",
        kind: "table",
        rootPath: "D:\\DesignRag\\examples\\config-tables",
        enabled: true,
        includeExtensions: [".xlsx", ".xlsm", ".xls", ".csv"],
        excludeDirectoryNames: [".git", "node_modules", "tmp"],
        maxFileBytes: 134_217_728,
      },
    ],
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
    indexing: { automaticScan: true, scanIntervalMinutes: 10, concurrency: 16 },
    codex: { codexPath: null, model: "gpt-5.6-terra", reasoningEffort: "medium" },
  },
  account: { connected: true, authMode: "chatgpt", planType: "pro", codexVersion: "codex-cli 0.151.0", error: null },
  index: {
    databasePath: "C:\\Users\\demo\\AppData\\Local\\DesignRag\\index.sqlite",
    configPath: "C:\\Users\\demo\\AppData\\Roaming\\DesignRag\\config.json",
    indexRevision: 42,
    fts5Available: true,
    trigramAvailable: true,
    documentCount: 128,
    chunkCount: 1_462,
    staleCount: 0,
    sourceCounts: { plans: 96, tables: 32 },
    activeRun: null,
    lastRun: {
      runId: "demo-run-baseline",
      phase: "complete",
      startedAt: "2026-08-31T13:42:49.545Z",
      finishedAt: "2026-08-31T13:52:39.667Z",
      discovered: 128,
      indexed: 128,
      unchanged: 0,
      skipped: 0,
      failed: 0,
      deleted: 0,
      currentPath: null,
      error: null,
    },
    recentIssues: [
      {
        path: "D:\\DesignRag\\examples\\design-docs\\sample-empty.txt",
        sourceId: "plans",
        code: "extract_failed",
        message: "文档未提取到可索引内容",
        occurredAt: "2026-08-31T13:52:12.000Z",
      },
    ],
  },
  threads: [
    { id: "thread-wheel", title: "复用星港庆典活动", preview: "优先比较最近复用版本", createdAt: now - 2_000_000, updatedAt: now - 20_000, active: true, archived: false },
    { id: "thread-signin", title: "签到活动历史改动", preview: "整理登录奖励变更", createdAt: now - 9_000_000, updatedAt: now - 2_400_000, active: false, archived: false },
    { id: "thread-pet", title: "宠物养成配置", preview: "配表字段与模块关系", createdAt: now - 12_000_000, updatedAt: now - 6_000_000, active: false, archived: false },
    { id: "thread-flow", title: "限时聚充活动复用方案", preview: "活动流程与奖励结构", createdAt: now - 70_000_000, updatedAt: now - 68_000_000, active: false, archived: false },
    { id: "thread-new", title: "新手七日目标拆解", preview: "版本差异", createdAt: now - 170_000_000, updatedAt: now - 169_000_000, active: false, archived: false },
  ],
  activeThreadId: "thread-wheel",
  messages: [
    {
      id: "demo-user",
      role: "user",
      text: "我想复用星港庆典抽奖活动，帮我找一下有哪些可用方案",
      createdAt: now - 60_000,
      status: "complete",
      citationIds: [],
    },
    {
      id: "demo-assistant",
      role: "assistant",
      text: `建议优先复用 **星港庆典轮盘** 这条示例策划线。最新明确标记“复用”的版本是 2026-08-19，结构覆盖概述、版本记录、面板逻辑、奖励数值、配置明细和美术需求。[示例_【复用】星港庆典_星灵_20260819.xlsx](<D:/DesignRag/examples/design-docs/versions/2026.08.19/示例_【复用】星港庆典_星灵_20260819.xlsx>) · \`版本修改记录!A1:D18\`

可复用候选（按业务日期从新到旧）：

1. **2026-08-19 · 星港庆典_星灵**：当前最新复用版，适合作为整体框架。
2. **2026-08-05 · 星港庆典_星灵+外观**：奖励形态更接近多资源组合，可抽取奖励配置差异。[[DRAG:demo-wheel-0805]]
3. **2026-07-22 · 星港庆典_星灵**：可用于核对 8 月版本前的面板与逻辑基线。[[DRAG:demo-wheel-0722]]

同日的“暑期勋章兑好礼+转盘”属于相关改造方案，但没有明确复用标签，建议单列比较，避免误并入同一版本链。

下一步我可以继续整理：版本演进差异，或 **lotteryConfig / rewardPool / featureModule** 的配表字段映射。`,
      createdAt: now - 20_000,
      status: "complete",
      citationIds: ["DRAG:demo-wheel-0819", "DRAG:demo-wheel-0805", "DRAG:demo-wheel-0722"],
      citations: [
        { citationId: "DRAG:demo-wheel-0819", label: "示例_【复用】星港庆典_星灵_20260819 · 版本修改记录!A1:D18", title: "示例_【复用】星港庆典_星灵_20260819", relativePath: "versions\\2026.08.19\\示例_【复用】星港庆典_星灵_20260819.xlsx", absolutePath: "D:\\DesignRag\\examples\\design-docs\\versions\\2026.08.19\\示例_【复用】星港庆典_星灵_20260819.xlsx", locator: "版本修改记录!A1:D18", sourceKind: "design" },
        { citationId: "DRAG:demo-wheel-0805", label: "示例_【复用】星港庆典_星灵+外观_20260805 · 版本修改记录!A1:D16", title: "示例_【复用】星港庆典_星灵+外观_20260805", relativePath: "versions\\2026.08.05\\示例_【复用】星港庆典_星灵+外观_20260805.xlsx", absolutePath: "D:\\DesignRag\\examples\\design-docs\\versions\\2026.08.05\\示例_【复用】星港庆典_星灵+外观_20260805.xlsx", locator: "版本修改记录!A1:D16", sourceKind: "design" },
        { citationId: "DRAG:demo-wheel-0722", label: "示例_【复用】星港庆典_星灵_20260722 · 版本修改记录!A1:D16", title: "示例_【复用】星港庆典_星灵_20260722", relativePath: "versions\\2026.07.22\\示例_【复用】星港庆典_星灵_20260722.xlsx", absolutePath: "D:\\DesignRag\\examples\\design-docs\\versions\\2026.07.22\\示例_【复用】星港庆典_星灵_20260722.xlsx", locator: "版本修改记录!A1:D16", sourceKind: "design" },
      ],
    },
  ],
  evidence: {
    query: "我想复用星港庆典抽奖活动，帮我找一下有哪些可用方案",
    expandedTerms: ["星港", "庆典", "抽奖", "复用", "轮盘"],
    requestedMode: "auto",
    actualMode: "lexical",
    semanticUsed: false,
    semanticCoverage: 0,
    sort: "newest",
    indexRevision: 147,
    totalCandidates: 235,
    tookMs: 84.6,
    warnings: [],
    hits: [
      {
        documentId: "demo-doc-0819",
        sourceId: "plans",
        sourceLabel: "策划案",
        sourceKind: "design",
        title: "示例_【复用】星港庆典_星灵_20260819",
        absolutePath: "D:\\DesignRag\\examples\\design-docs\\versions\\2026.08.19\\示例_【复用】星港庆典_星灵_20260819.xlsx",
        relativePath: "versions\\2026.08.19\\示例_【复用】星港庆典_星灵_20260819.xlsx",
        extension: ".xlsx",
        effectiveUpdatedAt: "2026-08-19T00:00:00.000Z",
        dateSource: "filename",
        filesystemModifiedAt: "2026-08-04T10:10:00.000Z",
        relevance: 0.96,
        familyKey: "示例 星港庆典 星灵",
        familyConfidence: 0.88,
        stale: false,
        sectionTypes: ["overview", "version_history", "panel_logic", "reward_value", "config", "art_requirement"],
        excerpts: [
          {
            chunkId: "demo-wheel-0819",
            sectionType: "version_history",
            headingPath: ["版本修改记录"],
            locator: "版本修改记录!A1:D18",
            text: "版本记录从 20260311 初版延续到 20260819，多次标记为复用并更新精灵、奖励及活动表现。",
            highlightedText: "版本记录从 20260311 初版延续到 **20260819**，多次标记为**复用**并更新精灵、奖励及活动表现。",
            score: 0.96,
            citation: {
              citationId: "DRAG:demo-wheel-0819",
              display: "星港庆典 · 版本修改记录 · A1:D18",
              sourceId: "plans",
              sourceLabel: "策划案",
              sourceKind: "design",
              absolutePath: "D:\\DesignRag\\examples\\design-docs\\versions\\2026.08.19\\示例_【复用】星港庆典_星灵_20260819.xlsx",
              relativePath: "versions\\2026.08.19\\示例_【复用】星港庆典_星灵_20260819.xlsx",
              documentId: "demo-doc-0819",
              chunkId: "demo-wheel-0819",
              locator: "版本修改记录!A1:D18",
              headingPath: ["版本修改记录"],
              indexedContentHash: "demo0819",
              indexRevision: 147,
              stale: false,
            },
          },
        ],
      },
      ...[
        ["0805", "2026-08-05", "示例_【复用】星港庆典_星灵+外观_20260805", 0.91],
        ["0722", "2026-07-22", "示例_【复用】星港庆典_星灵_20260722", 0.86],
        ["0708", "2026-07-08", "示例_【复用】星港庆典_星灵+外观_20260708", 0.82],
      ].map(([suffix, date, title, relevance]) => ({
        documentId: `demo-doc-${suffix}`,
        sourceId: "plans",
        sourceLabel: "策划案",
        sourceKind: "design" as const,
        title: String(title),
        absolutePath: `D:\\DesignRag\\examples\\design-docs\\versions\\${String(date).replaceAll("-", ".")}\\${title}.xlsx`,
        relativePath: `versions\\${String(date).replaceAll("-", ".")}\\${title}.xlsx`,
        extension: ".xlsx",
        effectiveUpdatedAt: `${date}T00:00:00.000Z`,
        dateSource: "filename" as const,
        filesystemModifiedAt: "2026-08-04T10:10:00.000Z",
        relevance: Number(relevance),
        familyKey: "示例 星港庆典 星灵",
        familyConfidence: 0.88,
        stale: false,
        sectionTypes: ["version_history", "panel_logic", "config"] as import("../shared/contracts.js").SectionType[],
        excerpts: [{
          chunkId: `demo-wheel-${suffix}`,
          sectionType: "version_history" as const,
          headingPath: ["版本修改记录"],
          locator: "版本修改记录!A1:D16",
          text: "历史复用版本，包含面板逻辑、奖励数值与配置表明细。",
          highlightedText: "历史**复用**版本，包含面板逻辑、奖励数值与配置表明细。",
          score: Number(relevance),
          citation: {
            citationId: `DRAG:demo-wheel-${suffix}`,
            display: `${title} · 版本修改记录`,
            sourceId: "plans",
            sourceLabel: "策划案",
            sourceKind: "design" as const,
            absolutePath: `D:\\DesignRag\\examples\\design-docs\\${title}.xlsx`,
            relativePath: `${title}.xlsx`,
            documentId: `demo-doc-${suffix}`,
            chunkId: `demo-wheel-${suffix}`,
            locator: "版本修改记录!A1:D16",
            headingPath: ["版本修改记录"],
            indexedContentHash: `demo${suffix}`,
            indexRevision: 147,
            stale: false,
          },
        }],
      })),
    ],
  },
  retrieval: { phase: "complete", query: "我想复用星港庆典抽奖活动，帮我找一下有哪些可用方案", message: "检索与回答已完成", foundCount: 4, startedAt: now - 60_000 },
  models: [
    {
      id: "gpt-5.6-sol",
      model: "gpt-5.6-sol",
      displayName: "GPT-5.6 Sol",
      description: "旗舰能力，适合复杂策划分析",
      hidden: false,
      isDefault: false,
      defaultReasoningEffort: "medium",
      supportedReasoningEfforts: ["low", "medium", "high", "xhigh", "max"].map((value) => ({ value, description: `${value} 推理强度` })),
    },
    {
      id: "gpt-5.6-terra",
      model: "gpt-5.6-terra",
      displayName: "GPT-5.6 Terra",
      description: "平衡分析质量与响应速度",
      hidden: false,
      isDefault: true,
      defaultReasoningEffort: "medium",
      supportedReasoningEfforts: ["low", "medium", "high", "xhigh", "max"].map((value) => ({ value, description: `${value} 推理强度` })),
    },
    {
      id: "gpt-5.6-luna",
      model: "gpt-5.6-luna",
      displayName: "GPT-5.6 Luna",
      description: "更快的日常检索问答",
      hidden: false,
      isDefault: false,
      defaultReasoningEffort: "medium",
      supportedReasoningEfforts: ["low", "medium", "high", "xhigh", "max"].map((value) => ({ value, description: `${value} 推理强度` })),
    },
  ],
  activeView: "chat",
};

export function createDemoApi(initial = demoSnapshot): DragDesktopApi {
  let snapshot = structuredClone(initial);
  const fixtureEvidence = structuredClone(initial.evidence);
  const listeners = new Set<(event: AppEvent) => void>();
  const emit = (event: AppEvent) => listeners.forEach((listener) => listener(event));
  return {
    getSnapshot: async () => structuredClone(snapshot),
    setActiveView: async (view) => {
      snapshot.activeView = view;
      emit({ type: "snapshot", snapshot: structuredClone(snapshot) });
    },
    saveConfig: async (config) => {
      const previousSources = snapshot.config.sources;
      const added = config.sources.filter((source) => !previousSources.some((item) => item.id === source.id));
      const removed = previousSources.filter((source) => !config.sources.some((item) => item.id === source.id));
      const disabled = config.sources.filter((source) => !source.enabled && previousSources.some((item) => item.id === source.id && item.enabled));
      const enabled = config.sources.filter((source) => source.enabled && previousSources.some((item) => item.id === source.id && !item.enabled));
      const moved = config.sources.filter((source) => previousSources.some((item) => item.id === source.id && item.rootPath !== source.rootPath));
      snapshot.config = structuredClone(config);
      emit({ type: "snapshot", snapshot: structuredClone(snapshot) });
      const notice = added.length > 0 || moved.length > 0
        ? { kind: "index-updated" as const, title: "来源已保存并完成增量索引", message: "预览模式已完成来源配置与增量索引流程。" }
        : removed.length > 0
          ? { kind: "index-updated" as const, title: "来源及对应索引已删除", message: "仅移除该来源的本地索引；源文件未被修改。" }
          : disabled.length > 0
            ? { kind: "info" as const, title: "来源已停用", message: "已有索引缓存仍保留，但不会再进入搜索、引用和模型证据。" }
            : enabled.length > 0
              ? { kind: "info" as const, title: "来源已启用", message: "现有缓存已立即恢复检索，无需重建。" }
              : null;
      if (notice) emit({ type: "notice", notice: { id: `demo-notice-${Date.now()}`, createdAt: Date.now(), ...notice } });
      return config;
    },
    chooseSourceDirectory: async (sourceId) => snapshot.config.sources.find((source) => source.id === sourceId)?.rootPath ?? null,
    chooseDirectory: async () => "D:\\资料库\\新资料源",
    resolveDroppedPath: async (file) => {
      const name = file && typeof file === "object" && "name" in file && typeof file.name === "string" ? file.name : "拖入的资料源";
      return `D:\\资料库\\${name}`;
    },
    rebuildIndex: async () => {
      const base: NonNullable<AppSnapshot["index"]["lastRun"]> = {
        runId: `demo-run-${Date.now()}`,
        phase: "discover",
        startedAt: new Date().toISOString(),
        finishedAt: null,
        discovered: 0,
        indexed: 0,
        unchanged: 0,
        skipped: 0,
        failed: 0,
        deleted: 0,
        currentPath: snapshot.config.sources[0]?.rootPath ?? null,
        error: null,
      };
      snapshot.index.activeRun = base;
      emit({ type: "index-progress", run: structuredClone(base) });
      await new Promise((resolve) => setTimeout(resolve, 300));
      const extracting = {
        ...base,
        phase: "extract" as const,
        discovered: 1_112,
        indexed: 238,
        unchanged: 174,
        currentPath: "D:\\DesignRag\\examples\\design-docs\\versions\\2026.08.19\\示例_【复用】星港庆典_星灵_20260819.xlsx",
      };
      snapshot.index.activeRun = extracting;
      emit({ type: "index-progress", run: structuredClone(extracting) });
      await new Promise((resolve) => setTimeout(resolve, 3_000));
      const complete = {
        ...extracting,
        phase: "complete" as const,
        finishedAt: new Date().toISOString(),
        indexed: 397,
        unchanged: 714,
        currentPath: null,
      };
      snapshot.index.activeRun = null;
      snapshot.index.lastRun = complete;
      snapshot.index.indexRevision += 397;
      emit({ type: "snapshot", snapshot: structuredClone(snapshot) });
      emit({
        type: "notice",
        notice: {
          id: `demo-notice-${Date.now()}`,
          kind: "index-updated",
          title: "索引已自动更新",
          message: "新增或更新 397 份资料，714 份无变化。",
          createdAt: Date.now(),
        },
      });
      return complete;
    },
    pauseIndex: async () => {
      if (!snapshot.index.activeRun) return null;
      snapshot.index.activeRun = { ...snapshot.index.activeRun, phase: "paused" };
      emit({ type: "index-progress", run: structuredClone(snapshot.index.activeRun) });
      return structuredClone(snapshot.index.activeRun);
    },
    resumeIndex: async () => {
      if (!snapshot.index.activeRun) return null;
      snapshot.index.activeRun = { ...snapshot.index.activeRun, phase: "extract" };
      emit({ type: "index-progress", run: structuredClone(snapshot.index.activeRun) });
      return structuredClone(snapshot.index.activeRun);
    },
    clearIndexCache: async () => {
      snapshot.index = {
        ...snapshot.index,
        indexRevision: snapshot.index.indexRevision + 1,
        documentCount: 0,
        chunkCount: 0,
        staleCount: 0,
        sourceCounts: {},
        activeRun: null,
        lastRun: null,
        recentIssues: [],
      };
      snapshot.evidence = null;
      emit({ type: "snapshot", snapshot: structuredClone(snapshot) });
      return structuredClone(snapshot.index);
    },
    search: async (_request: SearchRequest) => snapshot.evidence as NonNullable<AppSnapshot["evidence"]>,
    createThread: async () => {
      const thread = { id: `demo-${Date.now()}`, title: "新对话", preview: "", createdAt: Date.now(), updatedAt: Date.now(), active: true, archived: false };
      snapshot.threads = snapshot.threads.map((item) => ({ ...item, active: false })).concat(thread);
      snapshot.activeThreadId = thread.id;
      snapshot.messages = [];
      emit({ type: "snapshot", snapshot: structuredClone(snapshot) });
      return thread;
    },
    selectThread: async (threadId) => {
      snapshot.threads = snapshot.threads.map((thread) => ({ ...thread, active: thread.id === threadId }));
      snapshot.activeThreadId = threadId;
      emit({ type: "snapshot", snapshot: structuredClone(snapshot) });
    },
    archiveThread: async (threadId) => {
      snapshot.threads = snapshot.threads.map((thread) => thread.id === threadId ? { ...thread, archived: true, active: false } : thread);
      const next = snapshot.threads.find((thread) => !thread.archived);
      if (next) {
        snapshot.activeThreadId = next.id;
        snapshot.threads = snapshot.threads.map((thread) => ({ ...thread, active: thread.id === next.id }));
      }
      emit({ type: "snapshot", snapshot: structuredClone(snapshot) });
    },
    restoreThread: async (threadId) => {
      snapshot.threads = snapshot.threads.map((thread) => thread.id === threadId ? { ...thread, archived: false } : thread);
      emit({ type: "snapshot", snapshot: structuredClone(snapshot) });
    },
    deleteThread: async (threadId) => {
      snapshot.threads = snapshot.threads.filter((thread) => thread.id !== threadId);
      const next = snapshot.threads.find((thread) => !thread.archived);
      if (next) {
        snapshot.activeThreadId = next.id;
        snapshot.threads = snapshot.threads.map((thread) => ({ ...thread, active: thread.id === next.id }));
      }
      emit({ type: "snapshot", snapshot: structuredClone(snapshot) });
    },
    setCodexPreferences: async ({ model, reasoningEffort }) => {
      snapshot.config.codex.model = model;
      snapshot.config.codex.reasoningEffort = reasoningEffort;
      emit({ type: "snapshot", snapshot: structuredClone(snapshot) });
      return structuredClone(snapshot.config);
    },
    sendMessage: async (text, _citationIds = []) => {
      const timestamp = Date.now();
      snapshot.messages = [
        ...snapshot.messages,
        { id: `demo-user-${timestamp}`, role: "user", text, createdAt: timestamp, status: "complete", citationIds: [] },
        { id: `demo-assistant-${timestamp}`, role: "assistant", text: "", createdAt: timestamp + 1, status: "streaming", citationIds: [] },
      ];
      snapshot.evidence = null;
      snapshot.retrieval = { phase: "searching", query: text, message: "正在检索策划案与配表", foundCount: 0, startedAt: timestamp };
      emit({ type: "messages", messages: structuredClone(snapshot.messages) });
      emit({ type: "evidence", evidence: null });
      emit({ type: "retrieval", retrieval: structuredClone(snapshot.retrieval) });
      await new Promise((resolve) => setTimeout(resolve, 350));
      snapshot.evidence = fixtureEvidence ? { ...structuredClone(fixtureEvidence), query: text, tookMs: 326.4 } : null;
      snapshot.retrieval = { phase: "partial", query: text, message: "基础检索已完成，回答期间可能继续补充来源", foundCount: snapshot.evidence?.hits.length ?? 0, startedAt: timestamp };
      emit({ type: "evidence", evidence: structuredClone(snapshot.evidence) });
      emit({ type: "retrieval", retrieval: structuredClone(snapshot.retrieval) });
      await new Promise((resolve) => setTimeout(resolve, 450));
      snapshot.messages = snapshot.messages.map((message) => message.id === `demo-assistant-${timestamp}` ? {
        ...message,
        text: "我已根据当前命中的策划案与配表整理答案。最新版本、玩法流程和配置位置都可以从右侧证据逐条核对。[[DRAG:demo-wheel-0819]]",
        status: "complete" as const,
        citationIds: ["DRAG:demo-wheel-0819"],
      } : message);
      snapshot.retrieval = { ...snapshot.retrieval, phase: "complete", message: "检索与回答已完成" };
      emit({ type: "messages", messages: structuredClone(snapshot.messages) });
      emit({ type: "retrieval", retrieval: structuredClone(snapshot.retrieval) });
    },
    stopTurn: async () => {
      snapshot.messages = snapshot.messages.map((message) => message.status === "streaming" ? { ...message, status: "complete" } : message);
      snapshot.retrieval = { ...snapshot.retrieval, phase: "complete", message: "已停止生成，当前证据已保留" };
      emit({ type: "messages", messages: structuredClone(snapshot.messages) });
      emit({ type: "retrieval", retrieval: structuredClone(snapshot.retrieval) });
    },
    loginWithChatGPT: async () => ({ authUrl: "https://chatgpt.com" }),
    openCitation: async (citationId) => {
      const citation = snapshot.evidence?.hits.flatMap((hit) => hit.excerpts.map((excerpt) => excerpt.citation)).find((item) => item.citationId === citationId);
      return { opened: true, method: "excel-range", absolutePath: citation?.absolutePath ?? "", locator: citation?.locator ?? "", note: null };
    },
    subscribe: (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
  };
}
