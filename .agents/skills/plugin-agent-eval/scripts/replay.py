"""退步定位（只用标准库）：列出会话里的插件调用，或用同一组参数在各版本上重放。

用法：
  python replay.py --bank DIR calls --tag 轮次 --side 版本 --id 题目 [--rep 1]
  python replay.py --bank DIR call --sides A,B --tool 工具名 --args '<JSON>' [--keys '原文1|原文2'] [--show]
  python replay.py --bank DIR session --tag 轮次 --side 版本 --id 题目 --sides A,B [--rep 1] [--tools 工具1,工具2] [--ignore 前缀1,前缀2]

calls 按顺序列出会话调用的插件工具与参数，并标出返回中出现的关键原文。关键原文取自标准答案“## 原文摘录”：
每行取第一段不短于 8 个字符的文字，截取前 12 个字符。
call 按 config.json 的 sides 启动各版本的 MCP 服务（stdio），用同一组参数各调一次，比较返回片段与关键原文。
session 把一次会话里的全部检索调用按原参数在各版本上重放，逐个调用比较关键原文，并汇总只在某个版本出现（仅）
或三个及以上版本时只有某个版本缺少（缺）的关键原文。
检索工具名取 --tools，未给时取 config.json 的 retrievalTools；--ignore 可排除与题目无关的标记（按前缀匹配）。
"""
import argparse
import json
import os
import re
import subprocess
import sys
import threading

sys.stdout.reconfigure(encoding="utf-8")


def markers_from_ref(path):
    if not os.path.exists(path):
        return []
    text = open(path, encoding="utf-8").read()
    if "## 原文摘录" not in text:
        return []
    section = re.split(r"\n## ", text.split("## 原文摘录", 1)[1])[0]
    result = []
    for line in section.splitlines():
        line = line.strip()
        if not line or line.startswith("#") or (line.startswith("**") and line.endswith("**")):
            continue
        line = re.sub(r"^(>\s*|[-*]\s+|\d+[.、]\s*)", "", line).strip()
        for segment in re.split(r"…|\.\.\.|\s{2,}|\s\|\s", line):
            segment = segment.strip(" ·-*“”\"`")
            if len(segment) >= 8 and re.search(r"\w", segment):
                result.append(segment[:12])
                break
    return sorted(set(result))


def result_text(result):
    if not isinstance(result, dict):
        return ""
    return "\n".join(part.get("text", "") for part in result.get("content", []) if isinstance(part, dict))


def cmd_calls(args, config, bank):
    question = next((q for q in bank["questions"] if q["id"] == args.id), None)
    if question is None:
        sys.exit(f"题库中没有：{args.id}")
    markers = markers_from_ref(os.path.join(args.bank, question["ref"]))
    path = os.path.join(args.bank, "runs", args.tag, args.side, f"{args.id}-r{args.rep}.session.jsonl")
    if not os.path.exists(path):
        sys.exit(f"没有会话日志：{path}")
    print(f"关键原文 {len(markers)} 条")
    index = 0
    for line in open(path, encoding="utf-8"):
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        item = event.get("item", {})
        if event.get("type") != "item.completed" or item.get("type") != "mcp_tool_call":
            continue
        index += 1
        text = result_text(item.get("result"))
        found = [marker for marker in markers if marker in text]
        arguments = json.dumps(item.get("arguments") or {}, ensure_ascii=False)
        print(f"{index:>2}. {item.get('tool')} [{item.get('status')}] 返回 {len(text)} 字，关键原文 {len(found)}/{len(markers)}")
        print(f"    参数 {arguments}")
        if found:
            print(f"    命中 {found}")


class MCP:
    def __init__(self, definition):
        env = dict(os.environ)
        env.update({key: str(value) for key, value in (definition.get("env") or {}).items()})
        self.process = subprocess.Popen([definition["command"], *definition.get("args", [])], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env)
        self.stderr = []
        threading.Thread(target=lambda: [self.stderr.append(line) for line in self.process.stderr], daemon=True).start()
        self.next_id = 0
        self.request("initialize", {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "plugin-agent-eval", "version": "1"}})
        self.send({"jsonrpc": "2.0", "method": "notifications/initialized"})

    def send(self, message):
        self.process.stdin.write((json.dumps(message, ensure_ascii=False) + "\n").encode("utf-8"))
        self.process.stdin.flush()

    def request(self, method, params):
        self.next_id += 1
        self.send({"jsonrpc": "2.0", "id": self.next_id, "method": method, "params": params})
        while True:
            line = self.process.stdout.readline()
            if not line:
                raise RuntimeError("MCP 服务已退出：" + b"".join(self.stderr[-5:]).decode("utf-8", "replace"))
            message = json.loads(line)
            if message.get("id") == self.next_id:
                if "error" in message:
                    raise RuntimeError(json.dumps(message["error"], ensure_ascii=False))
                return message["result"]

    def close(self):
        self.process.stdin.close()
        self.process.wait(timeout=15)


def fragments(value, found):
    """在返回的 JSON 里找带 locator 的片段，返回 (locator, 正文)；兼容不同的结果结构。"""
    if isinstance(value, dict):
        content = value.get("content") if isinstance(value.get("content"), str) else value.get("text")
        if isinstance(value.get("locator"), str) and isinstance(content, str):
            found.append((value["locator"], content))
        for item in value.values():
            fragments(item, found)
    elif isinstance(value, list):
        for item in value:
            fragments(item, found)
    return found


def cmd_call(args, config, bank):
    keys = [key for key in args.keys.split("|") if key]
    arguments = json.loads(args.args)
    for side in [value for value in args.sides.split(",") if value]:
        client = MCP(config["sides"][side])
        try:
            result = client.request("tools/call", {"name": args.tool, "arguments": arguments})
        finally:
            client.close()
        text = result_text(result)
        try:
            payload = json.loads(text)
        except json.JSONDecodeError:
            payload = None
        items = fragments(payload, []) if payload is not None else []
        print(f"== {side}：返回 {len(text)} 字，片段 {len(items)} 个，关键原文 {[key for key in keys if key in text]}")
        for locator, content in items:
            preview = content[:60].replace("\n", " ")
            print(f"   {locator[:32]:32} {len(content):5} {[key for key in keys if key in content]} {preview}")
        if args.show:
            print(text)


def cmd_session(args, config, bank):
    question = next((q for q in bank["questions"] if q["id"] == args.id), None)
    if question is None:
        sys.exit(f"题库中没有：{args.id}")
    ignored = [value for value in args.ignore.split(",") if value]
    markers = [marker for marker in markers_from_ref(os.path.join(args.bank, question["ref"])) if not any(marker.startswith(prefix) for prefix in ignored)]
    tools = [value for value in args.tools.split(",") if value] or config.get("retrievalTools", [])
    if not tools:
        sys.exit("没有指定要重放的检索工具：用 --tools，或在 config.json 的 retrievalTools 里登记")
    path = os.path.join(args.bank, "runs", args.tag, args.side, f"{args.id}-r{args.rep}.session.jsonl")
    calls = []
    for line in open(path, encoding="utf-8"):
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        item = event.get("item", {})
        if event.get("type") == "item.completed" and item.get("type") == "mcp_tool_call" and item.get("tool") in tools:
            calls.append((item.get("tool"), item.get("arguments") or {}))
    sides = [value for value in args.sides.split(",") if value]
    clients = {side: MCP(config["sides"][side]) for side in sides}
    union = {side: set() for side in sides}
    try:
        print(f"关键原文 {len(markers)} 条，重放 {len(calls)} 个调用")
        for index, (tool, arguments) in enumerate(calls, 1):
            found = {}
            for side in sides:
                text = result_text(clients[side].request("tools/call", {"name": tool, "arguments": arguments}))
                found[side] = {marker for marker in markers if marker in text}
                union[side] |= found[side]
            counts = "，".join(f"{side} {len(found[side])}" for side in sides)
            print(f"{index:>2}. {tool} {json.dumps(arguments, ensure_ascii=False)[:160]}")
            print(f"    关键原文：{counts}")
            print_differences(found, sides, "    ")
    finally:
        for client in clients.values():
            client.close()
    print("== 整个会话合计：" + "，".join(f"{side} {len(union[side])}" for side in sides))
    print_differences(union, sides, "   ")


def print_differences(found, sides, indent):
    """“仅 X”是只有 X 返回的关键原文；三个及以上版本时，“缺 X”是其他版本都返回、只有 X 没返回的关键原文。"""
    for side in sides:
        others = [found[other] for other in sides if other != side]
        only = sorted(found[side] - set().union(*others))
        if only:
            print(f"{indent}仅 {side}：{only}")
        if len(others) > 1:
            lacking = sorted(set.intersection(*others) - found[side])
            if lacking:
                print(f"{indent}缺 {side}：{lacking}")


def main():
    parser = argparse.ArgumentParser(description="退步定位")
    parser.add_argument("--bank", required=True)
    sub = parser.add_subparsers(dest="command", required=True)
    p = sub.add_parser("calls")
    p.add_argument("--tag", required=True)
    p.add_argument("--side", required=True)
    p.add_argument("--id", required=True)
    p.add_argument("--rep", type=int, default=1)
    p = sub.add_parser("call")
    p.add_argument("--sides", required=True)
    p.add_argument("--tool", required=True)
    p.add_argument("--args", required=True)
    p.add_argument("--keys", default="")
    p.add_argument("--show", action="store_true")
    p = sub.add_parser("session")
    p.add_argument("--tag", required=True)
    p.add_argument("--side", required=True)
    p.add_argument("--id", required=True)
    p.add_argument("--rep", type=int, default=1)
    p.add_argument("--sides", required=True)
    p.add_argument("--tools", default="")
    p.add_argument("--ignore", default="")
    args = parser.parse_args()
    config_path = os.path.join(args.bank, "config.json")
    config = json.load(open(config_path, encoding="utf-8")) if os.path.exists(config_path) else {}
    bank = json.load(open(os.path.join(args.bank, "bank.json"), encoding="utf-8"))
    {"calls": cmd_calls, "call": cmd_call, "session": cmd_session}[args.command](args, config, bank)


if __name__ == "__main__":
    main()
