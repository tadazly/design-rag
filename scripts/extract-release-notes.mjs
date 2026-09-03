import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import process from "node:process";

const projectRoot = path.resolve(process.argv[4] ?? path.resolve(import.meta.dirname, ".."));
const version = process.argv[2];
const outputPath = process.argv[3];
if (!/^\d+\.\d+\.\d+$/.test(version ?? "") || !outputPath) {
  throw new Error("用法：node scripts/extract-release-notes.mjs X.Y.Z <output-path> [project-root]");
}

const changelog = await readFile(path.join(projectRoot, "CHANGELOG.md"), "utf8");
const heading = new RegExp(`^## \\[${version.replaceAll(".", "\\.")}\\] - \\d{4}-\\d{2}-\\d{2}\\s*$`, "m");
const match = heading.exec(changelog);
if (!match) throw new Error(`CHANGELOG.md 缺少版本 ${version} 的发布日期章节`);
const start = match.index + match[0].length;
const remainder = changelog.slice(start);
const next = /^## \[/m.exec(remainder);
const body = remainder.slice(0, next?.index ?? remainder.length).trim();
if (!body) throw new Error(`CHANGELOG.md 的 ${version} 章节为空`);
await writeFile(path.resolve(projectRoot, outputPath), `${body}\n`, "utf8");
process.stdout.write(`${JSON.stringify({ status: "PASS", version, output: outputPath }, null, 2)}\n`);
