# 更新日志

本文件记录 DRAG 面向使用者的功能、兼容性和发布变化。开发过程中的内部实现细节不单独列出。

## [Unreleased]

## [0.3.0] - 2026-09-03

### 新增

- 提供 Codex Plugin、MCP、CLI 和桌面客户端四种使用方式。
- Plugin 使用单一纯 Go runtime，支持 Windows x64 与 Apple Silicon macOS。
- 支持本地策划案、配置表、历史版本检索和可回读引用。

### 变更

- 统一品牌为 DRAG（Design-RAG），技术 ID 为 `design-rag`，CLI/runtime 为 `drag`。
- 公开发布统一使用 GitHub 仓库与 Apache License 2.0。
