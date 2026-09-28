"""评测题库管理（只用标准库）。

用法：
  python bank.py --bank DIR init
  python bank.py --bank DIR check
  python bank.py --bank DIR list [--status active] [--set 题组]
  python bank.py --bank DIR add --from 出题目录 --set 题组 --author 出题方 [--date YYYY-MM-DD]   读取 out/，先核对浏览脚本未被改动
  python bank.py --bank DIR sample --n 4 --seed 种子 [--status active] [--exclude-set 题组,...] [--exclude id,...]
  python bank.py --bank DIR cases --ids a,b [--out 文件]      导出只含 id 与 query 的题单
  python bank.py --bank DIR used-docs                          题库已用文档 ID
  python bank.py --bank DIR history [--id 题目]                各轮次的评分与胜负
  python bank.py --bank DIR set-status 题目 active|needs-fix|retired [--note 说明]
  python bank.py --bank DIR match-refs [--index 索引]          从标准答案识别依据文档，补进 documents
  python bank.py --bank DIR writer-kit --out 出题目录 --count 4 [--prefix fresh] [--scope 范围] [--date YYYY-MM-DD] [--index 索引]

题库格式见 ../references/bank-format.md。索引默认取 config.json 的 corpus.index，只以只读方式打开。
会写题库的命令只由评测主持方运行；出题包里只有只读的 tools/corpus.py，出题方只能写 out/。
"""
import argparse
import datetime
import json
import os
import random
import re
import shlex
import shutil
import sqlite3
import stat
import sys

sys.stdout.reconfigure(encoding="utf-8")
SKILL = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FORMAT = "agent-eval-bank/v1"
ID_PATTERN = re.compile(r"[a-z0-9]+(?:-[a-z0-9]+)*")
STATUSES = ("active", "needs-fix", "retired")
REF_SECTIONS = ("## 真值要点", "## 判错条件", "## 原文摘录")
DOCUMENT_ID = re.compile(r"\bdoc_[0-9a-f]{24}\b")
MIN_TITLE_LENGTH = 8
# 出题会话的权限：只能用 python -I 运行只读的浏览脚本，只能读出题目录、写 out/。
# 浏览脚本在 tools/，不在任何可写范围内，出题方无法先改脚本再执行。
WRITER_BROWSER = "tools/corpus.py"
WRITER_ALLOWED_TOOLS = [f"Bash(python -I {WRITER_BROWSER} {command}:*)" for command in ("stats", "list", "show", "grep")] + [
    "Read(./**)", "Edit(./out/**)", "Write(./out/**)"]
WRITER_DISALLOWED_TOOLS = ["WebFetch", "WebSearch", "Agent", "Task", "NotebookEdit", "Skill"]


def load(bank):
    path = os.path.join(bank, "bank.json")
    if not os.path.exists(path):
        sys.exit(f"题库不存在：{path}（先运行 init）")
    data = json.load(open(path, encoding="utf-8"))
    if data.get("format") != FORMAT:
        sys.exit(f"不支持的题库格式：{data.get('format')}")
    return data


def save(bank, data):
    path = os.path.join(bank, "bank.json")
    temporary = path + ".tmp"
    try:
        with open(temporary, "w", encoding="utf-8", newline="\n") as handle:
            json.dump(data, handle, ensure_ascii=False, indent=1)
            handle.write("\n")
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.remove(temporary)


def by_id(data):
    return {question["id"]: question for question in data["questions"]}


def rounds(bank):
    """按轮次日期与名称排序返回 (tag, round.json 内容, judge 行列表)。

    judge 行先读 judge.jsonl，再按文件名读其他 judge*.jsonl。同一题同一重复编号只保留最后一行成功的评审（重评覆盖旧评审）；
    都失败时保留最后一行失败记录。"""
    root = os.path.join(bank, "runs")
    result = []
    for tag in os.listdir(root) if os.path.isdir(root) else []:
        meta_path = os.path.join(root, tag, "round.json")
        if not os.path.exists(meta_path):
            continue
        meta = json.load(open(meta_path, encoding="utf-8"))
        names = sorted(name for name in os.listdir(os.path.join(root, tag)) if name.startswith("judge") and name.endswith(".jsonl"))
        names.sort(key=lambda name: name != "judge.jsonl")
        latest = {}
        for name in names:
            for line in open(os.path.join(root, tag, name), encoding="utf-8"):
                if not line.strip():
                    continue
                row = json.loads(line)
                key = (row.get("case"), row.get("rep", 1))
                if "scores" in row or "scores" not in latest.get(key, {}):
                    latest[key] = row
        result.append((tag, meta, list(latest.values())))
    result.sort(key=lambda item: (item[1].get("date", ""), item[0]))
    return result


def cmd_init(args):
    os.makedirs(os.path.join(args.bank, "refs"), exist_ok=True)
    os.makedirs(os.path.join(args.bank, "runs"), exist_ok=True)
    if os.path.exists(os.path.join(args.bank, "bank.json")):
        print("题库已存在，未覆盖")
        return
    save(args.bank, {"format": FORMAT, "questions": []})
    print("已创建", os.path.join(args.bank, "bank.json"))


def check_question(bank, question, seen_queries, ref_text=None):
    """校验一道题的字段与标准答案。给了 ref_text 时校验这段文本，不读题库里的文件（入库前校验用）。"""
    problems, warnings = [], []
    for field in ("id", "query", "set", "author", "createdAt", "status", "ref"):
        value = question.get(field)
        if not isinstance(value, str) or not value.strip():
            problems.append(f"字段 {field} 必须是非空字符串")
    question_id = question.get("id")
    if isinstance(question_id, str) and question_id and not ID_PATTERN.fullmatch(question_id):
        problems.append("id 只能含小写字母、数字和短横线")
    if question.get("status") not in STATUSES:
        problems.append(f"status 必须是 {'/'.join(STATUSES)}")
    documents = question.get("documents", [])
    if not isinstance(documents, list) or not all(isinstance(document, str) for document in documents):
        problems.append("documents 必须是字符串列表")
    elif not documents:
        warnings.append("documents 为空（可用 bank.py match-refs 补全）")
    if not isinstance(question.get("notes", ""), str):
        problems.append("notes 必须是字符串")
    if isinstance(question.get("query"), str):
        normalized = re.sub(r"\s+", "", question["query"])
        if normalized in seen_queries:
            problems.append(f"题面与 {seen_queries[normalized]} 重复")
        seen_queries[normalized] = question_id
    if ref_text is None and isinstance(question.get("ref"), str):
        ref = os.path.join(bank, question["ref"])
        if not os.path.isfile(ref):
            problems.append(f"标准答案不存在：{question['ref']}")
        else:
            try:
                with open(ref, encoding="utf-8") as handle:
                    ref_text = handle.read()
            except UnicodeDecodeError:
                problems.append(f"标准答案不是 UTF-8 文本：{question['ref']}")
    if ref_text is not None:
        missing = [section for section in REF_SECTIONS if section not in ref_text]
        if "## 真值要点" in missing:
            problems.append("标准答案缺少“## 真值要点”")
        elif missing:
            warnings.append("标准答案缺少 " + "、".join(missing))
    return problems, warnings


def cmd_check(args):
    data = load(args.bank)
    ids, seen_queries, errors, notes = set(), {}, 0, 0
    for question in data["questions"]:
        if not isinstance(question, dict):
            print(f"[错误] 题目必须是对象：{question!r}")
            errors += 1
            continue
        key = question.get("id") if isinstance(question.get("id"), str) else repr(question.get("id"))
        if key in ids:
            print(f"[错误] {key}: id 重复")
            errors += 1
        ids.add(key)
        problems, warnings = check_question(args.bank, question, seen_queries)
        for problem in problems:
            print(f"[错误] {question.get('id')}: {problem}")
        for warning in warnings:
            print(f"[提示] {question.get('id')}: {warning}")
        errors += len(problems)
        notes += len(warnings)
    for tag, meta, judged in rounds(args.bank):
        unknown = sorted({row.get("case") for row in judged if isinstance(row.get("case"), str)} - ids)
        if unknown:
            print(f"[提示] 轮次 {tag} 含题库外的题目：{', '.join(unknown)}")
            notes += 1
    print(f"共 {len(data['questions'])} 题，错误 {errors}，提示 {notes}")
    sys.exit(1 if errors else 0)


def usage_counts(bank):
    counts = {}
    for tag, meta, judged in rounds(bank):
        for question_id in {row.get("case") for row in judged if "scores" in row}:
            counts[question_id] = counts.get(question_id, 0) + 1
    return counts


def cmd_list(args):
    data = load(args.bank)
    counts = usage_counts(args.bank)
    rows = [q for q in data["questions"] if (not args.status or q["status"] == args.status) and (not args.set or q["set"] == args.set)]
    print(f"共 {len(rows)} 题（id | 状态 | 题组 | 出题日期 | 依据文档数 | 评测轮次 | 题面）")
    for q in rows:
        query = str(q.get("query", ""))
        query = query if len(query) <= 60 else query[:60] + "…"
        print(f"{q['id']} | {q['status']} | {q['set']} | {q['createdAt']} | {len(q.get('documents', []))} | {counts.get(q['id'], 0)} | {query}")


def check_writer_kit(kit):
    """出题包的浏览脚本必须与技能里的原件逐字节一致；否则产出不可信，拒绝入库。返回产出目录 out/。"""
    browser = os.path.join(kit, *WRITER_BROWSER.split("/"))
    outputs = os.path.join(kit, "out")
    if not os.path.isfile(browser) or not os.path.isdir(outputs):
        sys.exit(f"不是 writer-kit 生成的出题目录（缺少 {WRITER_BROWSER} 或 out/）：{kit}")
    with open(browser, "rb") as copied, open(os.path.join(SKILL, "scripts", "corpus.py"), "rb") as original:
        if copied.read() != original.read():
            sys.exit(f"出题包的浏览脚本被改动过，拒绝入库：{browser}")
    return outputs


def cmd_add(args):
    data = load(args.bank)
    outputs = check_writer_kit(args.source)
    with open(os.path.join(outputs, "questions.json"), encoding="utf-8") as handle:
        source = json.load(handle)
    if not isinstance(source, list):
        sys.exit("questions.json 必须是数组")
    known = by_id(data)
    refs_in = os.path.realpath(os.path.join(outputs, "refs"))
    refs_out = os.path.realpath(os.path.join(args.bank, "refs"))
    seen_queries = {re.sub(r"\s+", "", q["query"]): q["id"] for q in data["questions"] if isinstance(q.get("query"), str)}
    # 第一阶段只读：全部题目的字段、路径和标准答案都校验通过，才会写题库；id 合格才用来拼路径。
    staged = []
    for item in source:
        if not isinstance(item, dict):
            sys.exit("questions.json 的每一项都必须是对象")
        question_id = item.get("id")
        if not isinstance(question_id, str) or not ID_PATTERN.fullmatch(question_id):
            sys.exit(f"id 只能含小写字母、数字和短横线：{question_id!r}")
        if question_id in known or any(question_id == question["id"] for question, _, _, _ in staged):
            sys.exit(f"id 已存在：{question_id}")
        ref_source = os.path.realpath(os.path.join(refs_in, f"{question_id}.md"))
        ref_target = os.path.realpath(os.path.join(refs_out, f"{question_id}.md"))
        if os.path.dirname(ref_source) != refs_in or not os.path.isfile(ref_source):
            sys.exit(f"缺少标准答案：{ref_source}")
        if os.path.dirname(ref_target) != refs_out or os.path.lexists(ref_target):
            sys.exit(f"题库里已有同名标准答案，不覆盖：{ref_target}")
        try:
            with open(ref_source, encoding="utf-8") as handle:
                ref_text = handle.read()
        except (OSError, UnicodeDecodeError) as error:
            sys.exit(f"{question_id}: 标准答案无法读取：{error}")
        documents = []
        case = item.get("case") or {}
        for document in case if isinstance(case, list) else [case]:
            if isinstance(document, dict) and isinstance(document.get("documentId"), str) and document["documentId"]:
                documents.append(document["documentId"])
        question = {
            "id": question_id,
            "query": item.get("query"),
            "set": args.set,
            "author": args.author,
            "createdAt": args.date or datetime.date.today().isoformat(),
            "documents": documents,
            "notes": item.get("why") or item.get("notes") or "",
            "status": "active",
            "ref": f"refs/{question_id}.md",
        }
        problems, warnings = check_question(args.bank, question, seen_queries, ref_text)
        if problems:
            sys.exit(f"{question_id}: " + "；".join(problems))
        staged.append((question, ref_source, ref_target, warnings))
    # 第二阶段写入：只创建不存在的文件；复制或保存任一步失败，都删除本次新建的文件，原有文件不受影响。
    created = []
    try:
        for question, ref_source, ref_target, _ in staged:
            with open(ref_source, "rb") as reader, open(ref_target, "xb") as writer:
                created.append(ref_target)
                shutil.copyfileobj(reader, writer)
        data["questions"] += [question for question, _, _, _ in staged]
        save(args.bank, data)
    except BaseException:
        for path in created:
            if os.path.exists(path):
                os.remove(path)
        raise
    for question, _, _, warnings in staged:
        for warning in warnings:
            print(f"[提示] {question['id']}: {warning}")
    print(f"已加入 {len(staged)} 题：{', '.join(question['id'] for question, _, _, _ in staged)}")


def cmd_sample(args):
    data = load(args.bank)
    excluded_sets = set(filter(None, args.exclude_set.split(",")))
    excluded_ids = set(filter(None, args.exclude.split(",")))
    pool = sorted(q["id"] for q in data["questions"] if q["status"] == args.status and q["set"] not in excluded_sets and q["id"] not in excluded_ids)
    if args.n > len(pool):
        sys.exit(f"候选只有 {len(pool)} 题")
    seed = int(args.seed) if args.seed.isdigit() else args.seed
    picked = random.Random(seed).sample(pool, args.n)
    print(json.dumps({"seed": args.seed, "pool": len(pool), "picked": picked}, ensure_ascii=False))


def cmd_cases(args):
    data = load(args.bank)
    known = by_id(data)
    ids = [value for value in args.ids.split(",") if value]
    missing = [value for value in ids if value not in known]
    if missing:
        sys.exit("题库中没有：" + ", ".join(missing))
    cases = [{"id": value, "query": known[value]["query"]} for value in ids]
    text = json.dumps(cases, ensure_ascii=False, indent=1)
    if args.out:
        open(args.out, "w", encoding="utf-8", newline="\n").write(text + "\n")
        print(f"已写出 {len(cases)} 题到 {args.out}")
    else:
        print(text)


def used_documents(data):
    return sorted({document for q in data["questions"] for document in q.get("documents", [])})


def cmd_used_docs(args):
    for document in used_documents(load(args.bank)):
        print(document)


def cmd_history(args):
    load(args.bank)
    for tag, meta, judged in rounds(args.bank):
        rows = [row for row in judged if not args.id or row.get("case") == args.id]
        if not rows:
            continue
        answer = meta.get("answer", {})
        print(f"== {tag}（{meta.get('date', '')}，{answer.get('model', '?')} {answer.get('effort', '')}，版本 {', '.join(f'{k}={v}' for k, v in meta.get('sides', {}).items())}）")
        for row in sorted(rows, key=lambda item: (item.get("case", ""), item.get("rep", 0))):
            if "scores" not in row:
                print(f"  {row.get('case')} r{row.get('rep')}: 评审失败")
                continue
            cells = [f"{side} {'✓' if score.get('target') else '✗'} {score.get('correctness')}/{score.get('completeness')}" for side, score in row["scores"].items()]
            winner = f" | 胜 {row['preferred']}" if row.get("preferred") else ""
            print(f"  {row.get('case')} r{row.get('rep')}: {' | '.join(cells)}{winner}")


def cmd_set_status(args):
    data = load(args.bank)
    known = by_id(data)
    if args.id not in known:
        sys.exit(f"题库中没有：{args.id}")
    if args.status not in STATUSES:
        sys.exit(f"status 必须是 {'/'.join(STATUSES)}")
    known[args.id]["status"] = args.status
    if args.note:
        known[args.id].setdefault("statusNotes", []).append({"date": datetime.date.today().isoformat(), "status": args.status, "note": args.note})
    save(args.bank, data)
    print(f"{args.id} -> {args.status}")


def corpus_index(args):
    config_path = os.path.join(args.bank, "config.json")
    config = json.load(open(config_path, encoding="utf-8")) if os.path.exists(config_path) else {}
    index = args.index or config.get("corpus", {}).get("index")
    if not index or not os.path.exists(index):
        sys.exit("找不到语料索引：用 --index 指定，或在 config.json 的 corpus.index 中配置")
    return index


def cmd_match_refs(args):
    """标准答案里出现的文档 ID，以及不短于 8 个字符的完整文档标题，都记为该题的依据文档。"""
    data = load(args.bank)
    db = sqlite3.connect("file:" + os.path.abspath(corpus_index(args)).replace("\\", "/") + "?mode=ro", uri=True)
    try:
        rows = db.execute("select id, title from documents where deleted = 0").fetchall()
    finally:
        db.close()
    known = {doc_id for doc_id, _ in rows}
    titles = {}
    for doc_id, title in rows:
        if title and len(title) >= MIN_TITLE_LENGTH:
            titles.setdefault(title, []).append(doc_id)
    for question in data["questions"]:
        ref_path = os.path.join(args.bank, question["ref"])
        if not os.path.exists(ref_path):
            continue
        text = open(ref_path, encoding="utf-8").read()
        found = {doc_id for doc_id in DOCUMENT_ID.findall(text) if doc_id in known}
        for title, ids in titles.items():
            if title in text:
                found.update(ids)
        before = set(question.get("documents", []))
        question["documents"] = sorted(before | found)
        print(f"{question['id']}: {len(before)} -> {len(question['documents'])}")
    save(args.bank, data)


def writer_command(model="<出题模型>", effort="<推理强度>"):
    """出题会话的启动命令（在出题目录运行，stdin 接 prompt.md）。"""
    return ["claude", "-p", "--model", model, "--effort", effort, "--strict-mcp-config", "--permission-mode", "dontAsk",
            "--allowedTools", *WRITER_ALLOWED_TOOLS, "--disallowedTools", *WRITER_DISALLOWED_TOOLS, "--output-format", "stream-json", "--verbose"]


def inside(path, root):
    path, root = os.path.normcase(os.path.abspath(path)), os.path.normcase(os.path.abspath(root))
    return path == root or path.startswith(root.rstrip(os.sep) + os.sep)


def cmd_writer_kit(args):
    data = load(args.bank)
    index = corpus_index(args)
    if os.path.exists(args.out) and os.listdir(args.out):
        sys.exit(f"出题目录必须是空目录：{args.out}")
    if inside(args.out, args.bank) or inside(args.bank, args.out):
        sys.exit("出题目录与题库不能互相包含")
    tools = os.path.join(args.out, "tools")
    os.makedirs(tools)
    os.makedirs(os.path.join(args.out, "out", "refs"))
    shutil.copyfile(os.path.join(SKILL, "scripts", "corpus.py"), os.path.join(tools, "corpus.py"))
    with open(os.path.join(tools, "corpus-config.json"), "w", encoding="utf-8", newline="\n") as handle:
        json.dump({"index": os.path.abspath(index).replace("\\", "/"), "used": used_documents(data)}, handle, ensure_ascii=False, indent=1)
    template = open(os.path.join(SKILL, "references", "writer-prompt.md"), encoding="utf-8").read()
    prompt = template.replace("{count}", str(args.count)).replace("{prefix}", args.prefix).replace("{scope}", args.scope).replace("{date}", args.date or datetime.date.today().isoformat())
    with open(os.path.join(args.out, "prompt.md"), "w", encoding="utf-8", newline="\n") as handle:
        handle.write(prompt)
    # 出题方只能写 out/；浏览脚本、配置和任务说明设为只读，重定向输出也改不了它们。
    for path in (os.path.join(tools, "corpus.py"), os.path.join(tools, "corpus-config.json"), os.path.join(args.out, "prompt.md")):
        os.chmod(path, stat.S_IREAD)
    print(f"已生成出题目录 {args.out}：tools/（只读浏览脚本与配置，已用文档 {len(used_documents(data))} 份）、prompt.md、out/（产出）")
    print("在出题目录运行：" + shlex.join(writer_command()) + " < prompt.md > writer.log")


def main():
    parser = argparse.ArgumentParser(description="评测题库管理")
    parser.add_argument("--bank", required=True, help="题库目录")
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("init")
    sub.add_parser("check")
    p = sub.add_parser("list")
    p.add_argument("--status")
    p.add_argument("--set")
    p = sub.add_parser("add")
    p.add_argument("--from", dest="source", required=True)
    p.add_argument("--set", required=True)
    p.add_argument("--author", required=True)
    p.add_argument("--date")
    p = sub.add_parser("sample")
    p.add_argument("--n", type=int, required=True)
    p.add_argument("--seed", required=True)
    p.add_argument("--status", default="active")
    p.add_argument("--exclude-set", default="")
    p.add_argument("--exclude", default="")
    p = sub.add_parser("cases")
    p.add_argument("--ids", required=True)
    p.add_argument("--out")
    sub.add_parser("used-docs")
    p = sub.add_parser("history")
    p.add_argument("--id")
    p = sub.add_parser("set-status")
    p.add_argument("id")
    p.add_argument("status")
    p.add_argument("--note")
    p = sub.add_parser("match-refs")
    p.add_argument("--index")
    p = sub.add_parser("writer-kit")
    p.add_argument("--out", required=True)
    p.add_argument("--count", type=int, default=4)
    p.add_argument("--prefix", default="fresh")
    p.add_argument("--scope", default="全部文档")
    p.add_argument("--date")
    p.add_argument("--index")
    args = parser.parse_args()
    {
        "init": cmd_init, "check": cmd_check, "list": cmd_list, "add": cmd_add, "sample": cmd_sample, "cases": cmd_cases,
        "used-docs": cmd_used_docs, "history": cmd_history, "set-status": cmd_set_status, "match-refs": cmd_match_refs, "writer-kit": cmd_writer_kit,
    }[args.command](args)


if __name__ == "__main__":
    main()
