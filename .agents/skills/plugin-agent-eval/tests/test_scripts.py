"""评测技能脚本的回归测试（只用标准库，全部使用虚构数据，不调用任何模型）。

运行：在技能目录执行 python -m unittest discover -s tests -v
"""
import hashlib
import importlib.util
import json
import os
import sqlite3
import stat
import subprocess
import sys
import tempfile
import unittest

SCRIPTS = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "scripts")
DOC_A = "doc_" + "a" * 24
DOC_B = "doc_" + "b" * 24


def run(args, cwd=None):
    env = dict(os.environ, PYTHONIOENCODING="utf-8", PYTHONDONTWRITEBYTECODE="1")
    env.pop("CORPUS_INDEX", None)
    return subprocess.run([sys.executable, *args], cwd=cwd, capture_output=True, text=True, encoding="utf-8", env=env)


def snapshot(*roots):
    """目录或单个文件下每个文件的内容哈希。"""
    result = {}
    for root in roots:
        paths = [root] if os.path.isfile(root) else [os.path.join(folder, name) for folder, _, names in os.walk(root) for name in names]
        for path in paths:
            with open(path, "rb") as handle:
                result[path] = hashlib.sha256(handle.read()).hexdigest()
    return result


def make_index(path):
    db = sqlite3.connect(path)
    db.executescript("""
        create table documents (id text, source_kind text, title text, relative_path text, effective_updated_at text,
            effective_updated_at_ms integer, date_source text, chunk_count integer, size_bytes integer, deleted integer);
        create table chunks (document_id text, ordinal integer, section_type text, heading_path_json text, locator text, text text);
    """)
    db.executemany("insert into documents values (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)", [
        (DOC_A, "design", "晨星试炼玩法说明书", "版本资料/晨星试炼玩法说明书.docx", "2026-08-01T00:00:00Z", 1785542400000, "filename", 2, 2048),
        (DOC_B, "table", "星核兑换配置表格", "配置/星核兑换配置表格.xlsx", "2026-07-01T00:00:00Z", 1782864000000, "modified", 1, 1024),
    ])
    db.executemany("insert into chunks values (?, ?, ?, ?, ?, ?)", [
        (DOC_A, 0, "paragraph", '["玩法流程"]', "段落 1-3", "进入试炼后先选择地图，放弃探索不会触发结算。"),
        (DOC_A, 1, "paragraph", '["奖励"]', "段落 4", "通关后获得星核。"),
        (DOC_B, 0, "table", '["兑换"]', "表格 1 行 1-2", "星核 | 数量 | 兑换上限"),
    ])
    db.commit()
    db.close()


def load_script(name):
    spec = importlib.util.spec_from_file_location(f"skill_{name}", os.path.join(SCRIPTS, f"{name}.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class FixtureCase(unittest.TestCase):
    """一个虚构题库：一道题，标准答案写着文档 A 的 ID 和文档 B 的完整标题。"""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = self.temp.name
        self.bank = os.path.join(self.root, "bank")
        self.index = os.path.join(self.root, "index.sqlite")
        make_index(self.index)
        result = run([os.path.join(SCRIPTS, "bank.py"), "--bank", self.bank, "init"])
        self.assertEqual(result.returncode, 0, result.stderr)
        with open(os.path.join(self.bank, "refs", "demo-one.md"), "w", encoding="utf-8") as handle:
            handle.write(f"## 真值要点\n- 放弃探索不会结算（{DOC_A}）\n\n## 判错条件\n- 说成会结算\n\n## 原文摘录\n> 放弃探索不会触发结算。\n\n兑换数量见《星核兑换配置表格》。\n")
        data = self.load_bank()
        data["questions"].append({"id": "demo-one", "query": "晨星试炼放弃探索会结算吗？", "set": "demo", "author": "测试", "createdAt": "2026-08-02",
                                  "documents": [], "notes": "", "status": "active", "ref": "refs/demo-one.md"})
        self.write_json(os.path.join(self.bank, "bank.json"), data)
        self.write_json(os.path.join(self.bank, "config.json"), {"corpus": {"index": self.index}})

    def tearDown(self):
        self.temp.cleanup()

    def load_bank(self):
        with open(os.path.join(self.bank, "bank.json"), encoding="utf-8") as handle:
            return json.load(handle)

    def write_json(self, path, value):
        with open(path, "w", encoding="utf-8") as handle:
            json.dump(value, handle, ensure_ascii=False, indent=1)


class WriterKitTest(FixtureCase):
    def make_kit(self):
        kit = os.path.join(self.root, "writer")
        result = run([os.path.join(SCRIPTS, "bank.py"), "--bank", self.bank, "writer-kit", "--out", kit, "--count", "2"])
        self.assertEqual(result.returncode, 0, result.stderr)
        return kit, result.stdout

    def write_outputs(self, kit):
        """模拟出题方在 out/ 写出一道题。"""
        with open(os.path.join(kit, "out", "questions.json"), "w", encoding="utf-8") as handle:
            json.dump([{"id": "fresh-demo", "query": "星核兑换有上限吗？", "case": {"documentId": DOC_B}, "why": "考兑换上限"}], handle, ensure_ascii=False)
        with open(os.path.join(kit, "out", "refs", "fresh-demo.md"), "w", encoding="utf-8") as handle:
            handle.write("## 真值要点\n- 有兑换上限\n\n## 判错条件\n- 说成没有上限\n\n## 原文摘录\n> 星核 | 数量 | 兑换上限\n")

    def test_writer_kit_layout_hides_bank(self):
        kit, _ = self.make_kit()
        files = sorted(os.path.relpath(os.path.join(folder, name), kit).replace("\\", "/") for folder, _, names in os.walk(kit) for name in names)
        self.assertEqual(files, ["prompt.md", "tools/corpus-config.json", "tools/corpus.py"])
        self.assertTrue(os.path.isdir(os.path.join(kit, "out", "refs")))
        with open(os.path.join(kit, "tools", "corpus-config.json"), encoding="utf-8") as handle:
            self.assertEqual(sorted(json.load(handle)), ["index", "used"])
        for name in files:
            with open(os.path.join(kit, name), encoding="utf-8") as handle:
                text = handle.read()
            self.assertNotIn(self.bank, text, f"{name} 暴露了题库路径")
            self.assertNotIn(self.bank.replace("\\", "/"), text, f"{name} 暴露了题库路径")

    def test_writer_kit_refuses_directory_inside_bank(self):
        result = run([os.path.join(SCRIPTS, "bank.py"), "--bank", self.bank, "writer-kit", "--out", os.path.join(self.bank, "writer")])
        self.assertNotEqual(result.returncode, 0)

    def test_writer_permissions_keep_browser_out_of_write_scope(self):
        bank = load_script("bank")
        _, printed = self.make_kit()
        writable = [rule for rule in bank.WRITER_ALLOWED_TOOLS if rule.startswith(("Edit(", "Write("))]
        commands = [rule for rule in bank.WRITER_ALLOWED_TOOLS if rule.startswith("Bash(")]
        self.assertEqual(sorted(writable), ["Edit(./out/**)", "Write(./out/**)"])
        self.assertEqual(sorted(commands), sorted(f"Bash(python -I {bank.WRITER_BROWSER} {name}:*)" for name in ("grep", "list", "show", "stats")))
        self.assertFalse(("./" + bank.WRITER_BROWSER).startswith("./out/"), "浏览脚本不能在可写范围内")
        self.assertEqual(sorted(set(bank.WRITER_ALLOWED_TOOLS) - {"Read(./**)"}), sorted(writable + commands))
        for rule in bank.WRITER_ALLOWED_TOOLS + bank.WRITER_DISALLOWED_TOOLS:
            self.assertIn(rule, printed, "writer-kit 打印的命令要与权限定义一致")

    def test_writer_kit_files_are_read_only(self):
        kit, _ = self.make_kit()
        for name in ("tools/corpus.py", "tools/corpus-config.json", "prompt.md"):
            path = os.path.join(kit, *name.split("/"))
            self.assertFalse(os.access(path, os.W_OK), f"{name} 应为只读")
            with self.assertRaises(PermissionError):
                open(path, "a", encoding="utf-8").close()

    def test_browser_commands_cannot_change_bank_or_index(self):
        kit, _ = self.make_kit()
        before = snapshot(self.bank, self.index)
        browser = os.path.join("tools", "corpus.py")
        for command in (["stats"], ["list"], ["show", DOC_A], ["grep", "结算"]):
            result = run(["-I", browser, *command], cwd=kit)
            self.assertEqual(result.returncode, 0, f"{command}: {result.stderr}")
        self.assertIn("放弃探索不会触发结算", run(["-I", browser, "show", DOC_A], cwd=kit).stdout)
        result = run(["-I", browser, "match-refs", "--bank", self.bank], cwd=kit)
        self.assertNotEqual(result.returncode, 0, "出题包的 corpus.py 不应再有 match-refs")
        result = run(["-c", "import corpus; corpus.connect(None).execute('create table probe (x)')"], cwd=os.path.join(kit, "tools"))
        self.assertNotEqual(result.returncode, 0, "出题包的索引连接不应允许写入")
        self.assertIn("readonly", result.stderr.replace(" ", "").lower())
        self.assertEqual(snapshot(self.bank, self.index), before)

    def test_add_accepts_untouched_kit(self):
        kit, _ = self.make_kit()
        self.write_outputs(kit)
        result = run([os.path.join(SCRIPTS, "bank.py"), "--bank", self.bank, "add", "--from", kit, "--set", "fresh", "--author", "测试"])
        self.assertEqual(result.returncode, 0, result.stderr)
        question = next(q for q in self.load_bank()["questions"] if q["id"] == "fresh-demo")
        self.assertEqual(question["documents"], [DOC_B])
        self.assertTrue(os.path.isfile(os.path.join(self.bank, "refs", "fresh-demo.md")))

    def add(self, kit):
        return run([os.path.join(SCRIPTS, "bank.py"), "--bank", self.bank, "add", "--from", kit, "--set", "fresh", "--author", "测试"])

    def test_add_rejects_traversal_id_without_touching_existing_refs(self):
        kit, _ = self.make_kit()
        with open(os.path.join(kit, "out", "questions.json"), "w", encoding="utf-8") as handle:
            json.dump([{"id": "../refs/demo-one", "query": "新题面"}], handle, ensure_ascii=False)
        with open(os.path.join(kit, "out", "refs", "demo-one.md"), "w", encoding="utf-8") as handle:
            handle.write("出题方写的内容\n")
        before = snapshot(self.bank)
        result = self.add(kit)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(snapshot(self.bank), before, "非法 id 不能改动或删除已有标准答案")

    def test_add_rejects_id_with_trailing_newline(self):
        kit, _ = self.make_kit()
        self.write_outputs(kit)
        with open(os.path.join(kit, "out", "questions.json"), "w", encoding="utf-8") as handle:
            json.dump([{"id": "fresh-demo\n", "query": "星核兑换有上限吗？"}], handle, ensure_ascii=False)
        before = snapshot(self.bank)
        result = self.add(kit)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(snapshot(self.bank), before)

    def test_add_refuses_to_overwrite_existing_reference_file(self):
        kit, _ = self.make_kit()
        self.write_outputs(kit)
        orphan = os.path.join(self.bank, "refs", "fresh-demo.md")
        with open(orphan, "w", encoding="utf-8") as handle:
            handle.write("题库里已有的文件\n")
        before = snapshot(self.bank)
        result = self.add(kit)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("不覆盖", result.stderr)
        self.assertEqual(snapshot(self.bank), before)

    def test_add_rollback_removes_only_files_it_created(self):
        kit, _ = self.make_kit()
        questions = [{"id": "fresh-good", "query": "星核兑换有上限吗？"}, {"id": "fresh-bad", "query": "星核兑换在哪里？"}]
        with open(os.path.join(kit, "out", "questions.json"), "w", encoding="utf-8") as handle:
            json.dump(questions, handle, ensure_ascii=False)
        with open(os.path.join(kit, "out", "refs", "fresh-good.md"), "w", encoding="utf-8") as handle:
            handle.write("## 真值要点\n- 有上限\n\n## 判错条件\n- 无\n\n## 原文摘录\n> 兑换上限\n")
        with open(os.path.join(kit, "out", "refs", "fresh-bad.md"), "w", encoding="utf-8") as handle:
            handle.write("缺少真值要点一节\n")
        before = snapshot(self.bank)
        result = self.add(kit)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("fresh-bad", result.stderr)
        self.assertEqual(snapshot(self.bank), before, "回滚后题库应与入库前完全一致")

    def test_add_rejects_malformed_fields_without_leaving_files(self):
        for bad in ({"query": 123}, {"query": "  "}, {"query": "星核兑换有上限吗？", "why": 7}, {"query": "星核兑换有上限吗？", "case": "doc"}):
            kit = os.path.join(self.root, f"writer-{len(os.listdir(self.root))}")
            result = run([os.path.join(SCRIPTS, "bank.py"), "--bank", self.bank, "writer-kit", "--out", kit])
            self.assertEqual(result.returncode, 0, result.stderr)
            self.write_outputs(kit)
            with open(os.path.join(kit, "out", "questions.json"), "w", encoding="utf-8") as handle:
                json.dump([dict({"id": "fresh-demo"}, **bad)], handle, ensure_ascii=False)
            before = snapshot(self.bank)
            result = self.add(kit)
            if "case" in bad:
                # case 不是对象时只是取不到依据文档，题目本身合格，可以入库。
                self.assertEqual(result.returncode, 0, result.stderr)
                continue
            self.assertNotEqual(result.returncode, 0, f"{bad} 应被拒绝")
            self.assertNotIn("Traceback", result.stderr)
            self.assertEqual(snapshot(self.bank), before, f"{bad} 被拒绝后题库应不变")

    def test_check_reports_malformed_question_without_crashing(self):
        data = self.load_bank()
        data["questions"][0]["query"] = 123
        self.write_json(os.path.join(self.bank, "bank.json"), data)
        result = run([os.path.join(SCRIPTS, "bank.py"), "--bank", self.bank, "check"])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("query 必须是非空字符串", result.stdout)
        self.assertNotIn("Traceback", result.stderr)

    def test_add_refuses_kit_whose_browser_was_modified(self):
        kit, _ = self.make_kit()
        self.write_outputs(kit)
        browser = os.path.join(kit, "tools", "corpus.py")
        os.chmod(browser, stat.S_IREAD | stat.S_IWRITE)
        with open(browser, "a", encoding="utf-8") as handle:
            handle.write("\nopen('escape.txt', 'w').close()\n")
        before = snapshot(self.bank)
        result = run([os.path.join(SCRIPTS, "bank.py"), "--bank", self.bank, "add", "--from", kit, "--set", "fresh", "--author", "测试"])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("改动", result.stderr)
        self.assertEqual(snapshot(self.bank), before)


class MatchRefsTest(FixtureCase):
    def test_match_refs_records_documents_named_in_reference(self):
        before = snapshot(self.index)
        result = run([os.path.join(SCRIPTS, "bank.py"), "--bank", self.bank, "match-refs"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.load_bank()["questions"][0]["documents"], [DOC_A, DOC_B])
        self.assertEqual(snapshot(self.index), before)


class RunAbTest(FixtureCase):
    def test_refuses_to_overwrite_existing_answers(self):
        busy = os.path.join(self.root, "busy-cwd")
        os.makedirs(busy)
        open(os.path.join(busy, "keep.txt"), "w", encoding="utf-8").close()
        # 作答目录不为空：即使防覆盖检查失效，脚本也会在启动任何会话前退出。
        self.write_json(os.path.join(self.bank, "config.json"), {
            "corpus": {"index": self.index}, "server": "demo", "sides": {"A": {"command": "demo-server"}},
            "answer": {"model": "demo-model", "cwd": busy},
        })
        answer = os.path.join(self.bank, "runs", "round-1", "A", "demo-one-r1.answer.md")
        os.makedirs(os.path.dirname(answer))
        with open(answer, "w", encoding="utf-8") as handle:
            handle.write("旧答案")
        before = snapshot(os.path.join(self.bank, "runs"))
        result = run([os.path.join(SCRIPTS, "run_ab.py"), "--bank", self.bank, "--tag", "round-1", "--sides", "A", "--ids", "demo-one"])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("不覆盖", result.stderr)
        self.assertEqual(snapshot(os.path.join(self.bank, "runs")), before)


class JudgeTest(FixtureCase):
    def setUp(self):
        super().setUp()
        self.judge = load_script("judge")
        self.run_dir = os.path.join(self.bank, "runs", "round-1")
        for side, text in (("A", "答案甲"), ("B", "答案乙")):
            os.makedirs(os.path.join(self.run_dir, side))
            with open(os.path.join(self.run_dir, side, "demo-one-r1.answer.md"), "w", encoding="utf-8") as handle:
                handle.write(text)
        self.question = self.load_bank()["questions"][0]

    @staticmethod
    def verdict(prefer_x):
        """X 总是 5/4，Y 总是 4/4 且漏一点；胜方按 prefer_x。"""
        score = {"target": True, "correctness": 5, "completeness": 4, "errors": [], "missing": []}
        return {"X": dict(score), "Y": dict(score, correctness=4, missing=["漏了一点"]), "preference": "X" if prefer_x else "Y", "reason": "理由"}

    def judge_pair(self, both_orders=True):
        return self.judge.judge_pair({}, self.bank, self.run_dir, "round-1", self.question, 1, ["A", "B"], both_orders)

    def test_both_orders_cancel_position_bias(self):
        prompts = []

        def always_prefers_x(config, prompt):
            prompts.append(prompt)
            return self.verdict(True), 0.01

        self.judge.ask = always_prefers_x
        row = self.judge_pair()
        self.assertEqual(row["preferred"], "tie")
        self.assertEqual([item["mapping"]["X"] for item in row["orders"]], [row["mapping"]["X"], row["mapping"]["Y"]])
        self.assertTrue(any("# 答案 X\n答案甲" in prompt for prompt in prompts))
        self.assertTrue(any("# 答案 X\n答案乙" in prompt for prompt in prompts))
        for side in ("A", "B"):
            self.assertEqual(row["scores"][side]["correctness"], 4.5)
            self.assertEqual(row["scores"][side]["completeness"], 4)
            self.assertEqual(row["scores"][side]["missing"], ["漏了一点"])
            self.assertTrue(row["scores"][side]["target"])

    def test_both_orders_keep_a_consistent_winner(self):
        def prefers_b(config, prompt):
            return self.verdict("# 答案 X\n答案乙" in prompt), 0.01

        self.judge.ask = prefers_b
        row = self.judge_pair()
        self.assertEqual(row["preferred"], "B")
        self.assertEqual([item["preferred"] for item in row["orders"]], ["B", "B"])

    def test_parse_json_ignores_text_after_the_object(self):
        self.assertEqual(self.judge.parse_json('{"preference": "X", "reason": "a}b"}\n补充说明：{无关}'), {"preference": "X", "reason": "a}b"})
        self.assertEqual(self.judge.parse_json('```json\n{"preference": "Y"}\n```'), {"preference": "Y"})

    def test_single_order_keeps_a_stable_mapping(self):
        self.judge.ask = lambda config, prompt: (self.verdict(True), 0.01)
        first, second = self.judge_pair(False), self.judge_pair(False)
        self.assertEqual(first["mapping"], second["mapping"])
        self.assertNotIn("orders", first)
        self.assertEqual(first["preferred"], first["mapping"]["X"])


if __name__ == "__main__":
    unittest.main()
