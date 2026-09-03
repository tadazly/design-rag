# DRAG 发布运行手册

## 输入与输出

输入允许只给“发布新版本”。可选输入包括精确版本、是否允许预发布、已知 breaking change 和发布说明补充。当前正式分发只接受严格 `x.y.z`，tag 为 `vX.Y.Z`。

最终输出必须包含：版本推断理由、变更摘要、门禁结果、源码 commit、`origin/main` 远端核验、CI run、tag target、Release URL 与资产、checksum、签名状态、dispatch 结果、`s-plugins` 下游 run 与 `main` SHA，或明确 blocker。

## 阶段 0：只读预检

1. 读取 `AGENTS.md`、`package.json`、`CHANGELOG.md`、`.github/workflows/ci.yml`、`.github/workflows/release.yml`、`electron-builder.yml`、`scripts/verify-release.mjs` 和 Plugin manifest。
2. 运行 `git status --short --branch`、`git remote -v`、`git fetch origin --prune --tags`、`git rev-list --left-right --count origin/main...HEAD`。
3. 运行 `node .agents/skills/design-rag-release/scripts/release-state.mjs`。
4. 用 `gh` 核对目标 tag、同名 Release 和源码 SHA 的 CI；核对 `tadazly/s-plugins` 的 `main` 包含接收 `plugin-released` 的 `.github/workflows/update-marketplace.yml`，并检查 `design-rag` 仓库存在名为 `S_PLUGINS_DISPATCH_TOKEN` 的 Secret。只检查名称，不读取值。
5. `s-plugins` 初始 `plugins` 数组允许为空，接收端会幂等 upsert。工作区有无关改动、分叉、版本源不一致、已有同名 tag/Release、发布 workflow 缺失、接收事件契约不一致或 Secret 名称缺失时，不进入写阶段。

## 阶段 1：推断与准备版本

1. 按 [versioning.md](versioning.md) 选择候选版本，显式版本优先。
2. 生成面向用户的 CHANGELOG 草稿；逐项核对真实 diff，不把 commit 标题原样堆叠。
3. 给用户展示一次性授权清单。确认后执行：
   - `node .agents/skills/design-rag-release/scripts/set-version.mjs X.Y.Z`
   - 将 `[Unreleased]` 的本次内容移动为 `## [X.Y.Z] - YYYY-MM-DD`，并恢复空的 `[Unreleased]`。
4. 再运行 `release-state.mjs`，要求版本矩阵一致。

版本矩阵：

| 范围 | 权威位置 |
|---|---|
| 仓库 / Electron package | `package.json`、`package-lock.json` 根 |
| Plugin | `plugins/design-rag/.codex-plugin/plugin.json` |
| Go CLI / MCP / backend | `go/core/model.go` 的 `BackendVersion` |
| GUI | `src/shared/contracts.ts` 的 `APP_VERSION` |
| 协议 | `ProtocolVersion`，仅协议不兼容时独立调整，不等于产品 SemVer |

## 阶段 2：本地门禁

依次执行，任何失败都停止：

```powershell
npm run verify:release
npm test
npm run check
npm run plugin:validate
git diff --check
```

协议、app-server、UI 有变化时按 `AGENTS.md` 加跑对应门禁。发布 Skill 或 workflow 变化至少运行 Skill validator、workflow 语法检查和发布状态脚本。

检查暂存区只包含本次发布文件，并扫描 commit message 与 CHANGELOG。发布提交使用中性信息，例如 `chore(release): prepare vX.Y.Z`。

## 阶段 3：源码提交、push 与 CI

1. 创建一个源码发布提交；它包含版本、CHANGELOG 及本次待发布代码，不包含 `release/` 或 Plugin binaries。
2. push 精确 commit 到 `origin/main`，不向 `gitlab` push。
3. 读取远端 `main` SHA，要求等于本地 HEAD；记录 SHA。
4. 等待该 SHA 的 `CI` 完成且为 success。不要用其他提交、旧 run 或本地测试代替。

## 阶段 4：发布 workflow

触发 `.github/workflows/release.yml`，显式传入 `version=X.Y.Z` 与 `source_sha=<40-char SHA>`。workflow 必须：

1. 再次确认 `source_sha` 是当前 `origin/main`、版本矩阵为 `X.Y.Z`，且目标 tag/Release 不存在。
2. Windows x64 原生 runner：构建和运行 Plugin CLI/MCP smoke，生成 Plugin ZIP；构建 GUI 可分发 EXE；扫描 Go build-info 与敏感字符串。
3. Apple Silicon 原生 runner：构建和运行 Plugin CLI/MCP smoke，生成 Plugin ZIP；构建 GUI ZIP/DMG；扫描 build-info。签名、notarization、stapling 未配置时必须在 Release 和最终报告中明确标为 unsigned / not notarized。
4. 汇总两个原生 stage，构造以 `source_sha` 为父提交的发布树，仅在该树加入两个 Plugin binary 与随包许可证。验证通用 `.mcp.json`、PE x64、Mach-O arm64、可执行 mode、版本和敏感词。
5. 创建 annotated tag `vX.Y.Z` 指向该发布树提交；不得把该提交合并或 push 到 `main`。
6. 生成 `SHA256SUMS.txt`，从 CHANGELOG 抽取该版本说明，创建 GitHub Release 并上传资产。
7. 上传证据 JSON，至少记录 source SHA、tag commit、每个 binary/archive 的 SHA-256、平台运行 smoke 和签名状态。

## 阶段 5：远端验收

不要只相信 workflow 绿色。逐项核对：

- `origin/main` 仍指向源码发布提交，工作树不含 `plugins/design-rag/bin`。
- `refs/tags/vX.Y.Z^{commit}` 与 workflow 报告一致，tag tree 同时包含两种 Plugin binary。
- manifest、Go `--version --json`、GUI/package 与 tag 都是 `X.Y.Z`。
- Release 不是 draft，资产名称唯一、大小非零，下载后的 SHA-256 与清单一致。
- Windows Plugin 的真实 MCP stdio smoke 为 PASS；macOS Plugin 的原生 CLI/MCP smoke 为 PASS。
- GUI 构建完成；未执行的 GUI 启动、签名或 notarization 只能标为 `NOT TESTED`，不能写成 PASS。

## 阶段 6：通知并验收 s-plugins

此阶段只能在 Release 验收通过后执行。

1. Release workflow 的独立 `notify-s-plugins` job 使用 `S_PLUGINS_DISPATCH_TOKEN`。该 fine-grained PAT 只授权 `tadazly/s-plugins`，并具有 `Contents: Read and write`；不得把 token 写入日志、artifact 或 payload。
2. `PLUGIN_REF` 固定为 `v${{ inputs.version }}`。当前发布 workflow 由 `workflow_dispatch` 启动，因此 `github.ref_name` 是启动分支而不是发布 tag，禁止用它生成通知 ref。
3. 向 `POST repos/tadazly/s-plugins/dispatches` 发送：

```json
{
  "event_type": "plugin-released",
  "client_payload": {
    "name": "design-rag",
    "url": "https://github.com/tadazly/design-rag.git",
    "path": "./plugins/design-rag",
    "ref": "vX.Y.Z",
    "category": "Productivity"
  }
}
```

4. `gh api` 返回成功只证明 dispatch 已被 GitHub 接受。随后等待 `s-plugins` 的 `Update plugin marketplace` repository-dispatch run，要求 conclusion 为 success。
5. 读取下游最新 `main` SHA 和 `.agents/plugins/marketplace.json`，要求唯一 `design-rag` 条目使用 `git-subdir`、公开仓库 URL、`./plugins/design-rag` 与 `vX.Y.Z`。重复通知同一 ref 可以不产生新 commit，但最终条目必须一致。
6. 接收端已经负责 payload 校验、幂等 upsert、仓库校验和直接提交 `main`；上游不再创建分支或 PR，也不重复实现 marketplace 写入逻辑。

若 Secret 缺失、dispatch 被拒绝、下游 run 失败或最终 marketplace 不一致，将此阶段标记为 `FAIL` 或 `BLOCKED`。不要回滚已经验收通过的主仓库 Release；只重试独立通知 job。不得发送虚构版本进行连通性测试，因为接收端把事件视为上游已完成发布验收。

## 阶段 7：最终报告

按阶段给出 `PASS / FAIL / BLOCKED / NOT TESTED`。如果 Release 成功而 marketplace 同步失败，应明确写“版本已发布，订阅源尚未更新”，并给出只重试 `notify-s-plugins` job 的方式。
