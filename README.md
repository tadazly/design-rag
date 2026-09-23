# DRAG（Design-RAG）

DRAG 是面向游戏策划案、配置表和历史版本的本地知识库。它可以帮助策划、开发和运营快速查找玩法、流程、奖励产出、配表字段、历史改动和可复用方案。

## 快速开始：Codex Plugin

最简单的安装方式是通过 [S Plugins 的“安装与使用”说明](https://github.com/tadazly/s-plugins#安装与使用) 安装 `DRAG 游戏策划知识库`。marketplace 的添加、安装和更新步骤以该仓库为准，本仓库不重复维护。

安装完成后新建 Codex 任务，直接添加本机资料来源：

```text
将 D:\Workspace\GameProject\design-docs 加入来源，类型是策划案；
将 D:\Workspace\GameProject\table\excel 加入来源，类型是配置表。
添加后等待增量索引结束，并报告失败文件。
```

之后可以直接描述问题或维护要求，例如：

- `查找最新的节日活动，说明玩法、奖励产出和所需配表，并附原文件位置。`
- `新增一个抽奖活动需要配置哪些表格？`
- `增量更新所有来源的索引；发现 Git 或 SVN 仓库时先询问我是否更新。`
- `更新 D:\Workspace\GameProject 的仓库，然后增量更新其中的来源。`

新增来源会自动执行增量索引。来源目录必须是绝对路径，不能使用相同目录或互为父子目录。明确要求“更新仓库并更新索引”时，Codex 不会重复口头确认，但仍会遵守必要的工具 approval。

## 快速开始：Claude Code Plugin

同一个 Plugin 同时提供 Claude Code 版本，Skill、MCP 工具与 CLI 和 Codex 版一致，两边共用同一份来源配置和本地索引。

1. 从 [GitHub Releases](https://github.com/tadazly/design-rag/releases) 下载对应平台的 `design-rag-local-<版本>-<平台>.zip`，解压到固定目录。Claude Code 直接从该目录加载 Plugin，移动或删除目录后需要重新添加。
2. 添加本地 marketplace 并安装，然后新开 Claude Code 会话：

   ```bash
   claude plugin marketplace add <解压后的 design-rag-local 目录>
   claude plugin install design-rag@design-rag-local
   ```

3. 与 Codex 一样直接描述问题或维护要求，例如上面的示例。

Claude Code 默认会在首次调用 Skill 和每个检索工具时请求确认。只读检索需要长期免确认时，在 `/permissions` 或 settings 的 `permissions.allow` 中加入以下规则；来源、索引和缓存管理工具保持逐次确认：

```json
[
  "Skill(design-rag:game-design-rag)",
  "mcp__plugin_design-rag_design-rag__drag_search",
  "mcp__plugin_design-rag_design-rag__drag_retrieve",
  "mcp__plugin_design-rag_design-rag__drag_read_citation",
  "mcp__plugin_design-rag_design-rag__drag_list_versions",
  "mcp__plugin_design-rag_design-rag__drag_sources",
  "mcp__plugin_design-rag_design-rag__drag_index_status"
]
```

## 主要功能

- 检索策划案、配置表和历史版本。
- 按有效更新日期优先返回较新资料。
- 支持模糊查找、精确 ID、表格字段和文档范围过滤。
- 返回可回读的文件路径、页码、行号或工作表区域。
- 支持多个策划案和配表目录，可随时启用、停用或删除来源。
- 支持增量索引、更新进度、暂停、继续和缓存清理。

## 支持的文件

- Word：DOCX
- 表格：XLSX、XLSM、XLS、CSV
- 文档：PDF、XMind
- 文本：Markdown、TXT、HTML、JSON、YAML

## 其他使用方式

- 桌面客户端：从 [GitHub Releases](https://github.com/tadazly/design-rag/releases) 下载对应平台的 GUI，用于管理来源、查看证据和进行多会话分析。
- CLI：通过 `drag` 管理来源、索引和检索；运行 `drag --help` 查看完整命令。
- MCP：运行 `drag mcp`，向支持 MCP 的客户端提供检索、引用和索引管理工具。

CLI 示例：

```powershell
drag sources add --id design-docs --label "策划案" --kind design --path "D:\Workspace\GameProject\design-docs" --json
drag index
drag search "幸运轮盘的历史方案" --sort newest --limit 10 --json
```

## 源码开发与验证

源码开发需要 Node.js 24 和 Go 1.26：

```powershell
npm install
npm test
npm run check
npm start
```

验证或构建 Codex / Claude Code Plugin：

```powershell
npm run plugin:validate
npm run plugin:stage:win
npm run plugin:stage:mac
```

## 本地数据

DRAG 不会修改策划案或配置表源文件。配置、索引和桌面会话保存在本机：

| 平台 | 配置 | 索引与会话 |
|---|---|---|
| Windows | `%APPDATA%\DesignRag` | `%LOCALAPPDATA%\DesignRag` |
| macOS | `~/Library/Application Support/DesignRag/config` | `~/Library/Application Support/DesignRag/data` |

也可以通过环境变量指定位置：

```text
DESIGN_RAG_CONFIG_DIR
DESIGN_RAG_DATA_DIR
```

## License

DRAG 使用 Apache License 2.0。详见 [LICENSE](LICENSE) 和 [NOTICE](NOTICE)。第三方项目说明见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
