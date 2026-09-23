# DRAG Plugin staging

Plugin 技术 ID 为 `design-rag`，用户可见名称为 `DRAG 游戏策划知识库`。同一个 Plugin 目录同时是 Codex Plugin（`.codex-plugin/plugin.json` + `.codex-mcp.json`）与 Claude Code Plugin（`.claude-plugin/plugin.json`，内联 MCP 配置），两者共用 Skill 与 binary。单平台 ZIP 只包含对应平台的纯 Go `drag` binary、两个 host 的 manifest、MCP 配置、Skill 与第三方声明；不包含 Node、JavaScript、`node_modules`、Electron 或独立 launcher。供 `git-subdir` marketplace 使用的正式 tag 树同时包含 Windows `drag.exe` 与 macOS `drag`，但不会把这两个 binary 合并回 `main`。

插件根目录不得出现 `.mcp.json`：Claude Code 会自动加载它，并按用户项目目录解析其中的相对 command。Codex 配置只能放在 `.codex-mcp.json`，Claude 配置只能内联在 `.claude-plugin/plugin.json`。

## 隔离验收

Windows 开发机可先生成带测试身份的 stage：

```powershell
npm run plugin:stage:win:go-test
```

该模式只写入 `tests/.tmp/plugin-stage-go-test/win32-x64`，并同时隔离：

- Plugin：`design-rag-go-test`
- marketplace：`design-rag-go-test-local`（Codex 与 Claude Code 各一份）
- MCP server：`design-rag-go-test`
- Skill：`game-design-rag-go-test`
- 配置与索引：通过 `DESIGN_RAG_STATE_NAMESPACE=design-rag-go-test` 使用独立的系统配置/数据目录，不写 Plugin cache

测试 identity 只存在于生成目录。源码与正式 stage 必须保持原名；正式 stage 的 fail-closed 校验会拒绝 manifest、MCP、Skill 或 marketplace 中残留的 `go-test`。

Claude Code 真实会话验收可直接加载隔离 stage，不安装到任何 scope：

```powershell
claude plugin validate tests/.tmp/plugin-stage-go-test/win32-x64/design-rag-go-test-local --strict
claude --plugin-dir tests/.tmp/plugin-stage-go-test/win32-x64/design-rag-go-test-local/plugins/design-rag-go-test
```

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
- `.codex-mcp.json` 直接执行上述 binary，唯一参数为 `mcp`；Claude manifest 使用 `${CLAUDE_PLUGIN_ROOT}/bin/drag mcp`，不带 `cwd`；
- 插件根目录没有 `.mcp.json`，marketplace 根目录同时有 `.agents/plugins/marketplace.json` 与 `.claude-plugin/marketplace.json`；
- Skill `allowed-tools` 恰好是 6 个只读工具的 Claude Code 调用名；
- `nodeArtifactCount=0`；
- 匹配当前宿主时，CLI version 通过；隔离 `go-test` stage 还必须分别按 Codex 配置（cwd 为插件根目录）与 Claude 配置（替换 `${CLAUDE_PLUGIN_ROOT}`，cwd 在插件之外）启动真实 MCP，读取 3 个 resources、列出并调用全部 13 个 tools；
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

## GitHub Release

仓库级 `design-rag-release` Skill 负责版本推断、CHANGELOG、授权、源码提交和远端验收；`.github/workflows/release.yml` 负责原生构建、tag 发布树、Release 资产，以及在发布成功后向 `s-plugins` 发送 `plugin-released` repository dispatch。发布顺序固定为：源码 CI 通过 → 两个平台原生构建与 smoke → 组装双 binary tag 树 → 创建 tag → 发布 Release → 下游幂等更新 marketplace。已有 tag 不得移动或覆盖。

`s-plugins` 的 Claude Code marketplace（`.claude-plugin/marketplace.json`）由接收端从同一 Codex 条目自动生成，发布通知无需额外字段；生成的条目以 `git-subdir`（`path` 为 `plugins/design-rag`、`ref` 为 `vX.Y.Z`）指向 tag 树，因此 tag 树必须满足两份 manifest 同名同版本、Claude MCP 内联并使用 `${CLAUDE_PLUGIN_ROOT}`、根目录没有 `.mcp.json`。
