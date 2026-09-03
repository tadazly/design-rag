# DRAG Plugin staging

Plugin 技术 ID 为 `design-rag`，用户可见名称为 `DRAG 游戏策划知识库`。分发物只包含一个目标平台纯 Go `drag` binary、manifest、MCP 配置、Skill 与第三方声明；不包含 Node、JavaScript、`node_modules`、Electron 或独立 launcher。

## 隔离验收

Windows 开发机可先生成带测试身份的 stage：

```powershell
npm run plugin:stage:win:go-test
```

该模式只写入 `tests/.tmp/plugin-stage-go-test/win32-x64`，并同时隔离：

- Plugin：`design-rag-go-test`
- marketplace：`design-rag-go-test-local`
- MCP server：`design-rag-go-test`
- Skill：`game-design-rag-go-test`
- 配置与索引：通过 `DESIGN_RAG_STATE_NAMESPACE=design-rag-go-test` 使用独立的系统配置/数据目录，不写 Plugin cache

测试 identity 只存在于生成目录。源码与正式 stage 必须保持原名；正式 stage 的 fail-closed 校验会拒绝 manifest、MCP、Skill 或 marketplace 中残留的 `go-test`。

## 正式 stage

```powershell
npm run plugin:validate
npm run plugin:stage:win
npm run plugin:stage:mac
npm run plugin:test:stages
```

每个 stage 必须满足：

- Windows：`plugins/design-rag/bin/drag.exe` 为 PE x64；
- macOS：`plugins/design-rag/bin/drag` 为 Mach-O arm64，ZIP mode 固定为 `0755`；
- `.mcp.json` 直接执行上述 binary，唯一参数为 `mcp`；
- `nodeArtifactCount=0`；
- 匹配当前宿主时，CLI version 通过；隔离 `go-test` stage 还必须从 staged `.mcp.json` 启动真实 MCP，读取 3 个 resources、列出并调用全部 13 个 tools；
- 正式 `design-rag` stage 不在开发机启动 MCP，避免与已安装同名 Plugin 混淆；其 runtime 门禁由隔离 `go-test` stage 承担，正式 metadata 另做 fail-closed 原名检查；
- 非匹配宿主只可提供静态目标证据，runtime 必须保持 `NOT_TESTED`。

最终 archive 仍要求目标原生 runner：

```powershell
npm run plugin:pack:win
```

```bash
npm run plugin:pack:mac
```

stage、最终 archive、安装、commit、push 与发布是独立动作。Windows stage PASS 不代表 macOS 实机、codesign、notarization、Gatekeeper 或最终发布 PASS。
