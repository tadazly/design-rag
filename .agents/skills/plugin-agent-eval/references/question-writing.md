# 独立出题

出题方是一个独立会话。它只能只读浏览语料全文，自行选案例出题，并记录经原文核实的标准答案。

## 准备出题目录

出题目录只放出题需要的东西，与仓库和题库隔离：

```
python scripts/bank.py --bank <bank> writer-kit --out <出题目录> --count 4 --prefix fresh [--scope "<选题范围>"]
```

`--scope` 只写选题范围（例如某一类文档），默认是“全部文档”。出题目录不能在题库里，也不能反过来包含题库，建议放在仓库之外。

生成的目录结构：

- **`tools/corpus.py`**：只读语料浏览器，只有 `stats`、`list`、`show`、`grep` 四个命令。它以只读模式打开索引，不写任何文件。会写题库的命令都在 `bank.py` 里，不进出题包。
- **`tools/corpus-config.json`**：索引路径，以及题库已用文档的 ID。`list` 和 `show` 会把已用文档标为“已用”。
- **`prompt.md`**：出题任务说明，包括题目数量、排除规则和输出格式。
- **`out/`**：出题方唯一可写的目录，产出 `out/questions.json` 与 `out/refs/<id>.md`。

`tools/` 下的文件和 `prompt.md` 都设为只读。

生成后读一遍 `prompt.md`。只允许调整数量、日期和输出格式。不要加入题型、主题或目标文档的提示，否则题目就不再独立。需要改时，先取消文件的只读属性，改完再恢复。

## 启动出题会话

在出题目录里运行 `writer-kit` 打印的命令。权限在 `bank.py` 的 `WRITER_ALLOWED_TOOLS` 里定义，不要手工放宽：

```
claude -p --model <出题模型> --effort <推理强度> --strict-mcp-config --permission-mode dontAsk \
  --allowedTools "Bash(python -I tools/corpus.py stats:*)" "Bash(python -I tools/corpus.py list:*)" \
    "Bash(python -I tools/corpus.py show:*)" "Bash(python -I tools/corpus.py grep:*)" \
    "Read(./**)" "Edit(./out/**)" "Write(./out/**)" \
  --disallowedTools WebFetch WebSearch Agent Task NotebookEdit Skill \
  --output-format stream-json --verbose < prompt.md > writer.log
```

**权限的设计**
- 能执行的只有 `tools/corpus.py`，而可写范围只有 `out/`，两者不重叠。出题方无法先改脚本再执行它。
- `python -I` 不把脚本目录和当前目录加入模块搜索路径，同名模块也就无法顶替标准库。
- `dontAsk` 模式会直接拒绝未放行的命令，例如 `python -c`。

**运行**
- 用户指定了出题模型时照用；未指定时用能力最强的模型和较高推理强度。
- 会话在后台运行。完成的标志是：`out/questions.json` 和 `out/refs/*.md` 都已写出，并且日志里出现最终结果事件。
- 出题期间不要回答它的问题，也不要补充提示。卡住或失败时，换一个新的出题目录重跑。

## 入库前检查

1. `out/questions.json` 格式正确，题数对，id 唯一，且不与题库重复。
2. 抽查每题 1–2 条真值要点：用 `python -I tools/corpus.py show <文档> --start <分块>` 回到原文核对定位和数值。
3. 题面不泄露答案，也不依赖出题方才知道的背景。
4. 每个标准答案都有“真值要点”“判错条件”“原文摘录”三节。

发现问题时，把具体问题交回同一出题目录的新会话修订，权限不变。你自己不改题面和要点，只修格式错误。

## 入库

```
python scripts/bank.py --bank <bank> add --from <出题目录> --set <题组名> --author "<出题方>"
python scripts/bank.py --bank <bank> match-refs
python scripts/bank.py --bank <bank> check
```

- `add` 先核对 `tools/corpus.py` 与技能里的原件逐字节一致；被改动过的出题包会被拒绝。
- 然后从 `out/` 读取题目，把 `out/refs/*.md` 复制进题库，并把题目的 `case.documentId` 记入 `documents`。
- `match-refs` 再从标准答案里补齐其他依据文档。
