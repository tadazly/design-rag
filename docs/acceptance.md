# design-rag（drag）0.2.4 验收记录

## 2026-09-03 Codex Plugin 纯 Go 迁移与发布

当前独立工作树已把 Codex Plugin 的 CLI、MCP、配置/来源控制面、格式抽取、索引、检索、retrieve、citation 与 versions 迁移到单一纯 Go `drag` binary；Plugin stage 不再包含 Node/JavaScript runtime。Electron/React 桌面构建不在本次迁移范围内。本机 `design-rag-local` 已在用户明确授权后覆盖更新。

| 门禁 | 状态 | 当前证据 |
|---|---|---|
| 独立 worktree / branch | PASS | worktree `f485`；branch `codex/plugin-go-runtime` |
| 纯 Go 全新索引 | PASS | 11,268 discovered；11,259 indexed；9 个精确 allowlist failed；42 skipped；107.616s；源盘 OS cache 已预热，不是 cold-disk 基准 |
| 输入只读与稳定 | PASS | 18,319,460,361 bytes；运行前后 inventory fingerprint 均为 `aa388cdd027d2ebc9398c411c5124cd9cd3084171931ce70c1098824a51c0e7b` |
| SQLite | PASS | 11,259 physical documents、120,780 chunks/FTS；10,738 canonical documents、117,949 canonical chunks；integrity ok；stale/deleted/orphan 均为 0 |
| `.xls` 与 formula | PASS | 40 documents / 1,271 chunks；2,447 total、2,447 decoded、0 degraded/empty；XLOOKUP 1,704、TEXTJOIN 577 |
| PDF | PASS | 5 documents / 11 chunks；3 个无文本层图片页文档标记 `needsOcr` |
| 配表 | PASS | table source 539 documents |
| Go 检索单元/回归 | PASS | deterministic SQL cutoff、latest identity、table intent、strong ID、documentIds、DRAG:3/UTF-16 scoped citation 与防篡改 |
| TypeScript/Go 搜索 A/B | PASS | 两个独立引擎；11,259 documents / 120,780 chunks 完整投影 hash 一致，日期 diff 0；六题质量门禁通过；宽查询排序差异为有意改进，不宣称全序 parity |
| `design-rag-go-test` 隔离 stage | PASS | `0.2.4`；Plugin/marketplace/MCP/Skill/state 全部隔离；PE x64；CLI 与 3 resources / 13 tools 真实 stdio PASS；binary `f49b25dd…1d2b4`；Node artifact 0 |
| 正式 Windows x86_64 archive | PASS | `design-rag-local-0.2.4-win32-x64.zip`；7,781,842 bytes；SHA-256 `e87a8c49cac79584d126991da8785380ef56a47a08ade62fd07abc2fb9888fd9`；archive validation PASS |
| macOS arm64 cross-stage | PASS（静态） | Mach-O arm64；17,747,442 bytes；SHA-256 `35a5912e02f9283606170e338c0c745c7d7de145c8d6d71f267e138d1b144c30`；Node artifact 0；runtime/MCP NOT TESTED |
| macOS arm64 正式 archive | BLOCKED | 缺少 Apple Silicon 原生 runner；禁止在 Windows 手工 ZIP 绕过 native runtime/MCP 门禁 |
| `npm test` | PASS | 68 tests：67 pass、1 个真实语料日期 parity 因未设置 `DRAG_DATE_PARITY_ROOT` skipped、0 fail；含 Go test/vet 与本地 XLS fork |
| `npm run check` | PASS | Go test/vet、fork test/vet、Go build、TypeScript build、Vite renderer build |
| MCP concurrency / stdio | PASS | `TestBackgroundIndexJob`；`TestMCP*` 与 `TestWindowsGoTestStage` |
| `go test -mod=readonly ./go/...` | PASS | Go core、CLI 与 pluginpack；本地 XLS fork `test -mod=readonly` / `vet -mod=readonly` 同样 PASS |
| `npm run test:app-server` | PASS | Codex CLI 0.152.0；ChatGPT connected/pro；dynamic model list 返回 |
| `go test -race ./go/core` | BLOCKED | 默认 `CGO_ENABLED=0`；启用 CGO 后本机没有 `gcc`，无法构建 race runtime |
| 本地 marketplace 覆盖 | PASS | 上一版备份为 `win32-x64.backup-20260903-101730`；新 marketplace 无 Node/runtime；binary SHA-256 `f49b25dd24170dad0eab4a637839304965311ae33124323af8b1cfba6191d2b4` |
| `codex plugin add` | PASS | `design-rag@design-rag-local` 已安装并启用为 `0.2.4+codex.20260903021751` |
| 安装 cache CLI / 检索 | PASS | `drag --version` 返回 `0.2.4`；真实历史索引检索返回路径、locator、hash、日期与 citation；安装 cache 无 Node artifact |
| MCP | PASS | 与安装 cache 相同 binary hash 的 go-test stage 完成 3 resources、13 tools 与无污染 stdio 验收 |
| Apple Silicon / codesign / notarization / Gatekeeper | NOT TESTED | 必须在目标原生 runner 验收 |
| 真实语料生成式 Codex 会话 | NOT TESTED | 不把本地资料发送给模型，除非另行授权 |
| Git 发布 | 以仓库历史为准 | 构建验收不预判后续 commit、push 与 fast-forward merge 结果 |

最终完整索引证据位于 `tests/.tmp/go-plugin-migration-full-20260902-d/acceptance-report.json`，A/B 位于同目录的 `typescript-go-search-ab-final.json`。`0.2.4` 只提升发布版本并重跑完整单元、构建、Plugin 与 app-server 门禁，没有改变上述 corpus 输入或检索实现，因此未伪造一次重复全库性能运行。前一候选 `20260902-c` 正确地因 1,952 条 BIFF formula degraded、XLOOKUP/TEXTJOIN 为 0 而失败；修复后才生成最终通过报告。当前 Desktop 进程应在下次使用 Plugin 前重启。完整迁移清单、格式边界、体积与依赖风险见 [go-plugin-migration.md](go-plugin-migration.md)。

---

以下内容是 2026-09-01 Node-host Plugin 与桌面基线，用于历史对照，不代表当前纯 Go Plugin stage。

## 结论（2026-09-01 历史）

`0.2.3` 已完成 Go indexing data plane 的真实默认语料冷索引、Node/Go evidence A/B、Windows x64 Plugin 最终包、安装后新宿主进程检索以及 app-server dynamic model handshake。Go 负责扫描、Go-native 抽取、日期解析、结构分块和 SQLite/FTS 写入；TypeScript host 继续负责配置、mutation lease、来源生命周期与恢复、search/retrieve/citation/version，以及 `.xls`、PDF 和 typed XLSX failure 的逐文件 extract/date/chunk fallback。这里的“Go 索引主链”不表示 Node-free，也不表示 Go 独立提供搜索 API。

Windows `0.2.3` Plugin、两个全新 Codex host 会话和 fresh `drag-gui` 均已通过。GUI 证据绑定当前 EXE/ASAR/Go hash，覆盖来源生命周期、startup incremental、watcher、进度与暂停/继续、原生文件夹浏览和精确 PID 清理；不是借用 `0.2.1` 历史结果。macOS 仅完成 Go core 与 Plugin stage 的 Windows 跨宿主静态检查；runtime、实际 executable mode、签名、notarization、Gatekeeper、最终 archive 和 Apple Silicon 实机均为 `NOT TESTED`。

## 当前 0.2.3 门禁

| 门禁 | 结果 | 当前证据 |
|---|---|---|
| 策划案冷索引 `<60s` | PASS | 外部 `50.727s`；Go `48.853s`；10,730 discovered、10,721 indexed、41 skipped、9 failed |
| 配表首次 scoped 索引 | PASS | 外部 `5.720s`；Go `5.040s`；539/539 indexed、0 failed |
| SQLite 完整性 | PASS | schema 3；`quick_check=ok`；11,260 documents、120,909 chunks/FTS、45,287 trigram、stale 0 |
| Node/Go evidence A/B | PASS | same inventory；11,260 common；node-only 0、go-only 0、effective-date diff 0；六题全部通过 |
| Windows Go core `0.2.3` | PASS | protocol 2；SHA-256 `0905ab2b05e1b8e988aab1ad867126192014e55183828799b33751cdb90c95d3` |
| Windows Plugin `0.2.3` 最终包 | PASS | ZIP、随包 Node、native addon、Go、launcher、runtime execution 与真实 MCP 全工具 smoke 通过 |
| 安装缓存与新宿主加载 | PASS | `0.2.3+codex.20260901072442`；两个独立新 Codex host 会话完成真实检索与引用回读 |
| app-server dynamic model handshake | PASS | 当前安装的 Codex app-server 完成 dynamic model handshake |
| `npm test` / `npm run check` | PASS | Go test/vet、65 项确定性 TypeScript 集成测试 + 15 项真实语料日期子测试（80 pass）、类型检查和 renderer build 通过 |
| Windows `0.2.3` `drag-gui` lifecycle | PASS | fresh EXE/ASAR/Go 匹配；settings lifecycle 与 startup incremental 报告均为 PASS |
| macOS arm64 静态 stage | PASS | Mach-O arm64、manifest/MCP 布局、目标 native addon 与 Go core hash 静态核验通过 |
| macOS runtime / executable mode / Plugin archive / GUI / signing / notarization / Gatekeeper / 实机 | NOT TESTED | 需要 Apple Silicon 原生 runner |

## Go 冷索引性能与资源

证据目录：`tests/.tmp/go-full-evidence-0.2.3-2026-09-01T06-44-00-959Z`。

- 主报告：`acceptance-report.json`
- Node/Go A/B：`node-go-evidence-ab.json`
- Go core：`dist/native/drag-core.exe`
- Go core SHA-256：`0905ab2b05e1b8e988aab1ad867126192014e55183828799b33751cdb90c95d3`

### 可比语料清单

| 指标 | 值 |
|---|---:|
| algorithm | `sha256-source-config-canonical-path-size-mtime-ms-v1` |
| SHA-256 | `79c222abb39e4721ed932d08345b8503ea2d112f348be2af027e6d7a9c77b8f5` |
| 文件 / 来源 | `11,269 / 2` |
| 总字节 | `18,319,460,526` |
| 策划案 / 配表候选 | `10,730 / 539` |

索引前后 inventory 稳定，候选总数等于两段运行的 discovered 总和。报告缺失、inventory 算法或 hash 不同、运行期间清单变化，或 discovered 不匹配时，A/B 脚本会 fail closed。

### 策划案

| 指标 | 值 |
|---|---:|
| 来源 | `[已脱敏的策划案目录]` |
| 外部 / Go wall clock | `50,726.9ms / 48,853ms` |
| discovered / indexed / skipped / failed | `10,730 / 10,721 / 41 / 9` |
| canonical documents / chunks | `10,201 / 111,614` |
| 本轮写入 chunks | `114,445` |
| 读取字节 | `650,370,424` |
| SQLite write | `42,065ms` |
| worker | `16` |
| TS fallback documents | `47` |
| 峰值 working set | `1,647,812,608 bytes` |
| 峰值 Go heap alloc / heap sys | `881,233,992 / 1,096,155,136 bytes` |
| CPU time / sampled peak CPU | `29,249ms / 374%` |

47 个 fallback 是格式能力边界：旧 BIFF `.xls`、PDF 或原生 XLSX typed failure 由 TypeScript host 生成单文件 extract/date/chunk draft，之后仍由 Go writer 写 SQLite/FTS。9 个损坏或空内容项写入 `index_issues` 并保留真实路径与错误；drag 没有修改源文件，也没有为失败项生成伪内容或伪引用。

### 配表

| 指标 | 值 |
|---|---:|
| 来源 | `[已脱敏的配表目录]` |
| 外部 / Go wall clock | `5,719.9ms / 5,040ms` |
| discovered / indexed / failed | `539 / 539 / 0` |
| canonical documents / chunks | `538 / 6,464` |
| TS fallback documents | `1` |
| SQLite write | `4,630ms` |

策划案与配表采用“先策划案冷库、再首次启用配表”的两段真实流程；两段不能相加冒充单个命令的端到端测量。最终同一 SQLite 为 `1,103,151,104 bytes`，包含 11,260 physical documents、120,909 chunks/FTS rows、45,287 trigram rows、stale 0，`quick_check=ok`。

## Node/Go evidence A/B

两端使用完全相同的 11,269 文件 inventory。A/B 结果：

- 11,260 Node documents、11,260 Go documents、11,260 common documents；
- node-only 0、go-only 0；
- effective-date diff 0；
- same inventory、latest top-1、top-1 parity、required Recall@8、required identity Recall@8、citation readability、六题总门禁全部为 true；
- 隐藏工具/凭据目录及明显 secret 文档进入索引或 evidence 的数量为 0。

六个问题覆盖最新 888、扭蛋机配表、妖王888复用、轮盘复用、环潮龙888产出和显式 `newLottery newPrizePool` ID。A/B 只证明相同语料、当前索引表示和检索门禁；不会把历史 Node 总耗时冒充为本次 Go 运行耗时。

## 来源生命周期与崩溃恢复

配置文件是来源权威状态，所有读路径同时执行 enabled + current source identity gate：

- 添加：配置先原子落盘，只对新增且启用来源执行 scoped 增量；
- 停用：不改 SQLite documents/chunks/FTS/hash/identity，只立即屏蔽 search、retrieve、citation 和 versions；
- 重新启用：identity 未变时复用缓存并 scoped 检查；停用期间 root/kind 已变时，重新启用才软失效旧 identity 并强制重抽取；
- 同 id 换 root/kind：旧证据立即不能通过 read gate，旧记录软标记 `deleted=1, stale=1`，新来源 scoped 增量；
- 删除：配置移除后立即不可检索，reconciliation 精确硬 purge 对应 source 的 documents、chunks、FTS、embeddings、issues 和 state，源文件不删除；
- 配置保存后任一阶段崩溃：下一次普通 index 或桌面启动自动增量根据 pending/source state 恢复，并精确清理 orphan 来源；恢复前旧 root/kind 不会通过 search、citation 或 versions gate。

只有删除来源配置执行单来源硬 purge；停用和同 id replacement 均不先硬清缓存。

## MCP、Plugin 与独立 Codex 会话

源码版与 Windows `0.2.3` 打包版的真实 stdio 报告位于：

- `tests/.tmp/mcp-stdio-all-source/report.json`
- `tests/.tmp/mcp-stdio-all-packaged/report.json`

两者均读取 3 个 Skill resources、实际调用全部 13 个工具，stderr 为 0 bytes。报告本身不包含旧文档所称的 14 路 concurrency 结果，因此本文不把历史并发记录冒充为本次 `0.2.3` 门禁。

Windows Plugin 最终包：

- ZIP：`release/plugin/design-rag-marketplace-0.2.3-win32-x64.zip`
- ZIP 大小：`79,456,233 bytes`
- ZIP SHA-256：`9af149c96ef24e2b17aa3d6b90fa82abcea04d41e767e705b38372aae6111e5f`
- Go core SHA-256：`0905ab2b05e1b8e988aab1ad867126192014e55183828799b33751cdb90c95d3`
- 官方 Node archive SHA-256：`57f71ab3652e797d84acddc79c81cc9ff1c6ddb2a1974cdb83f00fee9bff4c73`

最终安装缓存为 `0.2.3+codex.20260901072442`。缓存中的 manifest、MCP 配置、Skill、search、MCP server 和 Go core 均与 stage 匹配。两个相互独立的 `gpt-5.6-sol`、`max` 推理 Codex 新宿主会话分别完成扭蛋机配表和妖王888复用问题：

- 使用正确的 `design-rag` MCP server；
- backend `0.2.3`、protocol 2；
- 同时取得 design 与 table 证据；
- citation 回读成功并显示可点击真实来源与 locator；
- unknown server、unknown citation、裸 chunk ID 均为 0。

Plugin 安装不是热刷新。已打开的 Desktop host 和已启动 MCP 可以继续使用旧缓存；安装新版本后必须启动全新 Codex host 进程或重启 Desktop，才能把新 Skill/MCP 加载写成 PASS。在同一已运行 Desktop host 中仅新建任务不构成新版本加载证据，也不得通过终止活跃 MCP 或覆盖同版本缓存强制刷新。

## Windows `drag-gui`

### 当前 0.2.3

当前 artifact：

- `release/win-unpacked/drag-gui.exe`：`244,440,576 bytes`，SHA-256 `ffd3e15a424334eec08274dc6bb2ced0a6a752966324afc781146edafbabaef6`；
- `resources/app.asar`：`105,217,867 bytes`，SHA-256 `baa0be0582a41524060a9314d6efdf462a583a884d8e8cee2f7625680d8ceaea`；
- 随包 Go core：SHA-256 `0905ab2b05e1b8e988aab1ad867126192014e55183828799b33751cdb90c95d3`；artifact guard 核对 74 个 runtime 文件与当前 dist 匹配。

来源 lifecycle 报告：`tests/.tmp/electron-settings-2026-09-01T07-37-28-478Z/electron-settings-report.json`，SHA-256 `2a7f3220918388560d2e7eeb57bf1113a04938dfb38d948ec8a50cdf72bedd56`。结果：

- 真实 Electron renderer/preload/命名 IPC 目录 DragEvent、browse IPC 类型切换、watcher notice 和 citation：PASS；
- 停用 revision `3 → 3`，documents/chunks/FTS/hash/source state 逐表不变，目标 search 为 0、其他来源为 1、旧 citation 被拒绝；
- 重新启用只做 scoped incremental：`discovered=1, indexed=0, unchanged=1`；
- partial run：`discovered=2, indexed=1, failed=1`，成功证据和错误提示同时可见；
- 删除只 purge 目标来源 `documents 1 → 0`，其他来源不变，源文件 hash/mtime 不变；根 PID 10828 及精确进程树已清理。

启动自动增量报告：`tests/.tmp/electron-startup-incremental-2026-09-01T07-40-20-008Z/electron-startup-incremental-report.json`，SHA-256 `70029b967e958e86de81287e985434f54441ecaaeb4eb8ddcf6d3773abe83cd5`。离线修改明确早于第二次启动，watcher 被排除为替代解释，revision `1 → 2`，启动后 1,207ms 开始增量并可 search/citation 新 marker；PID 27260、18328 及进程树均已清理。

Computer Use 前台验收还在同一 fresh EXE 上确认：来源类型可显式选择“策划案/配置表”，原生文件夹选择器可写回真实目录，本地新增 CSV 后自动增量 `updated=1` 并显示通知；全量进度条在 93% 成功暂停并恢复至完成。Explorer 跨窗口物理拖拽手势仍为 `NOT TESTED`，但真实 renderer/preload/IPC DragEvent 已通过。

### 历史 0.2.1 证据

历史完整报告为 `tests/.tmp/electron-settings-2026-09-01T01-23-28-390Z/electron-settings-report.json`，`scope=full`。对应 `drag-gui.exe` 为 `244,440,576 bytes`，SHA-256 `a8c1efe554afb2d59a5e7da802d5488cd6e4ed766728a7b03107695626c5ec62`；内嵌 Go core SHA-256 为 `71eddb43e7232ac908e56dcad2c6244020c56907dfd51bce48630b716d234c2c`。该版本曾通过 CDP 目录拖入、watcher、来源启停、pause/resume、cache clear、source remove 与 citation，并保留源文件；它只作为历史回归基线，不代表 `0.2.3` 当前 GUI PASS。

历史 Computer Use capture 限制已由当前 0.2.3 的原生目录选择器前台验收取代；历史 artifact 仍只用于回归对照。

## macOS arm64

`0.2.3` Go core 已在 Windows 使用无 CGO 交叉构建：

- SHA-256：`1c9ca0d3e1a82f43c985af27149b12710d51ed822d26f86aba364c5626893c10`
- stage：`tests/.tmp/plugin-stage/darwin-arm64/stage-evidence.json`
- validation mode：`CROSS_HOST_STATIC_ONLY`

该 stage 只证明 manifest/MCP 布局、Node/Go/native addon 的 Mach-O arm64 header、launcher shebang/path 和预期 `0755` 清单。Windows/NTFS 不能证明 macOS 解包后的实际 executable bit，也不能执行目标 runtime。因此下列项目全部为 `NOT TESTED`：

- Apple Silicon 原生 Node、Go、native addon、CLI 和 MCP 执行；
- 实际 executable mode；
- 完整 Plugin 最终 archive；
- `drag-gui`；
- Developer ID signing；
- notarization、stapling 与 Gatekeeper；
- Apple Silicon 实机索引。

## 已知限制与发布状态

- 图片型 PDF 只标记 `needs_ocr`，未内置 OCR。
- `.xls`、PDF 和 typed XLSX native failure 使用 TypeScript compatibility fallback；该文件的 extract/date/chunk draft 在 TS，SQLite/FTS writer 在 Go。
- XLSX 保留 formula 与 cached value，但不执行或重算公式。
- 可选 Ollama embedding 未纳入本轮门禁；默认离线词法检索不依赖它。
- 当前 Windows Go core、Plugin 内 Go core 与 `0.2.3` GUI 均未做 Authenticode 签名。
- 当前 full report 绑定了明确 Go/host bundle hash，但报告生成时 Git worktree 仍为 detached dirty 状态。commit、push、Plugin 发布和 GUI 发布均是独立授权；最终发布记录还应补冻结后的 commit/source manifest 与命令日志。
