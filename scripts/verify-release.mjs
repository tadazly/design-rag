import { execFileSync } from "node:child_process";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import process from "node:process";

const projectRoot = path.resolve(import.meta.dirname, "..");
const expectedRepository = "https://github.com/tadazly/design-rag.git";
const expectedModule = "github.com/tadazly/design-rag";

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

async function readJson(relativePath) {
  return JSON.parse(await readFile(path.join(projectRoot, relativePath), "utf8"));
}

function capture(command, args) {
  return execFileSync(command, args, { cwd: projectRoot, encoding: "utf8", windowsHide: true }).trim();
}

function extractVersion(source, pattern, label) {
  const match = pattern.exec(source);
  assert(match?.[1], `无法读取 ${label} 版本`);
  return match[1];
}

function lineNumber(source, offset) {
  return source.slice(0, offset).split(/\r?\n/).length;
}

const packageJson = await readJson("package.json");
const packageLock = await readJson("package-lock.json");
const pluginManifest = await readJson("plugins/design-rag/.codex-plugin/plugin.json");
const goModel = await readFile(path.join(projectRoot, "go/core/model.go"), "utf8");
const contracts = await readFile(path.join(projectRoot, "src/shared/contracts.ts"), "utf8");
const goModule = await readFile(path.join(projectRoot, "go.mod"), "utf8");
const electronBuilder = await readFile(path.join(projectRoot, "electron-builder.yml"), "utf8");
const releaseWorkflow = await readFile(path.join(projectRoot, ".github/workflows/release.yml"), "utf8");
const readme = await readFile(path.join(projectRoot, "README.md"), "utf8");
const license = await readFile(path.join(projectRoot, "LICENSE"), "utf8");
const notice = await readFile(path.join(projectRoot, "NOTICE"), "utf8");

const version = packageJson.version;
const backendVersion = extractVersion(goModel, /BackendVersion\s*=\s*"([^"]+)"/, "Go backend");
const appVersion = extractVersion(contracts, /APP_VERSION\s*=\s*"([^"]+)"/, "GUI");
const versions = {
  repository: version,
  packageLock: packageLock.version,
  packageLockRoot: packageLock.packages?.[""]?.version,
  plugin: pluginManifest.version,
  backend: backendVersion,
  gui: appVersion,
};

assert(/^\d+\.\d+\.\d+$/.test(version), `发布版本必须为严格 x.y.z：${version}`);
for (const [component, componentVersion] of Object.entries(versions)) {
  assert(componentVersion === version, `版本不一致：${component}=${componentVersion ?? "<missing>"}, expected=${version}`);
}

assert(packageJson.author === "tadazly", "package author 必须为 tadazly");
assert(packageJson.license === "Apache-2.0", "package license 必须为 Apache-2.0");
assert(packageJson.repository === expectedRepository, "package repository 与公开仓库不一致");
assert(pluginManifest.name === "design-rag", "Plugin 技术 ID 必须为 design-rag");
assert(pluginManifest.author?.name === "tadazly", "Plugin author 必须为 tadazly");
assert(pluginManifest.interface?.developerName === "tadazly", "Plugin developerName 必须为 tadazly");
assert(pluginManifest.interface?.displayName === "DRAG 游戏策划知识库", "Plugin 显示名不正确");
assert(pluginManifest.repository === expectedRepository, "Plugin repository 与公开仓库不一致");
assert(pluginManifest.license === "Apache-2.0", "Plugin license 必须为 Apache-2.0");
assert(goModule.startsWith(`module ${expectedModule}\n`) || goModule.startsWith(`module ${expectedModule}\r\n`), "Go module 路径不正确");
assert(/^appId:\s*com\.luyilabs\.design-rag\s*$/m.test(electronBuilder), "Electron appId 不正确");
assert(!/^\s*-\s+target:\s+zip\s*$/m.test(electronBuilder), "macOS GUI Release 不应再生成重复 ZIP");
assert(!releaseWorkflow.includes("design-rag-gui-*-mac-arm64.zip"), "Release workflow 不应收集 macOS GUI ZIP");
assert(!releaseWorkflow.includes("release-assets/*.json"), "Release workflow 不应公开上传 evidence JSON");
assert(releaseWorkflow.includes("release-audit/release-evidence.json"), "Release workflow 必须保留独立审计 evidence");
assert(/name:\s*release-audit-\$\{\{ inputs\.version \}\}[\s\S]*?retention-days:\s*90/.test(releaseWorkflow), "Release 审计 artifact 必须保留 90 天");
assert(license.includes("Apache License") && license.includes("Version 2.0, January 2004"), "LICENSE 不是 Apache License 2.0");
assert(notice.includes("Copyright 2026 tadazly") && notice.includes(expectedRepository), "NOTICE 内容不完整");
assert(!/\b\d+\.\d+\.\d+\b/.test(readme), "README 不应维护发布版本号");

const notesTempRoot = await mkdtemp(path.join(tmpdir(), "design-rag-release-notes-"));
try {
  const evidencePath = path.join(notesTempRoot, "release-evidence.json");
  const notesPath = path.join(notesTempRoot, "RELEASE_NOTES.md");
  await writeFile(evidencePath, `${JSON.stringify({ signing: { windows: "unsigned", macos: "unsigned", notarized: false } }, null, 2)}\n`, "utf8");
  capture(process.execPath, ["scripts/extract-release-notes.mjs", version, notesPath, projectRoot, evidencePath]);
  const releaseNotes = await readFile(notesPath, "utf8");
  assert(releaseNotes.includes(`design-rag-local-${version}-win32-x64.zip`) && releaseNotes.includes("Codex Plugin"), "Release Notes 缺少 Windows Plugin 用途");
  assert(releaseNotes.includes(`design-rag-local-${version}-darwin-arm64.zip`) && releaseNotes.includes(`design-rag-gui-${version}-mac-arm64.dmg`), "Release Notes 缺少 macOS Plugin 或 GUI 用途");
  assert(!releaseNotes.includes(`design-rag-gui-${version}-mac-arm64.zip`), "Release Notes 不应列出已移除的 macOS GUI ZIP");
  assert(releaseNotes.includes("Windows 分发产物：未签名") && releaseNotes.includes("未完成 Apple notarization"), "Release Notes 缺少签名或 notarization 状态");
} finally {
  await rm(notesTempRoot, { recursive: true, force: true });
}

const forbiddenPatterns = [
  ["旧品牌名", new RegExp("s\\s*(?:plan|计划)", "i")],
  ["旧引用前缀", new RegExp(["s", "p", ":"].join(""), "i")],
  ["内部域名", new RegExp(["tao", "mee\\.com"].join(""), "i")],
  ["内部组织路径", new RegExp(["s", "plan-frontend"].join(""), "i")],
  ["内部人员标识", new RegExp(["al", "berts"].join(""), "i")],
  ["旧作者署名", /\bLuyi\b/i],
];

const tracked = capture("git", ["ls-files", "-z", "--cached", "--others", "--exclude-standard"]).split("\0").filter(Boolean);
const sensitiveMatches = [];
for (const relativePath of tracked) {
  const raw = await readFile(path.join(projectRoot, relativePath));
  if (raw.includes(0)) continue;
  const source = raw.toString("utf8");
  for (const [label, pattern] of forbiddenPatterns) {
    const match = pattern.exec(source);
    if (match) sensitiveMatches.push(`${relativePath}:${lineNumber(source, match.index)} [${label}]`);
  }
}
assert(sensitiveMatches.length === 0, `敏感词门禁失败：\n${sensitiveMatches.join("\n")}`);

const tag = process.env.GITHUB_REF_TYPE === "tag"
  ? process.env.GITHUB_REF_NAME
  : process.env.CI_COMMIT_TAG;
if (tag) {
  assert(tag === `v${version}`, `tag 必须与版本一致：${tag} != v${version}`);
  const authorEmail = capture("git", ["log", "-1", "--format=%ae"]);
  const internalEmail = new RegExp(["@tao", "mee\\.com$"].join(""), "i");
  assert(!internalEmail.test(authorEmail), `tag commit 使用了内部邮箱：${authorEmail}`);
}

process.stdout.write(`${JSON.stringify({ status: "PASS", version, protocolVersion: 3, tag: tag ?? null, trackedFilesScanned: tracked.length }, null, 2)}\n`);
