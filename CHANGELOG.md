# 更新日志

本文件记录 DRAG 面向使用者的功能、兼容性和发布变化。开发过程中的内部实现细节不单独列出。

## [Unreleased]

### 改进

- 检索按关键词概念计算相关度：自动去掉“我要”“有哪些”等提问词，实体名、系统名这类稀有词优先于“配置”“表格”等泛化词，较早但直接相关的策划和配表不再被截断在候选之外。
- 命中的策划会带上更相关的页：问“产出逻辑”时，证据包包含“奖励数值”页，不再被各个“面板&逻辑”页占满。
- 配表所在目录名和名称列单元格命中计为强信号；“需要配哪些表”类问题按相关度挑选配表，频繁更新的汇总表不再挤掉专用表。
- 活动身份匹配区分独立名称与更长名称，例如“晨星888”优先命中“晨星·守望者888活动”，不再被更新的“破晨星·裂空888活动”抢先。
- MCP 检索结果改为面向模型的紧凑格式，证据按文档分组，去掉重复元数据；同样的问题返回体积约减少三分之二，检索耗时约减半。

### 变更

- MCP `drag_retrieve` 结果改为 `drag_retrieval_bundle_v2`，引用链接字段为 `link`（内容与原 `sourceLink.markdown` 相同）。CLI `--json` 与桌面协议的完整结构不变。

## [0.4.0] - 2026-09-23

### 新增

- 新增 Claude Code Plugin：同一个 Plugin 同时支持 Codex 与 Claude Code，共用 Skill、MCP 工具、CLI、来源配置与本地索引。Release 中的 Plugin ZIP 解压后同时是两个宿主的本地 marketplace。
- 检索、证据包、引用回读和版本列表工具声明单次结果上限，Claude Code 中常规检索结果不再被转存为文件。

### 变更

- Codex Plugin 的 MCP 配置文件改名为 `.codex-mcp.json`；插件根目录不再包含 `.mcp.json`，避免 Claude Code 按用户项目目录启动错误的程序。
- Skill 改为宿主中立，分别说明 Codex 与 Claude Code 的 MCP 名称，并更正 citationId 格式说明。

### 升级提示

- Codex 用户更新 Plugin 后需重启 Codex 或新建任务；Claude Code 用户安装或更新后需新开会话才会加载 MCP server。

## [0.3.3] - 2026-09-04

### 修复

- 修复 Codex Plugin 安装后详情页“网站”显示不可用的问题，并增加源码、构建产物和发布流程的网站元数据防回归校验。

### 升级提示

- 更新或重新安装 Plugin 后需重启 Codex 或新建任务，才能加载新的 Plugin manifest。

## [0.3.2] - 2026-09-04

### 改进

- 完善 Codex Plugin 手动更新索引流程：在有限父目录范围内识别 Git/SVN，并在用户授权后先更新仓库再做增量索引。
- 将 Codex Plugin 安装和用法提前到快速开始，链接 S Plugins marketplace 说明，简化示例，并由每个 Release 的说明集中解释下载产物。
- GitHub Release Notes 会逐项说明 Plugin 与桌面安装包用途，并明确 Windows/macOS 的签名和公证状态。
- 精简 GitHub Release 下载项：保留两平台 Plugin 包、Windows GUI、macOS DMG 和校验和；发布 evidence 改为 Actions 审计产物，不再与用户安装包混列。

## [0.3.1] - 2026-09-03

### 修复

- 更新 `s-plugins` 发布通知参数：从实际 Plugin manifest 读取版本与展示信息，使用嵌套 `source` 结构，并由 marketplace 统一管理分类和安装策略。

## [0.3.0] - 2026-09-03

### 新增

- 提供 Codex Plugin、MCP、CLI 和桌面客户端四种使用方式。
- Plugin 使用单一纯 Go runtime，支持 Windows x64 与 Apple Silicon macOS。
- 支持本地策划案、配置表、历史版本检索和可回读引用。

### 变更

- 统一品牌为 DRAG（Design-RAG），技术 ID 为 `design-rag`，CLI/runtime 为 `drag`。
- 公开发布统一使用 GitHub 仓库与 Apache License 2.0。
