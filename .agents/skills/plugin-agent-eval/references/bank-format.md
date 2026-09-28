# 题库格式

题库是一个本地目录，默认放在仓库中被忽略的 `tests/.tmp/question-bank/`。目录里含真实语料内容，不得提交。

```
<bank>/
  bank.json          题目清单
  refs/<id>.md       标准答案
  runs/<tag>/        每轮评测一个目录
  config.json        本机配置：被测版本、模型、路径（不含密钥）
```

## bank.json

```json
{
  "format": "agent-eval-bank/v1",
  "questions": [
    {
      "id": "demo-reward-split",
      "query": "示例活动一局失败后，已获得的道具会怎么处理？",
      "set": "2026-01-fresh",
      "author": "独立出题会话（模型名）",
      "createdAt": "2026-01-15",
      "documents": ["doc_0000000000000000000000a1"],
      "notes": "考失败结算与放弃流程的区别",
      "status": "active",
      "ref": "refs/demo-reward-split.md"
    }
  ]
}
```

| 字段 | 说明 |
|---|---|
| `id` | 小写字母、数字和短横线，全库唯一；发布后不再改 |
| `query` | 交给作答方的题面，原样使用 |
| `set` | 题组名，同一次出题的题目共用一个题组 |
| `author` | 出题方，写模型或会话，不写个人信息 |
| `createdAt` | 出题日期 `YYYY-MM-DD` |
| `documents` | 标准答案依据的文档 ID。出新题时用它排除已用文档；可用 `bank.py match-refs` 补全 |
| `notes` | 可选，考点或选题理由 |
| `status` | `active` 参加抽题；`needs-fix` 标准答案待修订；`retired` 不再使用 |
| `ref` | 标准答案的相对路径 |
| `extra` | 可选，其他结构化期望，如检索层的必中文档 |

## 标准答案 `refs/<id>.md`

依次包含以下几节，缺节时 `bank.py check` 会提示：

- `## 真值要点`：逐条列出，每条注明出处文档和定位（页签、行号、段落等）。
- `## 判错条件`：会被判为关键错误的说法。
- `## 原文摘录`：支撑答案的原文，逐行摘录。排查退步时会从这里取关键原文作标记。
- `## 评分说明`：可选。写明哪些要点可以不同表述，哪些不确定内容不算真值。

## 轮次 `runs/<tag>/`

```
round.json                      轮次说明
<side>/<id>-r<rep>.answer.md    作答方的最终答案
<side>/<id>-r<rep>.session.jsonl  会话事件日志
metrics.jsonl                   每个会话一行：耗时、token、工具调用、错误
judge.jsonl                     每题每次重复一行：X/Y 映射、双方评分、胜方、理由
```

`round.json` 示例：

```json
{
  "tag": "2026-01-15-fix-retest",
  "date": "2026-01-15",
  "purpose": "修复后复测：新题 4 道 + 旧题随机 4 道（seed 20260115）",
  "sides": {"A": "发布版 v1.2.0", "B": "修复分支工作树"},
  "answer": {"agent": "codex exec", "model": "<模型>", "effort": "medium"},
  "judge": {"model": "<模型>"},
  "questions": ["demo-reward-split"],
  "reps": [1]
}
```

`judge.jsonl` 每行中，`scores.<side>` 的字段为 `target`（是否找准目标）、`correctness`、`completeness`（0–5），以及 `errors`、`missing` 两个列表。`preferred` 为胜方版本名或 `tie`。

## 本机配置 `config.json`

```json
{
  "server": "demo-rag",
  "sides": {
    "A": {"command": "D:/eval/A/bin/server.exe", "args": ["mcp"], "env": {"DEMO_DATA_DIR": "D:/eval/state"}},
    "B": {"command": "D:/eval/B/bin/server.exe", "args": ["mcp"], "env": {"DEMO_DATA_DIR": "D:/eval/state"}}
  },
  "answer": {
    "model": "<作答模型>",
    "effort": "medium",
    "cwd": "D:/eval/empty-cwd",
    "timeoutSeconds": 1800,
    "prompt": "可选，覆盖默认作答提示；必须包含 {server} 与 {question}"
  },
  "judge": {"model": "<评审模型>", "cwd": "D:/eval/judge-cwd", "questionPrefix": "请基于本地知识库回答："},
  "corpus": {"index": "D:/eval/state/index.sqlite"},
  "retrievalTools": ["search", "retrieve"]
}
```

- `sides` 的键就是版本名，会出现在轮次目录和评分结果里。
- `retrievalTools` 是插件的检索类工具名，`replay.py session` 默认重放这些调用。
- 各版本共用同一份只读索引快照，避免语料差异混进比较。
- 作答和评审的工作目录都应是空目录，会话读不到题库和仓库。
