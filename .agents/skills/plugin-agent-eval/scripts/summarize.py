"""汇总一个轮次（只用标准库）：逐题评分与胜负、均值，以及各版本的 token、工具调用与耗时。

用法：python summarize.py --bank DIR --tag 轮次 [--sides A,B] [--judge judge.jsonl]
只给一个版本时按单评结果汇总。同一题同一重复编号有多行评审结果时，取最后一行成功的结果。
盲评时另外列出：双序评审里两种顺序胜方不一致的题次；单序评审里 X 位置在两个版本间的分布。
"""
import argparse
import collections
import json
import os
import sys

sys.stdout.reconfigure(encoding="utf-8")


def main():
    parser = argparse.ArgumentParser(description="汇总一个轮次")
    parser.add_argument("--bank", required=True)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--sides", default="")
    parser.add_argument("--judge", default="judge.jsonl")
    args = parser.parse_args()
    run_dir = os.path.join(args.bank, "runs", args.tag)
    meta = json.load(open(os.path.join(run_dir, "round.json"), encoding="utf-8"))
    sides = [value for value in args.sides.split(",") if value] or list(meta.get("sides", {}))
    judge_path = os.path.join(run_dir, args.judge)
    latest = {}
    failed = []
    if os.path.exists(judge_path):
        for line in open(judge_path, encoding="utf-8"):
            if not line.strip():
                continue
            row = json.loads(line)
            if "scores" in row and all(side in row["scores"] for side in sides):
                latest[(row["case"], row.get("rep", 1))] = row
            else:
                failed.append(row)
    failed = [row for row in failed if (row.get("case"), row.get("rep", 1)) not in latest]
    answer = meta.get("answer", {})
    print(f"== {args.tag}：{meta.get('purpose', '')}")
    print(f"   作答 {answer.get('model')} {answer.get('effort', '')}，评审 {meta.get('judge', {}).get('model', '?')}，版本 " + "，".join(f"{side}={meta.get('sides', {}).get(side, '?')}" for side in sides))
    wins = collections.Counter()
    totals = {side: collections.Counter() for side in sides}
    for (case, rep), row in sorted(latest.items()):
        cells = []
        for side in sides:
            score = row["scores"][side]
            totals[side]["n"] += 1
            totals[side]["target"] += bool(score.get("target"))
            totals[side]["correct"] += score.get("correctness", 0)
            totals[side]["complete"] += score.get("completeness", 0)
            cells.append(f"{side} {'✓' if score.get('target') else '✗'} {score.get('correctness')}/{score.get('completeness')}")
        preferred = row.get("preferred")
        if preferred:
            wins[preferred] += 1
        print(f"  {case:40} r{rep} {' | '.join(cells)}" + (f" | 胜 {preferred}" if preferred else ""))
        if len(sides) == 1:
            score = row["scores"][sides[0]]
            for label, items in (("错误", score.get("errors", [])), ("遗漏", score.get("missing", []))):
                for item in items:
                    print(f"      {label}：{item}")
    for row in failed:
        print(f"  {row.get('case')} r{row.get('rep')}: 评审失败 {str(row.get('error', ''))[:120]}")
    if len(sides) == 2:
        left, right = sides
        print(f"  合计：{right}:{left}:平 = {wins[right]}:{wins[left]}:{sum(wins.values()) - wins[right] - wins[left]}")
        both = [row for row in latest.values() if row.get("orders")]
        if both:
            split = [f"{row['case']} r{row.get('rep', 1)}" for row in both if len({item["preferred"] for item in row["orders"]}) > 1]
            print(f"  双序评审 {len(both)} 题次，两种顺序胜方不一致 {len(split)} 题次（计为平）" + (f"：{', '.join(split)}" if split else ""))
        positions = collections.Counter(row["mapping"]["X"] for row in latest.values() if row.get("mapping") and not row.get("orders"))
        if positions:
            print("  单序评审的 X 位置：" + "，".join(f"{side} {positions[side]} 次" for side in sides))
    for side in sides:
        stats = totals[side]
        count = stats["n"] or 1
        print(f"  {side}: 找准 {stats['target']}/{stats['n']}，正确性 {stats['correct'] / count:.2f}，完整性 {stats['complete'] / count:.2f}")
    metrics_path = os.path.join(run_dir, "metrics.jsonl")
    if not os.path.exists(metrics_path):
        return
    runs = collections.defaultdict(dict)
    for line in open(metrics_path, encoding="utf-8"):
        if line.strip():
            row = json.loads(line)
            runs[row.get("side")][(row.get("case"), row.get("rep", 1))] = row
    print("== 运行指标（每题每次重复取最后一次运行）")
    summary = {}
    for side in sides:
        rows = list(runs[side].values())
        usage = collections.Counter()
        for row in rows:
            usage.update(row.get("usage", {}))
        calls = sum(sum(row.get("mcpCalls", {}).values()) for row in rows)
        wall = sum(row.get("wallSeconds", 0) for row in rows)
        shells = sum(row.get("shellCommands", 0) for row in rows)
        errors = sum(len(row.get("errors", [])) for row in rows)
        summary[side] = (usage["input_tokens"], usage["input_tokens"] - usage["cached_input_tokens"], usage["output_tokens"], calls, wall)
        print(f"  {side}: 会话 {len(rows)}，总输入 {usage['input_tokens']}，未缓存 {summary[side][1]}，输出 {usage['output_tokens']}，MCP 调用 {calls}，耗时 {wall:.0f}s，shell {shells}，错误 {errors}")
    if len(sides) == 2 and all(summary[side][0] for side in sides):
        left, right = sides
        names = ("总输入", "未缓存", "输出", "MCP 调用", "耗时")
        print(f"  {right} 相对 {left}：" + "，".join(f"{name} {(summary[right][index] / summary[left][index] - 1) * 100:+.1f}%" for index, name in enumerate(names) if summary[left][index]))


if __name__ == "__main__":
    main()
