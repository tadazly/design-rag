# DRAG（Design-RAG）

DRAG 是面向游戏策划案、配置表和历史版本的本地知识库。它可以帮助策划、开发和运营快速查找玩法、流程、奖励产出、配表字段、历史改动和可复用方案。

项目提供四种使用方式：

- Codex Plugin：在 Codex 中直接检索和分析本地资料。
- MCP：向支持 MCP 的客户端提供检索、引用和索引管理工具。
- CLI：通过 `drag` 命令管理资料源、更新索引并查询资料。
- 桌面客户端：管理资料源、查看检索证据和进行多会话分析。

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

## 快速开始

源码开发需要 Node.js 24 和 Go 1.26。

```powershell
npm install
npm test
npm run check
```

启动桌面客户端：

```powershell
npm start
```

## 配置资料源

首次启动时资料源为空。可以在桌面客户端设置页中添加目录，也可以使用 CLI：

```powershell
go run .\go\cmd\drag sources add --id design-docs --label "策划案" --kind design --path "D:\DesignRag\examples\design-docs" --json
go run .\go\cmd\drag sources add --id config-tables --label "配表" --kind table --path "D:\DesignRag\examples\config-tables" --json
go run .\go\cmd\drag index
```

上述目录仅为示例，请替换为自己的本地路径。

## CLI 用法

```powershell
go run .\go\cmd\drag --version --json
go run .\go\cmd\drag doctor --json
go run .\go\cmd\drag sources list --json
go run .\go\cmd\drag index
go run .\go\cmd\drag search "幸运轮盘的历史方案" --sort newest --limit 10 --json
go run .\go\cmd\drag retrieve "这个活动需要哪些配表" --sort newest --max-documents 8 --json
go run .\go\cmd\drag status --json
```

构建后可将 `go run .\go\cmd\drag` 替换为 `drag`。

## Codex Plugin

Plugin 技术 ID 为 `design-rag`，显示名为 `DRAG 游戏策划知识库`。

```powershell
npm run plugin:validate
npm run plugin:stage:win
```

Apple Silicon Mac：

```bash
npm run plugin:validate
npm run plugin:stage:mac
```

Plugin 提供检索、引用回读、版本比较、资料源管理和索引管理工具。需要修改资料源或缓存的操作会在执行前请求确认。

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
