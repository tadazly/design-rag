# 发布失败与恢复

先读取远端事实，再决定恢复动作。不得为了重跑方便删除或强制移动 tag。

| 已完成状态 | 恢复方式 |
|---|---|
| 尚未 commit | 修复工作区，重跑本地门禁 |
| commit 未 push | 修复后 amend 仅在用户授权且 commit 从未公开时允许；否则新增修复提交 |
| push 完成、CI 失败 | 新增修复提交并重新走 CI；旧失败 run 不作为发布依据 |
| CI 成功、release workflow 未开始 | 使用同一已验证 source SHA 重试 dispatch |
| 单平台构建失败 | 修复源码需新 commit/CI；瞬时 runner/下载失败可对同一 SHA 重跑失败 job |
| tag 已创建、Release 未创建 | 验证 tag tree 和证据；完全正确时只恢复 Release，错误时停止并发布新 patch 版本，禁止移动 tag |
| Release 创建、资产不完整 | 对同一正确 tag 补齐缺失资产并重新核验 checksum；不替换已有同名但内容不同的资产 |
| Release 完成、通知 job 失败 | 只重试 `notify-s-plugins` job，不重建产物、不重打 tag |
| dispatch 成功、下游 run 失败 | 修复 `s-plugins` 接收端后重新发送同一幂等 payload；先核对 marketplace 是否已更新 |
| 下游 run 成功但未产生 commit | 若 marketplace 已是目标 ref，则视为幂等 PASS；否则标记 FAIL |

每次恢复前核对：`origin/main` SHA、tag commit、Release assets、上游 workflow、dispatch 接受结果、下游 workflow、`s-plugins/main` SHA 和 marketplace 条目。若状态彼此矛盾，标记 `BLOCKED` 并请求用户决定，不做破坏性清理。
