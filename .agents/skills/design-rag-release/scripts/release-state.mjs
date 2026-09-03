import { execFileSync } from "node:child_process";
import { access, readFile } from "node:fs/promises";
import path from "node:path";
import process from "node:process";

const projectRoot = path.resolve(import.meta.dirname, "../../../..");

function capture(command, args, options = {}) {
  return execFileSync(command, args, {
    cwd: projectRoot,
    encoding: "utf8",
    windowsHide: true,
    stdio: ["ignore", "pipe", "pipe"],
    ...options,
  }).trim();
}

function captureRaw(command, args) {
  return execFileSync(command, args, {
    cwd: projectRoot,
    encoding: "utf8",
    windowsHide: true,
    stdio: ["ignore", "pipe", "pipe"],
  }).replace(/\r?\n$/, "");
}

function extract(source, pattern, label) {
  const match = pattern.exec(source);
  if (!match?.[1]) throw new Error(`无法读取 ${label}`);
  return match[1];
}

function parseSemver(value) {
  const match = /^(\d+)\.(\d+)\.(\d+)$/.exec(value);
  if (!match) throw new Error(`不是严格 x.y.z 版本：${value}`);
  return match.slice(1).map(Number);
}

function bump(version, level) {
  const [major, minor, patch] = parseSemver(version);
  if (level === "major") return `${major + 1}.0.0`;
  if (level === "minor") return `${major}.${minor + 1}.0`;
  return `${major}.${minor}.${patch + 1}`;
}

function compareSemver(left, right) {
  const a = parseSemver(left);
  const b = parseSemver(right);
  for (let index = 0; index < 3; index += 1) {
    if (a[index] !== b[index]) return a[index] - b[index];
  }
  return 0;
}

const packageJson = JSON.parse(await readFile(path.join(projectRoot, "package.json"), "utf8"));
const packageLock = JSON.parse(await readFile(path.join(projectRoot, "package-lock.json"), "utf8"));
const plugin = JSON.parse(await readFile(path.join(projectRoot, "plugins/design-rag/.codex-plugin/plugin.json"), "utf8"));
const goModel = await readFile(path.join(projectRoot, "go/core/model.go"), "utf8");
const contracts = await readFile(path.join(projectRoot, "src/shared/contracts.ts"), "utf8");

const currentVersion = packageJson.version;
parseSemver(currentVersion);
const versions = {
  repository: currentVersion,
  packageLock: packageLock.version,
  packageLockRoot: packageLock.packages?.[""]?.version,
  plugin: plugin.version,
  backend: extract(goModel, /BackendVersion\s*=\s*"([^"]+)"/, "Go backend 版本"),
  gui: extract(contracts, /APP_VERSION\s*=\s*"([^"]+)"/, "GUI 版本"),
};
const versionConsistent = Object.values(versions).every((value) => value === currentVersion);

const branch = capture("git", ["branch", "--show-current"]);
const head = capture("git", ["rev-parse", "HEAD"]);
const status = captureRaw("git", ["status", "--porcelain=v1"]);
let ahead = null;
let behind = null;
try {
  const counts = capture("git", ["rev-list", "--left-right", "--count", "origin/main...HEAD"]).split(/\s+/).map(Number);
  [behind, ahead] = counts;
} catch {
  // origin/main may not exist before the caller fetches.
}

let latestTag = null;
try {
  // Release tags point to detached distribution commits whose parent is the
  // source commit on main, so they are intentionally not merged into HEAD.
  latestTag = capture("git", ["tag", "--list", "v[0-9]*.[0-9]*.[0-9]*", "--sort=-version:refname"]).split(/\r?\n/)[0] || null;
} catch {
  // First release.
}

const range = latestTag ? `${latestTag}..HEAD` : "HEAD";
const subjects = capture("git", ["log", "--format=%s%n%b%x00", range]).split("\0").map((value) => value.trim()).filter(Boolean);
let level = "patch";
let reason = "没有识别到 feature 或 breaking 标记；发布时至少递增 patch";
if (subjects.some((value) => /(^|\n)BREAKING[ -]CHANGE\s*:|^[a-z]+(?:\([^)]*\))?!:/im.test(value))) {
  const [major] = parseSemver(latestTag?.slice(1) ?? currentVersion);
  level = major === 0 ? "minor" : "major";
  reason = major === 0 ? "检测到 0.x 阶段 breaking change" : "检测到 breaking change";
} else if (subjects.some((value) => /^feat(?:\([^)]*\))?:/im.test(value))) {
  level = "minor";
  reason = "检测到向后兼容的 feature commit";
} else if (subjects.some((value) => /^(fix|perf)(?:\([^)]*\))?:/im.test(value))) {
  reason = "检测到 fix/perf commit";
}

let suggestedVersion;
let versionOrderValid = true;
if (!latestTag) {
  suggestedVersion = currentVersion;
  reason = "仓库没有正式版本 tag；当前一致版本作为首个发布候选";
} else if (latestTag === `v${currentVersion}`) {
  suggestedVersion = bump(currentVersion, level);
} else if (compareSemver(currentVersion, latestTag.slice(1)) > 0) {
  suggestedVersion = currentVersion;
  reason = `当前版本尚未以同名 tag 发布；先核对它是否为待发布版本（变化等级线索：${level}）`;
} else {
  suggestedVersion = null;
  versionOrderValid = false;
  reason = `当前版本 ${currentVersion} 不高于最新 tag ${latestTag}`;
}

const releaseWorkflowPresent = await access(path.join(projectRoot, ".github/workflows/release.yml")).then(() => true, () => false);
const blockers = [];
if (branch !== "main") blockers.push(`当前分支不是 main：${branch || "<detached>"}`);
if (ahead === null || behind === null) blockers.push("无法比较 origin/main 与 HEAD");
else if (ahead !== 0 || behind !== 0) blockers.push(`HEAD 与 origin/main 未同步：ahead=${ahead}, behind=${behind}`);
if (!versionConsistent) blockers.push("版本矩阵不一致");
if (!versionOrderValid) blockers.push("当前版本不高于最新正式 tag");
if (!releaseWorkflowPresent) blockers.push("缺少 .github/workflows/release.yml");

const output = {
  status: blockers.length === 0 ? "PASS" : "FAIL",
  branch,
  head,
  clean: status === "",
  changedFiles: status ? status.split(/\r?\n/) : [],
  publicationReady: blockers.length === 0 && status === "",
  blockers,
  reviewRequired: status === "" ? [] : ["工作区改动必须确认全部属于本次发布范围"],
  originMain: { ahead, behind, synchronized: ahead === 0 && behind === 0 },
  versions,
  versionConsistent,
  versionOrderValid,
  latestTag,
  commitRange: range,
  inferredChangeLevel: level,
  suggestedVersion,
  reason,
  releaseWorkflowPresent,
};

process.stdout.write(`${JSON.stringify(output, null, 2)}\n`);
if (blockers.length > 0) process.exitCode = 1;
