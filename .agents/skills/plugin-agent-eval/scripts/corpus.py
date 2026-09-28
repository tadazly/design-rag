"""只读浏览知识库索引里的全文（只用标准库）。出题包会复制本脚本，所以这里只放只读命令，不写任何文件。

用法：
  python corpus.py [--index 索引] stats
  python corpus.py [--index 索引] list [--kind 类型] [--year 2026] [--q 标题或路径关键词] [--sort date|chunks|size|title] [--asc] [--limit 50] [--offset 0]
  python corpus.py [--index 索引] show <文档id> [--start 0] [--max-chars 30000]
  python corpus.py [--index 索引] grep <文本> [--kind 类型] [--limit 30]

索引路径依次取自 --index、环境变量 CORPUS_INDEX、同目录 corpus-config.json 的 index。
corpus-config.json 的 used 列出旧题用过的文档，list 与 show 会标注“已用”。
数据库以只读模式打开并设为 query_only，任何命令都不会写索引。
"""
import argparse
import json
import os
import sqlite3
import sys

sys.stdout.reconfigure(encoding="utf-8")
HERE = os.path.dirname(os.path.abspath(__file__))


def local_config():
    path = os.path.join(HERE, "corpus-config.json")
    return json.load(open(path, encoding="utf-8")) if os.path.exists(path) else {}


def connect(index):
    index = index or os.environ.get("CORPUS_INDEX") or local_config().get("index")
    if not index or not os.path.exists(index):
        sys.exit("找不到索引：用 --index、环境变量 CORPUS_INDEX 或 corpus-config.json 指定")
    db = sqlite3.connect("file:" + os.path.abspath(index).replace("\\", "/") + "?mode=ro", uri=True)
    db.execute("pragma query_only = on")
    return db


def doc_chars(db, doc_id):
    return db.execute("select coalesce(sum(length(text)), 0) from chunks where document_id = ?", (doc_id,)).fetchone()[0]


def cmd_stats(db, used, args):
    for kind, count in db.execute("select source_kind, count(*) from documents where deleted = 0 group by source_kind"):
        print(f"{kind}: {count} 份")
    print("按有效更新年份：")
    for kind, year, count in db.execute("select source_kind, substr(effective_updated_at, 1, 4) y, count(*) from documents where deleted = 0 group by source_kind, y order by source_kind, y"):
        print(f"  {kind} {year}: {count}")


def cmd_list(db, used, args):
    where, params = ["deleted = 0"], []
    if args.kind:
        where.append("source_kind = ?")
        params.append(args.kind)
    if args.year:
        where.append("substr(effective_updated_at, 1, 4) = ?")
        params.append(args.year)
    if args.q:
        where.append("(instr(lower(title), lower(?)) > 0 or instr(lower(relative_path), lower(?)) > 0)")
        params += [args.q, args.q]
    order = {"date": "effective_updated_at_ms", "chunks": "chunk_count", "title": "title", "size": "size_bytes"}[args.sort]
    direction = "asc" if args.asc or args.sort == "title" else "desc"
    rows = db.execute(
        f"select id, source_kind, substr(effective_updated_at, 1, 10), date_source, chunk_count, title, relative_path from documents where {' and '.join(where)} order by {order} {direction} limit ? offset ?",
        params + [args.limit, args.offset],
    ).fetchall()
    total = db.execute(f"select count(*) from documents where {' and '.join(where)}", params).fetchone()[0]
    print(f"共 {total} 份，显示 {args.offset + 1}-{args.offset + len(rows)}（id | 类型 | 有效日期(来源) | 分块数 | 正文字数 | 已用 | 标题 | 相对路径）")
    for doc_id, kind, date, source, chunks, title, path in rows:
        print(f"{doc_id} | {kind} | {date}({source}) | {chunks} | {doc_chars(db, doc_id)} | {'已用' if doc_id in used else ''} | {title} | {path}")


def cmd_show(db, used, args):
    doc = db.execute("select id, source_kind, title, relative_path, effective_updated_at, date_source, chunk_count from documents where id = ? and deleted = 0", (args.id,)).fetchone()
    if not doc:
        print("没有这个文档 id")
        return
    print(f"# {doc[2]}\nid={doc[0]} 类型={doc[1]} 路径={doc[3]} 有效日期={doc[4]}({doc[5]}) 分块数={doc[6]}{' 【已用于旧题】' if doc[0] in used else ''}\n")
    printed = 0
    for ordinal, section, heading, locator, text in db.execute(
        "select ordinal, section_type, heading_path_json, locator, text from chunks where document_id = ? and ordinal >= ? order by ordinal", (args.id, args.start)
    ):
        path = " > ".join(str(part) for part in (json.loads(heading or "[]") or []) if part)
        block = f"--- [#{ordinal}] {section} | {path} | {locator}\n{text}\n"
        if printed and printed + len(block) > args.max_chars:
            print(f"\n（已输出约 {printed} 字；继续请用 --start {ordinal}）")
            return
        print(block)
        printed += len(block)
    print("（文档结束）")


def cmd_grep(db, used, args):
    where, params = ["d.deleted = 0", "instr(lower(c.text), lower(?)) > 0"], [args.text]
    if args.kind:
        where.append("d.source_kind = ?")
        params.append(args.kind)
    rows = db.execute(
        f"select d.id, d.source_kind, substr(d.effective_updated_at, 1, 10), d.title, c.ordinal, c.locator, c.text from chunks c join documents d on d.id = c.document_id where {' and '.join(where)} order by d.effective_updated_at_ms desc limit ?",
        params + [args.limit],
    ).fetchall()
    print(f"显示 {len(rows)} 条（按有效日期从新到旧）")
    for doc_id, kind, date, title, ordinal, locator, text in rows:
        position = text.lower().find(args.text.lower())
        snippet = text[max(0, position - 80): position + len(args.text) + 120].replace("\n", " ")
        print(f"{doc_id} | {kind} | {date} | {title} | #{ordinal} {locator}\n    …{snippet}…")


def main():
    parser = argparse.ArgumentParser(description="只读浏览知识库索引")
    parser.add_argument("--index")
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("stats")
    p = sub.add_parser("list")
    p.add_argument("--kind")
    p.add_argument("--year")
    p.add_argument("--q")
    p.add_argument("--sort", choices=["date", "chunks", "size", "title"], default="date")
    p.add_argument("--asc", action="store_true")
    p.add_argument("--limit", type=int, default=50)
    p.add_argument("--offset", type=int, default=0)
    p = sub.add_parser("show")
    p.add_argument("id")
    p.add_argument("--start", type=int, default=0)
    p.add_argument("--max-chars", type=int, default=30000)
    p = sub.add_parser("grep")
    p.add_argument("text")
    p.add_argument("--kind")
    p.add_argument("--limit", type=int, default=30)
    args = parser.parse_args()
    db = connect(args.index)
    used = set(local_config().get("used", []))
    {"stats": cmd_stats, "list": cmd_list, "show": cmd_show, "grep": cmd_grep}[args.command](db, used, args)


if __name__ == "__main__":
    main()
