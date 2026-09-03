---
name: design-rag-release
description: 发布 DRAG（Design-RAG）新版本。当用户要求“发布新版本”“发版”或更新 design-rag Plugin 与桌面 Release 时使用；负责 SemVer 推断、CHANGELOG、版本同步、发布门禁、提交与 origin push、跨平台 CI、tag、GitHub Release、s-plugins repository dispatch 和远端核验。不用于普通构建、日常提交或仅查看版本。
---

# DRAG 发布

把一次发布视为可恢复的状态机，不把提交、push、tag、Release 或跨仓库更新混成一个不可核验的动作。

## 开始前

1. 完整读取 [references/release-runbook.md](references/release-runbook.md)。
2. 需要推断版本时，再读取 [references/versioning.md](references/versioning.md)。
3. 发布中断或部分成功时，再读取 [references/failure-recovery.md](references/failure-recovery.md)。
4. 先执行 `node .agents/skills/design-rag-release/scripts/release-state.mjs`；这是只读预检，不得跳过失败项。

## 授权边界

- 用户说“发布新版本”表示启动发布预检，不自动授权外部写入。
- 预检后一次列出并确认：精确版本、CHANGELOG 摘要、release commit、push `origin/main`、创建 `vX.Y.Z` tag、发布 GitHub Release，以及发送会使 `tadazly/s-plugins` 自动更新 `main` 的 `repository_dispatch`。
- 一次确认可以覆盖上述完整且未变化的计划；计划、版本、目标仓库或资产集合变化时必须重新确认。
- 不向 `gitlab` push，不创建或移动已有 tag，不覆盖已有 Release，不直接向 `s-plugins` 写 Git；marketplace 修改由其已审核的接收 workflow 完成。

## 执行原则

- 使用 `origin` 获取和推送；发布前 `fetch --prune --tags`，要求本地 `main` 与 `origin/main` 无分叉。
- 版本必须在仓库、lockfile、Plugin、Go backend 和 GUI 中一致，并通过 `npm run verify:release`。
- `CHANGELOG.md` 只写用户可感知变化、兼容性和迁移提示；不得把原始 commit 列表直接当发布说明。
- 先提交并推送源码发布提交，再等待该 SHA 的 `CI` 成功。
- 跨平台产物必须由目标平台原生 runner 构建。发布 workflow 接收已验证的源码 SHA，构建 Windows x64 与 macOS arm64 Plugin/GUI，最后才创建不可变 tag 和 Release。
- tag 对应的树必须包含 `plugins/design-rag/bin/drag.exe` 与 `plugins/design-rag/bin/drag`，以便 `git-subdir` marketplace 安装后直接启动；`main` 保持源码树，不长期纳入二进制。
- GitHub Release 至少包含两个 Plugin ZIP、Windows GUI、macOS GUI、`SHA256SUMS.txt` 和发布说明。
- Release 完成并验证后，才使用 `S_PLUGINS_DISPATCH_TOKEN` 发送 `plugin-released` 事件；`ref` 必须由发布输入拼成 `vX.Y.Z`，不得在 `workflow_dispatch` 中误用值为分支名的 `github.ref_name`。
- 所有结论按 `PASS / FAIL / BLOCKED / NOT TESTED` 报告，并附远端 SHA、tag target、workflow run、Release URL、资产清单、dispatch 结果、下游 workflow run 与 marketplace `main` SHA。

## 禁止事项

- 不根据工作区中的无关改动自动扩大发布范围。
- 不在 CI 产物完成前创建 tag；tag 创建后不得用 force push 修正。
- 不因 Release 已创建而忽略资产、checksum、binary build-info 或 Plugin 运行 smoke 失败。
- 不把 macOS 交叉编译当作 Apple Silicon 原生运行、签名或 notarization 证据。
- 不在 commit、tag、Release Notes、workflow artifact 或 PR 中写入内部域名、内部邮箱、资料盘路径或旧品牌词。
- 不输出、读取或记录 Secret 值；只检查 Secret 名称是否存在。
