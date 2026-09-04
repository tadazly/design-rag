# design-rag（drag）架构

## 目标与边界

产品分为两条运行链：单一纯 Go binary 驱动的 CLI/MCP/Codex Plugin，以及 TypeScript/Electron/React 桌面客户端。Plugin 技术 ID 为 `design-rag`、显示名为 `DRAG 游戏策划知识库`，Go CLI 名为 `drag`，桌面可执行文件名为 `drag-gui`。

`src/core` 是桌面客户端的 TypeScript compatibility core，不依赖 LLM、网络或 Electron；Plugin 的独立 CLI/MCP 则由 `go/core` 与 `go/cmd/drag` 提供。两条路径都把 SQLite 视为可重建缓存，策划案和配置表源文件始终只读。

```text
策划案 / 配置表（只读，可配置多个 design / table 来源）
        ↓
Go core：配置 / lease / 来源生命周期 / 格式抽取 / SQLite + FTS5
        ↓
Search / Retrieve / Citation / Versions
        ↙                         ↘
Go drag CLI / MCP              TypeScript desktop core
        ↓                         ↓
Codex Plugin                  Electron main → Codex app-server
```

Plugin 不包含 Electron，也不自行嵌入桌面 app-server。Codex 是 Plugin 的 host，直接启动随包 `drag` Go binary 的 MCP 模式；Plugin 的 CLI、MCP、配置、索引与检索路径都不需要 Node。桌面客户端仍由 Electron main 管理 Codex app-server、多 thread、证据栏和源文件打开，并可继续复用 `drag-core` JSONL indexing 接口。

## 数据与信任边界

- 源文件永远只读。索引、设置和会话元数据只写入 drag 的应用数据目录。
- Windows 新安装默认使用 `%APPDATA%\design-rag` 和 `%LOCALAPPDATA%\design-rag`。
- macOS 新安装默认使用 `~/Library/Application Support/design-rag/config` 和 `~/Library/Application Support/design-rag/data`。
- 新配置和数据目录使用 `DesignRag`；若当前目录不存在而上一版 `design-rag` 目录存在，继续读取上一版目录。
- `DESIGN_RAG_CONFIG_DIR` / `DESIGN_RAG_DATA_DIR` 可覆盖默认位置。
- 首次启动不预填任何资料目录；用户通过 GUI、CLI 或 MCP 添加本机来源。
- 索引在本机完成。用户发起 AI 对话时，受字符预算限制的命中片段才会发送给 ChatGPT 服务。
- 文档正文属于 `untrusted_reference_data`，不是指令。知识库模式不给模型 shell、文件写入或开放网络能力。

## 来源模型与生命周期

`AppConfig.sources` 是来源数组，不限制 `design` 或 `table` 的数量。类型来自来源配置，不根据文件内容自动猜测。索引记录持久化 `source_id` 与 `source_kind`；同格式文件使用相同 extractor。

配置校验保证：

- 来源 id 只使用小写字母、数字、下划线和连字符，并保持唯一。
- 用户可见名称不作为主键，允许多个非重叠目录使用同名；GUI 同时显示路径，索引与检索始终用唯一 `source_id` 和 `source_identity` 区分。
- 来源目录是绝对路径。
- 两个来源不能指向相同目录，也不能互为父子目录。
- 路径 realpath 校验可用时会进一步识别 symlink/junction 重叠；临时离线来源不会阻止其他设置保存。

来源变更由 `KnowledgeBaseService.reconcileSources` 统一处理，并在跨进程 mutation lease 内完成配置保存、索引和清理：

| 操作 | 索引行为 | 检索行为 |
|---|---|---|
| 添加来源 | 配置先原子落盘，只对新增且启用的来源执行 scoped 增量索引 | 配置立即生效；新内容在 scoped index 成功后可检索 |
| 停用来源 | 不删除 documents/chunks/FTS/hash | 立即从 search、retrieve、citation 和 versions 中屏蔽 |
| 重新启用 | identity 未变时复用缓存并 scoped 检查；停用期间 root/kind 已变时失效旧 identity 并强制重抽取 | scoped index 完成后恢复检索，无需 full rebuild |
| 修改名称/格式/排除规则 | 对受影响且启用的来源执行 scoped 增量 | 使用新配置 |
| 同 id 修改目录或类型 | 先将该 id 的旧缓存软标记 `deleted=1`，再扫描新配置；成功命中的记录会恢复 | 新目录不可访问时也不会把旧目录证据冒充为新来源 |
| 删除来源配置 | 精确硬清理该 `source_id` 的文档、chunk、FTS、embedding、问题记录和 source state | 配置移除后立即屏蔽，其他来源不受影响 |
| 删除全部缓存 | 清空全部可重建索引 | 来源配置、源文件和会话保留 |

只有“删除来源配置”会对单一来源执行硬清理。停用不等于删除；同 id 更换目录也不先做硬 purge。

配置文件始终是来源权威状态。停用只改变配置和检索资格，不改写 SQLite；停用期间修改 root/kind 也不触碰缓存，重新启用时才失效旧 identity 并 scoped 重抽取。同 id 更换 root/kind 后，读路径先以 enabled + current `source_identity` gate 拒绝旧证据，再由 reconciliation 将旧记录软标记 `deleted=1, stale=1`。配置删除后读路径立即屏蔽，随后精确 purge；若配置保存后的任一阶段崩溃，下一次普通 index 或桌面启动自动增量会依据 pending/source state 恢复索引并清理 orphan 来源。

检索入口会在调用方传入的 `sourceIds` 与当前启用来源之间求交集，并要求记录匹配当前 source identity，因此显式参数无法绕过停用或 identity 变更。引用回读和版本链执行相同 gate；恢复完成前不会泄漏旧 root/kind 证据。

SQLite schema v3 为每条文档保存 `source_identity = sha256(v1 + kind + canonical rootPath)`。FTS、trigram、canonical 去重、引用和版本链查询都同时约束 `source_id + source_identity`；因此即使进程在配置原子保存后、SQLite 协调前退出，旧 root/kind 也不会泄漏。停用来源不更新 `deleted`、`stale`、chunk、FTS 或 revision；停用期间变更 root/kind 也延后到重新启用时处理。

`source_index_state` 为 scoped 来源更新保存 `pending/ready`。新增、修改、重新启用以及每次选中扫描都会先写 pending；来源发现成功且全部候选均已处理（包括已结构化记录的单文件错误）后才写 ready。成功扫描的空目录会标记 ready，不会在后续相同配置操作中反复触发扫描；取消、中断或不可用来源则由下一次普通、scoped 或启动增量继续恢复。

## 跨进程一致性

CLI、MCP 与 GUI 可能同时打开同一配置和 SQLite。Go Plugin runtime 与 TypeScript desktop host 都使用数据目录中的同一 mutation lease，串行化索引、来源 reconciliation、配置保存、缓存清理和来源 purge：

- lease 包含 owner、操作名、heartbeat 与 TTL。
- 长任务定期 heartbeat；进程异常退出后，其他实例可在 lease 过期后恢复。
- 获取索引 lease 后重新加载磁盘配置，避免旧实例扫描已被另一个进程停用的来源。
- 每次索引前以当前配置协调 SQLite：只有配置中已删除的 source id 才硬 purge；root/kind 错配只对启用来源做软失效。
- scoped 索引会合并仍处于 pending 的启用来源，恢复上次中断的来源变更；删除大来源时固定批次清理 FTS rowid，避免一次性装载全部 chunk。
- 配置热重载同时比较 fingerprint 与内容 SHA-256；即使 mtime 恢复，也能识别外部配置变化。
- 配置文件以原子替换写入，落盘后立即成为读路径权威；长期 MCP 查询在开始和返回前复核配置，不读取半完成文件，也不会在跨进程停用后返回旧来源证据。

启动健康检查只做有界 data-table 与 FTS probe；`doctor` 和 corpus acceptance 再运行完整 integrity 检查。任一门禁确认活动 SQLite 已损坏时，host 不再尝试重命名 Windows 上可能仍被旧 MCP 占用的主库、WAL 和 SHM。恢复过程使用数据目录文件锁串行化，创建新的 generation 文件并通过原子 `index.active.json` 指针切换；旧缓存保持原样，新的 GUI、CLI 与 MCP 进程立即收敛到同一 generation，随后由正常全量索引重建内容。

## 索引模型

SQLite 使用 WAL、foreign keys、busy timeout 和批次事务。全量索引保持 `synchronous=NORMAL`，使用有界 WAL autocheckpoint，并在索引完成后执行 truncate checkpoint；仍有活跃 reader 时退化为 passive checkpoint，避免未合并 WAL 无限制增长。`documents` 保存规范化路径、文件哈希、mtime、嵌入日期和状态；`chunks` 保存 section、locator 与正文；FTS5 使用 contentless 表建立应用层 CJK token 词法索引，trigram 只覆盖标题、heading 与路径。

CLI 的 `config`、`doctor`、`sources list`、查询、引用、版本与状态命令使用 SQLite read-only connection，不执行 schema DDL 或来源状态初始化；来源变更、索引和缓存管理命令仍使用读写 connection。已完成 checkpoint、没有未合并 WAL frame 时，只读 connection 使用 SQLite immutable URI，避免 Windows sandbox 为 WAL 创建 SHM 的写权限要求；存在非空 WAL 时仍走正常 shared read-only 打开，绝不忽略已提交 frame。这样在只允许读取应用数据目录的 Codex sandbox 中可以安全诊断与检索，而不会把只读限制误报为数据库损坏。

Plugin 的文件发现、格式解析、日期解析、结构分块、SQLite 写入和检索都在同一无 CGO `drag` 进程中完成。桌面客户端仍可通过 `drag-core` 的 `protocolVersion=3` JSONL 接口调用同一 indexing core；stdout 只传协议消息。Go 使用固定 goroutine 池并行提取，单一批量 writer 写入 SQLite/FTS；默认并发数为 16，每个事务最多 256 份文档或 25,000 个 chunk，chunk SQL insert 再按最多 512 行执行。

DOCX、XLSX/XLSM、XMind 与文本格式由 Go 原生处理；旧 BIFF `.xls` 使用纯 Go BIFF8 reader，PDF 使用纯 Go page extractor，Go 原生 XLSX 返回 typed ZIP/structure failure 时只对该文件使用 Excelize compatibility backend。Plugin 不会请求 TypeScript/SheetJS fallback。桌面打包仍优先解析 `app.asar.unpacked/dist/native` 的 `drag-core`，避免把 asar 虚拟路径当作可执行文件。

普通文本 chunk 目标约 16,000 字符、重叠约 320 字符；XLSX 按工作表和大行组分块。FTS 仍逐 chunk 保留标题、heading、路径与正文可核验定位，trigram 只覆盖更稀疏的标题/heading/路径字段。完整重建使用可重建缓存适用的批量 SQLite 配置；每次运行记录 wall clock、发现/提取/收尾/SQLite 写入时间、读取字节、heap、working set、CPU、goroutine、worker、fallback 和吞吐指标。

增量扫描流程：

1. 对每个候选只打开一次源文件并复制到受控临时 snapshot；SHA-256 与 extractor 读取同一 snapshot，同时用原 handle 的 identity/stat 复核来源没有被路径替换。
2. 每轮重新校验 snapshot/hash；内容与已索引 hash 相同时不重写 SQLite，不再仅以 `size + mtime` 跳过 Go Plugin 解析。
3. 成功结果在批次事务中原子删除该文档旧 chunk/FTS 并写入新内容；事务边界最多为 256 份文档或 25,000 个 chunk。
4. 提取失败保留 last-good chunk，文档标记 `stale` 并记录错误。
5. 只有来源成功完成发现后，才对本轮未出现的该来源文件执行 missing reconciliation。

完整重建在确认所有启用来源可发现后清空可重建缓存。日常扫描、来源添加、来源修改和重新启用均使用增量路径。暂停请求在当前在途文件完成后进入 `paused`；继续后复用同一批候选和 Go worker，不通过强杀进程制造半写事务。

## GUI 自动增量更新

Electron main 使用 `IndexCoordinator` 管理自动更新：

1. 应用启动后 750 ms 请求一次增量扫描。
2. 对每个启用来源安装递归 `fs.watch`。
3. 文件系统事件经过 1.5 秒 debounce，同一批来源合并后执行 scoped 增量扫描。
4. 按 `scanIntervalMinutes` 周期执行全启用来源的兜底增量扫描。
5. watcher 不可用时显示降级提示，但周期扫描仍保留。
6. 更新完成、索引已是最新或发生错误时，通过 `AppEvent.notice` 和最新快照更新 GUI。

GUI 每 1.5 秒检查外部配置 hash；若 CLI 或 MCP 修改来源，会刷新 watcher、快照和提示。设置页添加/修改来源走同步 reconciliation，删除只清理对应来源索引，停用只更新检索资格。

## 支持格式

- DOCX：保留 heading、段落与表格定位。
- XLSX/XLSM/XLS：按工作表和行组分块，返回 cell range，并保留 cell address、field、formula 字符串和 cached value；BIFF8 同时解码 token、defined name 与 shared formula，不支持 BIFF5/7；所有格式都不执行或重算公式。
- PDF：按页提取；无文本层时显式标记 `needs_ocr`。
- Markdown/TXT/HTML/JSON/YAML：保留行号或标题路径；TXT 检测历史中文编码。
- XMind：解析 topic tree。

临时文件、`~$`、`.tmp`、`.bak`、`.git`、`node_modules` 和超限文件默认跳过；索引运行记录保存 skipped 总数，当前不持久化每个被跳过文件的逐项原因。

## 检索

Go Plugin runtime 的 `SearchEngine` 独立提供离线词法检索：标题/heading/路径/正文加权、CJK shingles、trigram、领域同义词、section filter、exact ID、活动身份 gate、table-intent 配额与 newest-first；SQL cutoff 使用日期、路径、document/chunk identity 的确定性 tie-break，并实现 search/retrieve/citation/version API。桌面 TypeScript SearchEngine 在迁移期保留，真实语料 A/B 要求两个引擎分别执行；完整文档/chunk 投影必须一致，强 latest top-1、Recall、identity recall 和 citation 必须通过，宽查询允许 Go 的意图排序不同，不把旧引擎全序复制当作准确性。可选本地 Ollama embedding 只作增强；未启用、未就绪或覆盖不完整时返回实际模式，不把词法结果伪装成完整语义检索。

默认结果严格按 `effectiveUpdatedAt DESC → relevance DESC → relativePath ASC`。业务日期优先级为“文件名日期 → strong version evidence → 路径日期 → weak cover/version evidence → embedded modified → filesystem mtime”；同一范围内多个合法日期取最新值，每条结果返回 `dateSource`。

`retrieve` 返回固定 schema 的 evidence bundle，包含查询、index revision、实际检索模式、文档、chunk、citationId、locator、内容哈希和字符预算。它只检索，不生成自然语言答案。

每条 citation 和 evidence 还包含 `sourceLink`（`fileName`、`absolutePath`、`locator`、`markdown`）。Codex 回答使用 `sourceLink.markdown` 显示真实文档与原文位置；`DRAG:chunk_*` 只用于协议回读，不作为用户可见引用。桌面 host 只接受当前回合真实检索返回的 citationId，并要求回答区分“证据事实 / 推断 / 待确认”；未知或旧回合 citation 不会被渲染为引用。文件链接是否直接启动关联应用由 Codex host 决定，locator 始终保留可人工核对的位置。

## MCP

MCP stdio 当前暴露 3 个只读 Markdown resources，启动说明要求 agent 先读取主 Skill 和任务所需工作流：

- `design-rag://skill/game-design-rag`
- `design-rag://skill/game-design-rag/analysis-workflows`
- `design-rag://skill/game-design-rag/administration`

随后可调用 13 个工具：

只读查询和状态：

- `drag_search`
- `drag_retrieve`
- `drag_read_citation`
- `drag_list_versions`
- `drag_sources`
- `drag_index_status`

需要 host 审批的管理操作：

- `drag_source_add`
- `drag_source_update`
- `drag_source_remove`
- `drag_index_update`
- `drag_index_pause`
- `drag_index_resume`
- `drag_cache_clear`

`drag_source_remove` 和 `drag_cache_clear` 带 destructive annotation；其他管理工具带 mutating/idempotent annotation。Plugin 的 `.mcp.json` 对管理工具设置 prompt approval。MCP stdout 只写协议消息，诊断和进度写 stderr。

来源添加/修改和索引更新可以作为 MCP 后台任务立即返回；controller 尚未建立时收到的 pause intent 会在建立后应用，活动索引期间拒绝冲突 mutation，`drag_index_status` 用于读取真实进度。查询、引用、来源和状态工具调用前后都会检查磁盘配置变化，跨进程停用来源时会重试或拒绝旧证据。

Plugin cache 按版本安装。发布新版本时提升 manifest/package 版本后执行 `codex plugin add`；安装后必须启动新的 Codex host 进程或重启 Desktop 才能证明新 Skill/MCP 已加载，在同一已运行 Desktop host 中仅新建任务不构成刷新。不要杀死活跃 MCP 进程来覆盖同版本缓存，否则正在运行的任务会收到 `Transport closed`。

宽范围查询的协议上限为：`drag_search.limit ≤ 100`，`drag_retrieve.maxDocuments ≤ 50`，每份文档最多 10 个 chunk，证据字符预算最多 60,000。超过范围时应按日期窗口、玩法或活动类型分批检索，避免一次返回无限候选。

## Codex Plugin

Plugin 技术 ID 为 `design-rag`，用户界面显示名为 `DRAG 游戏策划知识库`。Plugin 源位于 `plugins/design-rag`：

```text
plugins/design-rag/
├── .codex-plugin/plugin.json
├── .mcp.json
├── skills/game-design-rag/
│   ├── SKILL.md
│   └── references/
└── THIRD_PARTY_NOTICES.md
```

源码 checkout 不在 `.agents/plugins/marketplace.json` 自注册，因为 `main` 不携带目标平台 binary，不能直接作为 Desktop 安装包启动。marketplace 模板保存在 `packaging/design-rag-marketplace.json`；Go 构建器只在完整 stage 中生成 `.agents/plugins/marketplace.json`，并将 MCP command 改写为目标平台 Go binary。

正式发布由 GitHub Actions 在 Windows x64 与 Apple Silicon macOS 原生 runner 分别完成 stage、CLI/MCP smoke 和 archive。两个平台都通过后，workflow 以已验证的 `origin/main` 源码提交为父提交构造独立发布树，只在该树的 `plugins/design-rag/bin` 中加入 `drag.exe` 与 `drag`，再创建不可变 `vX.Y.Z` tag；该分发提交不合并回 `main`。GitHub Release 只发布两平台 Codex Plugin 本地安装包、Windows GUI、macOS DMG 和 `SHA256SUMS.txt`；Release Notes 解释每个文件用途并显示签名/公证状态。stage、runtime 和汇总 evidence 保存为 90 天 Actions 审计产物，不与用户下载项混列。Release 验收成功后，上游使用仅授权 `s-plugins` 的 Secret 发送 `plugin-released` repository dispatch；接收端幂等更新 `git-subdir` ref 并自行校验、提交 `main`，订阅者随后可直接启动 tag 树中的对应平台 binary。

Skill 覆盖模糊查找策划、玩法/流程/产出分析、相关配表、历史版本和活动复用，并在首次使用时通过管理工具配置来源和索引。

纯 Go 分发包按平台只携带目标平台 `drag.exe`/`drag`、Plugin metadata、Skill、第三方声明、本地 marketplace 配置和安装说明。构建器拒绝任何 `node`、`node_modules`、runtime 目录或 `.js/.mjs/.cjs` 启动文件进入 stage。

正式分发版本必须是与项目基础版本一致的严格 `x.y.z`，源码校验、stage 校验和 ZIP 回读校验都会拒绝 `+codex.*` 或其他 build metadata。`+codex.<cachebuster>` 只允许用于开发者本机对同一基础版本执行缓存刷新，不得写入正式 stage、archive 或用户安装源。

Go runtime 可从 Windows 交叉构建并核验 `darwin-arm64` Mach-O；最终 Plugin archive 仍强制目标原生 runner：Windows x64 构建 `win32-x64`，Apple Silicon Mac 构建 `darwin-arm64`。Windows 隔离 `design-rag-go-test` stage 执行 CLI 与真实 MCP stdio smoke；正式 stage 恢复原身份并复用相同 binary，但为避免撞到已安装的同名 MCP 不在本机启动。macOS codesign、notarization、Gatekeeper 和 Apple Silicon 实机运行仍属于独立发布门禁。

## Electron 与 Codex app-server

Electron main 启动当前用户安装的 `codex app-server --listen stdio://`，使用 `initialize → initialized → account/read → thread/start|resume → turn/start`。默认复用 Codex CLI 登录态；未登录时使用 app-server 管理的官方登录流程。renderer 永远接触不到 token。

每个回合由 host 先调用 `retrieve`，立即更新证据栏，再把 evidence envelope 与用户问题交给 app-server。实验 `dynamicTools` 只作补充；失败不影响主检索路径。补检索结果按 document/chunk 去重合并，不清空已显示来源。

多 thread 由 app-server 保存模型 thread；应用本地保存属于产品的 threadId、消息、检索证据和 UI 元数据。事件按 `threadId + turnId` 分流。流式 token 只更新内存，回合完成时串行原子持久化。

模型选择器调用 app-server `model/list`，使用当前账号真实返回的模型和 `supportedReasoningEfforts`。归档、恢复、删除分别镜像到 app-server 的 thread 操作。

回答中的 `[[DRAG:chunkId]]` 是内部引用语法。host 先用当前回合 allowlist 校验，再替换为真实 `sourceLink.markdown`；renderer 不显示裸 `DRAG:chunk_*`。独立 GUI 把已验证的本地 Markdown 目标重新绑定到 citationId，经命名 IPC 校验后打开。Windows 默认关联为 Microsoft Excel 时使用隐藏 PowerShell COM helper 尝试定位范围；WPS 或其他默认应用则直接通过系统关联打开，避免先等待 Excel COM；重复打开请求会合并。

设置页的文件夹拖入链路为：Electron `DragEvent.dataTransfer.files` → renderer `onDrop` → preload `webUtils.getPathForFile` → 命名 IPC → main `stat`/目录校验 → 来源对话框路径。最终 Windows E2E 使用 CDP `Input.dispatchDragEvent` 向真实 `drag-gui` renderer 注入目录拖入，实际经过 preload 和 IPC，而不是在静态预览中直接写表单值；原生文件夹选择器也已通过 Computer Use 前台选择真实目录。Explorer 跨窗口物理鼠标拖拽仍属于独立 `NOT TESTED` 手势层，不影响已验证的拖入协议与实现结论。

## 安全

- Electron renderer：关闭 Node integration，开启 context isolation、sandbox 和 CSP。
- IPC 每个动作独立暴露并用 Zod 校验；拖入文件夹通过 preload `webUtils.getPathForFile` 解析，再由 main 校验为目录。
- app-server child process 使用 `shell:false`、隐藏窗口、JSONL 缓冲和精确 PID 清理。
- 路径解析后必须仍位于授权 source root 内；默认不跟随 symlink/junction。
- 引用只把真实返回过的 citationId 渲染为可点击来源。
- MCP 管理工具通过 annotations 与 Plugin approval 配置区分只读、写缓存和破坏性操作。

## 验收层级

1. 单元与生命周期：token、日期、family、chunk、来源启停、scoped incremental、精确 purge、跨进程 lease、配置热重载。
2. 提取 golden：中文 DOCX、XLSX、XLS、PDF、GBK TXT、损坏/空文本文件。
3. SQLite：有界 startup probe、完整 integrity gate、增量无变化不重写、last-good、missing reconciliation、缓存恢复。
4. Retrieval eval：轮盘抽奖、流程、配置、历史改动和 newest-first。
5. MCP：3 个 resources、13 个工具、参数上限、annotations、来源生命周期；源码 Go runtime 与隔离 `go-test` stage 执行真实 stdio 全工具调用及并发回归，正式同名 stage 不在已安装 Plugin 的宿主上启动。
6. Plugin：官方 manifest/Skill validator、目标平台原生分发包、安装、独立 Codex 会话 MCP 回归。
7. app-server：真实 initialize/account/model/thread smoke。
8. GUI：浏览器视觉检查后，在真实 `drag-gui` 验证 renderer/preload/IPC、目录拖入、配置持久化、增量与 watcher；Explorer 物理手势单独报告。
9. 发布：Windows/macOS 分别完成原生构建、签名策略、hash 和目标机验收。

当前门禁状态见 [acceptance.md](acceptance.md)。
