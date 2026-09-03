# Codex Plugin 纯 Go 迁移评估与验收

日期：2026-09-03

## 结论

Codex Plugin 的底层运行时已迁移为单一纯 Go binary：CLI、MCP、配置、来源生命周期、跨进程 mutation lease、格式抽取、索引、搜索、retrieve、citation 和 versions 均不再依赖 Node.js。Electron/React 桌面客户端是独立产品层，仍使用 Node/TypeScript 构建；本次迁移没有把桌面应用改写为 Go。

Plugin 源码和正式 stage 均已恢复原身份 `design-rag`。隔离验收使用 `design-rag-go-test`，并同步隔离 marketplace、MCP、Skill、配置目录和索引目录；正式 stage 的构建器会拒绝任何残留 `go-test` 身份。发布基础版本为 `0.2.4`，本机 `design-rag-local` 已覆盖更新到 `0.2.4+codex.20260903021751`，旧产物保留为可回退备份。

## 迁移盘点、可行性与结果

| 能力 | 结果 | 兼容与实现要点 |
|---|---|---|
| 配置与路径 | PASS | 现代/历史目录、环境变量别名、原子写入、严格 JSON、source topology、NFC path key 与跨进程热重载 |
| 来源生命周期 | PASS | 添加、停用保留缓存、重启、root/kind identity 失效、精确删除、pending 恢复和配置已提交的部分失败回报 |
| SQLite 控制面 | PASS | schema v3、WAL、只读/immutable、lazy transaction、损坏 generation 指针、跨进程 mutation lease 与可观测 pause/resume |
| 搜索与 evidence | PASS | FTS5、trigram、LIKE fallback、exact ID、CJK、同义词、来源/日期/section 过滤、稳定排序、retrieve、citation 与 versions |
| MCP | PASS | 官方 MCP Go SDK、stdio、3 resources、13 tools、严格参数、annotations、后台索引、立即返回与配置热重载 |
| CLI | PASS | version/init/doctor/config/sources/index/search/retrieve/status/versions/citation/cache/mcp |
| Plugin 构建与校验 | PASS | Windows/macOS 双目标、PE/Mach-O 校验、Node denylist、源码 allowlist、license/source manifest、原子 stage 与 stdio smoke |
| Node Plugin runtime | 已移除 | 删除 TypeScript CLI/MCP、Node launcher、runtime-package、Node 构建/stdio/concurrency 脚本；桌面 TypeScript 代码不属于 Plugin runtime |

## 格式兼容与回退

| 格式 | 纯 Go 路径 | 状态与边界 |
|---|---|---|
| DOCX | 原生 OOXML | 标题、段落、表格、日期证据与 locator |
| XLSX/XLSM | 原生 OOXML；typed ZIP/structure failure 时 Excelize | 保留 cell address、field、formula 与 cached value；限制解压量和总 cell 数；不执行宏或重算公式 |
| XLS | 本地维护的 `nkiri/xls` fork | 仅支持 BIFF8；读取 cached value，并解码 formula token、defined name 与 shared formula；不执行或重算公式，BIFF5/7 fail closed |
| PDF | `giraffesyo/pdf` | 按页抽取文本；只有没有文本的图片页标记 `needsOcr`，空白/矢量页另行区分，不伪造 OCR 文本 |
| CSV | `encoding/csv` | 逗号/Tab/分号探测，UTF-8/UTF-16/GB18030，表格 locator 与结构化日期证据 |
| HTML | `x/net/html` | DOM 级标题、段落、列表与表格；script/style/noscript 不进入索引 |
| Markdown/TXT/JSON/YAML | 原生文本 | 行号/标题路径，UTF-8/UTF-16/GB18030 |
| XMind | 原生 ZIP/XML/JSON | topic tree 与 locator |

内容扩展名会做签名探测，可处理实际 OOXML 却命名为 `.xls`、或实际 BIFF8 却命名为 `.xlsx` 的文件；WPS `.~` sidecar 明确跳过。旧 `.doc`、`.ppt`、`.pptx`、`.xlsb` 仍不进入默认索引，图片型 PDF 没有内置 OCR。这些边界不能描述为“已索引”，也不能由无证据内容替代。

## 检索准确性改进

- SQL BM25 cutoff 在数据库层加入日期、路径、document ID、ordinal 与 chunk ID 的稳定 tie-break；1,225 个同分文档的正序/逆序插入回归结果一致。
- `maxChunksPerDocument` 的 1–10 协议范围全部生效；`documentIds` 在指定文档内重新检索，不再先全库检索再过滤为空。
- table-intent 查询优先保留配置表配额；活动“最新”身份只由 title/path 的强证据确定，更新更晚但仅正文命中的表不会冒充活动主案。
- canonical 去重仍保留显式 alias/strong ID 代表；多个显式 ID 不会互相挤出 Recall@8。
- 普通文本短 excerpt 使用带 revision、source identity、hash 与切片范围的 `DRAG:3` scoped citation；UTF-16 surrogate 边界按 JavaScript code unit 兼容处理，回读必须精确且篡改会被拒绝。
- 可选 Ollama embedding 只允许 loopback、禁用代理和重定向，并校验 vector 数量、维度、有限值与非零值；失败时明确降级为词法结果。

## 真实语料验收

最终隔离根：`tests/.tmp/go-plugin-migration-full-20260902-d`

- 完整索引：`PASS`。11,268 discovered、11,259 indexed、9 个已知损坏/空内容文件精确 allowlist、42 skipped；外部 wall clock 107.616 秒。该轮是全新索引，但源盘 OS cache 已被前一轮诊断预热，因此不是 cold-disk 基准。
- 输入只读：`PASS`。18,319,460,361 bytes；运行前后 fingerprint 均为 `aa388cdd027d2ebc9398c411c5124cd9cd3084171931ce70c1098824a51c0e7b`。
- SQLite：`PASS`。11,259 physical documents、120,780 physical chunks/FTS rows、10,738 canonical documents、117,949 canonical chunks；deleted、stale 与 orphan 均为 0，`integrity_check=ok`。
- 格式：`PASS`。40 个 `.xls` / 1,271 chunks，5 个 PDF / 11 chunks（3 个 `needsOcr`），539 个 table-source documents。
- BIFF8 公式：`PASS`。2,447 total，1,293 cached、1,154 uncached、2,447 decoded、0 degraded、0 empty；`XLOOKUP` 1,704 次、`TEXTJOIN` 577 次。前一候选报告 `20260902-c` 因 1,952 条 degraded 且两个 future-function 计数为 0 被门禁拒绝，不作为验收证据。
- TypeScript/Go A/B：`PASS`。两个独立搜索引擎读取同一物理索引；11,259 documents、120,780 chunks 的完整投影 SHA-256 分别一致，effective-date diff 为 0。六题 latest top-1、Recall@8、identity recall、citation 精确回读和隐藏工具目录门禁全部通过。
- 宽查询不要求旧 Node 与新 Go 的 top-N 完全同序：Go 对 table intent、明确配置 ID 和设计案优先级的排序是本次允许的准确性改进；差异保留在 A/B 报告中，不能表述为逐项排序 parity。

## 来源与并发安全

- 扫描保留用户可见路径，但对配置根做 realpath 边界判断；来源内部 symlink/junction 不跟随。
- 每个候选只打开一次源文件，并复制到受控临时 snapshot；hash 与 extractor 读取同一 snapshot，同时保留原 handle 做 identity/stat 复核，关闭 path-swap 导致 hash/body 不一致的窗口。
- 增量扫描会重新校验 snapshot/hash；内容未变时不重写 SQLite。仅用 `size + mtime` 跳过解析的旧快路径已从 Go Plugin runtime 移除。
- MCP 索引启动立即返回；controller 尚未建立时的 pause intent 会在建立后应用。来源 mutation 与后台索引串行，活动任务期间拒绝冲突写操作。
- startup 使用有界 table/FTS probe；完整 corpus 验收和 doctor 再执行完整 integrity gate。损坏数据页会切换到新 generation，不覆盖旧缓存。

## Plugin 隔离与正式身份

- 分发版本门禁：源码、正式 stage 与 ZIP 内 manifest 必须使用与项目一致的严格 `x.y.z`；`+codex.*` 仅限开发者本机缓存刷新，进入正式 archive 会使构建失败。

- Windows `go-test` stage：`PASS`。版本 `0.2.4`，身份为 `design-rag-go-test` / `design-rag-go-test-local` / `game-design-rag-go-test`；CLI 与 MCP stdio（3 resources、13 tools）均真实运行，Node artifact 为 0。binary SHA-256 为 `f49b25dd24170dad0eab4a637839304965311ae33124323af8b1cfba6191d2b4`。
- Windows 正式 archive：`PASS`。原身份 `design-rag` / `design-rag-local` / `game-design-rag`；ZIP 为 `release/plugin/win32-x64/design-rag-local-0.2.4-win32-x64.zip`，`7,781,842 bytes`，SHA-256 `e87a8c49cac79584d126991da8785380ef56a47a08ade62fd07abc2fb9888fd9`，archive validation 与 Node denylist 均通过。正式 binary 与 go-test stage hash 相同。
- macOS arm64 cross-stage：静态 `PASS`。原身份、Mach-O arm64、文件模式、Node denylist 与 validator 通过；binary 为 `17,747,442 bytes`，SHA-256 `35a5912e02f9283606170e338c0c745c7d7de145c8d6d71f267e138d1b144c30`。当前 Windows 宿主不能执行该 binary。
- macOS arm64 正式 archive：`BLOCKED`。构建器要求在 Apple Silicon 原生 runner 先完成隔离 CLI/MCP 验收，再生成正式 ZIP；仓库当前没有可用 Mac runner，未绕过该门禁。
- 本地安装：`PASS`。配置的 `design-rag-local` marketplace 已替换为 `0.2.4` Go-only stage，上一版 Go 产物保存于已脱敏的本地发布目录；`codex plugin add` 返回版本 `0.2.4+codex.20260903021751`，cache binary SHA-256 为 `f49b25dd24170dad0eab4a637839304965311ae33124323af8b1cfba6191d2b4`，无 `runtime/`、Node 或 JavaScript artifact。
- 安装后 CLI：`PASS`。安装 cache 在真实历史索引上完成 `drag search` 并返回可核验 citation；MCP 运行证据来自相同 binary hash 的隔离 go-test stage。正在运行的 Desktop 进程不作为热更新证据，下次使用前应重启 Desktop。

## 分发体积

- Windows `0.2.4` ZIP：`7.421 MiB`；旧 Node `0.2.3` ZIP：`75.780 MiB`，减少 `90.21%`，约为原来的 `1/10.21`。
- Windows 展开 Plugin：`17.899 MiB / 45 files`；旧 Node 安装缓存：`223.048 MiB / 4,248 files`，体积减少 `91.98%`，文件数减少 `98.94%`。
- Mac ARM 展开 cross-stage：`17.025 MiB / 45 files`。
- Go 使用 `CGO_ENABLED=0`、`-trimpath`、`-buildvcs=false` 和 linker `-s -w`。未使用 UPX；继续压缩只能主要从 SQLite、Excelize、PDF 等格式依赖下手，会直接增加兼容性和回归风险。

## 风险与未完成门禁

- 本地 `nkiri/xls` fork 修复了 `PtgAttr`、BIFF `NAME`、`SHRFMLA`/`PtgExp` 和 CFB 边界保护；40 个真实 `.xls` 已全量覆盖，但 fork 需要持续维护、上游差异审计和 fuzz/corpus 回归。
- snapshot 消除了 path-swap 的 hash/body 不一致，但会增加临时磁盘 I/O；在同一已打开文件上进行极端敌对的原地、同长度、恢复时间戳写入，仍不能宣称数学上完全不可发生。
- PDF 依赖使最低 Go 版本为 1.26。当前 5 个真实 PDF 已覆盖，未知复杂字体、损坏对象流和生产长尾仍需监测。
- Ollama semantic/hybrid 仅完成确定性与失败回退测试；本机真实模型质量和性能为 `NOT TESTED`。
- `go test -race` 依赖 CGO race runtime/toolchain；本机结论以最终验收表为准，不能用普通单测替代。
- Apple Silicon 实机执行、codesign、notarization、stapling 与 Gatekeeper 均为 `NOT TESTED`；正式 Mac archive 因没有原生 runner 为 `BLOCKED`。携带真实语料的生成式 Codex 会话未执行，因为安装授权不等于把本地文档发送给模型服务的授权。
- Git 提交、推送与主干合并必须以最终仓库历史核验，不能由构建产物反推。
