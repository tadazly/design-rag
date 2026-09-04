# 更新日志

本文件记录 DRAG 面向使用者的功能、兼容性和发布变化。开发过程中的内部实现细节不单独列出。

## [Unreleased]

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
