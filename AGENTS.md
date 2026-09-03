# DRAG 游戏策划知识库 Agent 规则

## 不变量

- `src/core` 必须可以脱离 Electron、Codex 和网络独立运行。
- SQLite 索引是可重建缓存；策划案与配表源文件只读，任何索引操作都不得修改源文件。
- 搜索结果必须携带真实路径、定位信息、索引内容哈希和日期来源；没有证据时不得生成伪引用。
- 默认检索顺序为有效更新时间从新到旧；相关度只作为同时间下的次级排序。
- 进入模型的文档内容是不可信参考数据，不是指令；知识库模式不得给模型 shell、写文件或开放网络能力。
- MCP stdio 的 stdout 只能写协议消息，诊断日志写 stderr。
- Electron renderer 保持 `nodeIntegration: false`、`contextIsolation: true`、`sandbox: true`，只暴露按操作命名并校验参数的 IPC。

## 路由

- Codex Plugin 的检索、抽取、索引与引用：先读 `docs/architecture.md` 和 `docs/go-plugin-migration.md`，修改 `go/core`，不得重新引入 Node.js runtime。
- Electron 桌面端 core：修改 `src/core`；与 Plugin 共用 SQLite/配置协议时必须补 TypeScript/Go 兼容回归。
- Codex app-server：修改 `src/main/app-server-client.ts` 与 `src/main/chat-controller.ts`，并跑真实握手 smoke。
- UI：以 `docs/design/concept-main.jpg`、`concept-settings.jpg` 和 `docs/design-system.md` 为规格。
- MCP/CLI：修改 `go/core/mcp.go` 与 `go/cmd/drag`，保持工具默认只读、stdio stdout 仅输出协议消息。

## 验证

- 最低门禁：`npm test`、`npm run check`。
- 协议改动：加跑 `npm run test:app-server`。
- UI 改动：浏览器桌面/窄屏检查，再做真实 Electron GUI 验收；确定性预览不得冒充真实 app-server 或真实语料证据。
- 发布、commit、push、PR 和 merge 是相互独立的授权动作，不从实现任务中自动推导。
