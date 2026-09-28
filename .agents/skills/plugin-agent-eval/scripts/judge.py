"""评审作答结果（只用标准库）。有两种模式：

- 盲评：两个版本的答案标为 X/Y，对照标准答案同时评分并给出胜方。默认按两种顺序各评一次（X/Y 对调），
  消除展示位置的影响：分数取两次平均，找准目标要两次都成立，两次胜方一致才计胜，否则计平。
- 单评（--single）：只有一个版本时，逐题对照标准答案单独评分。

用法：
  python judge.py --bank DIR --tag 轮次 --sides A,B [--single-order] [--reps 1] [--ids a,b] [--jobs 4] [--out judge.jsonl]
  python judge.py --bank DIR --tag 轮次 --sides B --single [--reps 1] [--ids a,b]

评审模型、工作目录与题面前缀读自 <bank>/config.json 的 judge 节。评审看不到版本名。
第一种顺序的 X/Y 映射由轮次名、题目和重复编号确定，同一轮重评时不变；第二种顺序把它对调。
结果追加写入 <bank>/runs/<轮次>/<out>。
"""
import argparse
import concurrent.futures
import json
import os
import random
import re
import subprocess
import sys
import threading

sys.stdout.reconfigure(encoding="utf-8")
LOCK = threading.Lock()
RULES = """参考材料只覆盖部分原文：答案里参考材料没有覆盖的内容，只要不与参考材料矛盾、也不是明显臆造，就不算错误。答案里的来源链接、路径和格式不影响评分。"""
SCORE_FIELDS = """- target：是否找准了问题指向的文档或实体（true/false）
- correctness：0-5，关键事实与参考材料一致的程度（5 = 没有事实错误）
- completeness：0-5，回答问题所需关键要点的覆盖程度
- errors：与参考材料矛盾、或没有依据却当作事实陈述的内容，每条一句
- missing：遗漏的关键要点，每条一句"""
PAIR_PROMPT = """你是知识库问答的评审。下面给出用户问题、参考材料（真值要点、判错条件与原文摘录）以及两份匿名答案 X、Y。
{rules}

请分别评估 X 和 Y：
{fields}
最后给出 preference（X、Y 或 tie）和一句理由。

只输出一个 JSON 对象，不要输出其他文字：
{{"X":{{"target":true,"correctness":0,"completeness":0,"errors":[],"missing":[]}},"Y":{{"target":true,"correctness":0,"completeness":0,"errors":[],"missing":[]}},"preference":"X","reason":""}}

# 用户问题
{question}

# 参考材料
{reference}

# 答案 X
{answer_x}

# 答案 Y
{answer_y}
"""
SINGLE_PROMPT = """你是知识库问答的评审。下面给出用户问题、参考材料（真值要点、判错条件与原文摘录）以及一份答案。
{rules}

请评估这份答案：
{fields}
最后用一句话总结。

只输出一个 JSON 对象，不要输出其他文字：
{{"target":true,"correctness":0,"completeness":0,"errors":[],"missing":[],"reason":""}}

# 用户问题
{question}

# 参考材料
{reference}

# 答案
{answer}
"""


def parse_json(text):
    """取评审输出里第一个完整的 JSON 对象；忽略代码块标记和对象后面多出的文字。"""
    fenced = re.search(r"```(?:json)?\s*(\{.*\})\s*```", text, re.S)
    if fenced:
        text = fenced.group(1)
    value, _ = json.JSONDecoder().raw_decode(text, text.find("{"))
    return value


def ask(config, prompt):
    judge = config.get("judge", {})
    cwd = judge.get("cwd")
    os.makedirs(cwd, exist_ok=True)
    command = ["claude", "-p", "--model", judge["model"], "--output-format", "json", "--max-turns", "2", "--no-session-persistence", "--strict-mcp-config",
               "--disallowedTools", "Bash", "Read", "Write", "Edit", "Glob", "Grep", "WebFetch", "WebSearch", "Task", "Agent", "NotebookEdit", "Skill"]
    process = subprocess.run(command, input=prompt, cwd=cwd, capture_output=True, text=True, encoding="utf-8", timeout=judge.get("timeoutSeconds", 900))
    envelope = json.loads(process.stdout)
    return parse_json(envelope.get("result", "")), envelope.get("total_cost_usd")


def read_text(path):
    with open(path, encoding="utf-8") as handle:
        return handle.read()


def read_answer(run_dir, side, question_id, rep):
    path = os.path.join(run_dir, side, f"{question_id}-r{rep}.answer.md")
    if not os.path.exists(path):
        return None
    return read_text(path).strip() or "（空答案）"


def judge_pair(config, bank, run_dir, tag, question, rep, sides, both_orders=True):
    answers = {side: read_answer(run_dir, side, question["id"], rep) for side in sides}
    if any(value is None for value in answers.values()):
        return None
    order = list(sides)
    random.Random(f"{tag}|{question['id']}|{rep}").shuffle(order)
    mappings = [{"X": order[0], "Y": order[1]}]
    if both_orders:
        mappings.append({"X": order[1], "Y": order[0]})
    question_text = config.get("judge", {}).get("questionPrefix", "") + question["query"]
    reference = read_text(os.path.join(bank, question["ref"]))
    orders, cost = [], 0.0
    for mapping in mappings:
        prompt = PAIR_PROMPT.format(rules=RULES, fields=SCORE_FIELDS, question=question_text, reference=reference, answer_x=answers[mapping["X"]], answer_y=answers[mapping["Y"]])
        try:
            verdict, spent = ask(config, prompt)
            scores = {mapping[label]: verdict[label] for label in ("X", "Y")}
        except Exception as error:  # noqa: BLE001
            return {"case": question["id"], "rep": rep, "mapping": mappings[0], "error": str(error)[:400]}
        cost += spent or 0
        orders.append({"mapping": mapping, "scores": scores, "preferred": mapping.get(verdict.get("preference"), "tie"), "reason": verdict.get("reason", "")})
    return combine_orders(question["id"], rep, sides, orders, cost)


def combine_orders(case, rep, sides, orders, cost):
    """合并各顺序的评审：只有一种顺序时原样返回；两种顺序时分数取平均，胜方一致才算，否则计平。"""
    row = {"case": case, "rep": rep, "mapping": orders[0]["mapping"], "costUsd": round(cost, 6)}
    if len(orders) == 1:
        row.update(scores=orders[0]["scores"], preferred=orders[0]["preferred"], reason=orders[0]["reason"])
        return row
    row["scores"] = {side: merge_scores([item["scores"][side] for item in orders]) for side in sides}
    preferences = {item["preferred"] for item in orders}
    row["preferred"] = preferences.pop() if len(preferences) == 1 else "tie"
    row["reason"] = "｜".join(item["reason"] for item in orders)
    row["orders"] = orders
    return row


def merge_scores(scores):
    """找准目标要每次都成立；正确性与完整性取平均；错误与遗漏合并去重。"""
    merged = {"target": all(score.get("target") for score in scores)}
    for key in ("correctness", "completeness"):
        value = sum(score.get(key, 0) for score in scores) / len(scores)
        merged[key] = int(value) if value == int(value) else round(value, 2)
    for key in ("errors", "missing"):
        items = []
        for score in scores:
            for item in score.get(key, []):
                if item not in items:
                    items.append(item)
        merged[key] = items
    return merged


def judge_single(config, bank, run_dir, tag, question, rep, side):
    answer = read_answer(run_dir, side, question["id"], rep)
    if answer is None:
        return None
    prompt = SINGLE_PROMPT.format(rules=RULES, fields=SCORE_FIELDS, question=config.get("judge", {}).get("questionPrefix", "") + question["query"],
                                  reference=read_text(os.path.join(bank, question["ref"])), answer=answer)
    try:
        verdict, cost = ask(config, prompt)
        score = {key: verdict[key] for key in ("target", "correctness", "completeness", "errors", "missing")}
    except Exception as error:  # noqa: BLE001
        return {"case": question["id"], "rep": rep, "side": side, "error": str(error)[:400]}
    return {"case": question["id"], "rep": rep, "side": side, "scores": {side: score}, "reason": verdict.get("reason", ""), "costUsd": cost}


def main():
    parser = argparse.ArgumentParser(description="评审作答结果")
    parser.add_argument("--bank", required=True)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--sides", required=True)
    parser.add_argument("--single", action="store_true", help="单版本逐题评分")
    parser.add_argument("--single-order", action="store_true", help="盲评只按一种顺序评一次（默认两种顺序各评一次）")
    parser.add_argument("--reps", default="1")
    parser.add_argument("--ids", default="")
    parser.add_argument("--jobs", type=int, default=4)
    parser.add_argument("--out", default="judge.jsonl")
    args = parser.parse_args()
    args.bank = os.path.abspath(args.bank)
    config = json.loads(read_text(os.path.join(args.bank, "config.json")))
    if not config.get("judge", {}).get("model") or not config.get("judge", {}).get("cwd"):
        sys.exit("config.json 需要 judge.model 与 judge.cwd")
    bank = json.loads(read_text(os.path.join(args.bank, "bank.json")))
    known = {question["id"]: question for question in bank["questions"]}
    run_dir = os.path.join(args.bank, "runs", args.tag)
    meta_path = os.path.join(run_dir, "round.json")
    meta = json.loads(read_text(meta_path))
    ids = [value for value in args.ids.split(",") if value] or meta.get("questions", [])
    sides = [value for value in args.sides.split(",") if value]
    if args.single != (len(sides) == 1):
        sys.exit("--single 需要恰好一个版本；盲评需要两个版本")
    tasks = [(known[value], int(rep)) for rep in args.reps.split(",") for value in ids]
    out_path = os.path.join(run_dir, args.out)
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.jobs) as pool:
        if args.single:
            futures = [pool.submit(judge_single, config, args.bank, run_dir, args.tag, question, rep, sides[0]) for question, rep in tasks]
        else:
            futures = [pool.submit(judge_pair, config, args.bank, run_dir, args.tag, question, rep, sides, not args.single_order) for question, rep in tasks]
        for future in concurrent.futures.as_completed(futures):
            row = future.result()
            if row is None:
                continue
            with LOCK, open(out_path, "a", encoding="utf-8") as handle:
                handle.write(json.dumps(row, ensure_ascii=False) + "\n")
            brief = {side: {key: score[key] for key in ("target", "correctness", "completeness")} for side, score in row["scores"].items()} if "scores" in row else row.get("error")
            orders = " 各顺序胜方=" + "/".join(item["preferred"] for item in row["orders"]) if row.get("orders") else ""
            print(row["case"], row["rep"], "preferred=", row.get("preferred", "-"), json.dumps(brief, ensure_ascii=False) + orders, flush=True)
    mode = "single" if args.single else ("pairwise" if args.single_order else "pairwise-both-orders")
    meta["judge"] = {"model": config["judge"]["model"], "mode": mode}
    meta.setdefault("judgeFiles", {})[args.out] = mode
    with open(meta_path, "w", encoding="utf-8", newline="\n") as handle:
        json.dump(meta, handle, ensure_ascii=False, indent=1)
        handle.write("\n")


if __name__ == "__main__":
    main()
