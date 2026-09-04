import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import process from "node:process";

const projectRoot = path.resolve(process.argv[4] ?? path.resolve(import.meta.dirname, ".."));
const version = process.argv[2];
const outputPath = process.argv[3];
const evidencePath = process.argv[5];
if (!/^\d+\.\d+\.\d+$/.test(version ?? "") || !outputPath) {
  throw new Error("用法：node scripts/extract-release-notes.mjs X.Y.Z <output-path> [project-root] [release-evidence-path]");
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

const downloads = `## 下载与安装

| 文件 | 用途 |
|---|---|
| \`design-rag-local-${version}-win32-x64.zip\` | Codex Plugin 的 Windows x64 本地或离线安装包；只使用 Codex Plugin 时下载。 |
| \`design-rag-local-${version}-darwin-arm64.zip\` | Codex Plugin 的 Apple Silicon macOS 本地或离线安装包；只使用 Codex Plugin 时下载。 |
| \`design-rag-gui-${version}-win-x64.exe\` | Windows x64 桌面客户端。 |
| \`design-rag-gui-${version}-mac-arm64.dmg\` | Apple Silicon macOS 桌面客户端。 |
| \`SHA256SUMS.txt\` | 上述四个安装包的 SHA-256 校验值。 |

GitHub 自动附加的 Source code ZIP/TAR 是对应 tag 的源码快照，不是推荐的 Plugin 或桌面客户端安装包。`;

let signing = "";
if (evidencePath) {
  const evidence = JSON.parse(await readFile(path.resolve(projectRoot, evidencePath), "utf8"));
  const windows = evidence.signing?.windows;
  const macos = evidence.signing?.macos;
  const notarized = evidence.signing?.notarized;
  if (!new Set(["signed", "unsigned"]).has(windows) || !new Set(["signed", "unsigned"]).has(macos) || typeof notarized !== "boolean") {
    throw new Error("release evidence 缺少有效的 signing.windows、signing.macos 或 signing.notarized");
  }
  const signingLabel = (value) => value === "signed" ? "已签名" : "未签名";
  signing = `## 签名与公证

- Windows 分发产物：${signingLabel(windows)}。
- macOS 分发产物：${signingLabel(macos)}，${notarized ? "已完成 Apple notarization" : "未完成 Apple notarization"}。`;
}

const notes = [body, downloads, signing].filter(Boolean).join("\n\n");
await writeFile(path.resolve(projectRoot, outputPath), `${notes}\n`, "utf8");
process.stdout.write(`${JSON.stringify({ status: "PASS", version, output: outputPath, includesSigning: Boolean(signing) }, null, 2)}\n`);
