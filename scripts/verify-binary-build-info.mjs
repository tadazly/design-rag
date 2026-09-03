import { execFileSync } from "node:child_process";
import { access, readFile } from "node:fs/promises";
import path from "node:path";
import process from "node:process";

const projectRoot = path.resolve(import.meta.dirname, "..");
const packageJson = JSON.parse(await readFile(path.join(projectRoot, "package.json"), "utf8"));
const expectedModule = "github.com/tadazly/design-rag";

function capture(command, args) {
  return execFileSync(command, args, { cwd: projectRoot, encoding: "utf8", windowsHide: true }).trim();
}

const defaults = [
  "dist/native/drag-core.exe",
  "dist/native/drag-core",
  "tests/.tmp/plugin-stage/win32-x64/design-rag-local/plugins/design-rag/bin/drag.exe",
  "tests/.tmp/plugin-stage/darwin-arm64/design-rag-local/plugins/design-rag/bin/drag",
];
const candidates = process.argv.slice(2).length > 0 ? process.argv.slice(2) : defaults;
const binaries = [];
for (const candidate of candidates) {
  const absolutePath = path.resolve(projectRoot, candidate);
  try {
    await access(absolutePath);
    binaries.push({ relativePath: path.relative(projectRoot, absolutePath), absolutePath });
  } catch {
    if (process.argv.slice(2).length > 0) throw new Error(`二进制不存在：${candidate}`);
  }
}
if (binaries.length === 0) throw new Error("没有找到可检查的 Go 二进制");

const forbidden = [
  ["旧品牌", ["s", "plan"].join("")],
  ["内部域名", ["tao", "mee.com"].join("")],
  ["内部组织路径", ["s", "plan-frontend"].join("")],
];
const results = [];
for (const binary of binaries) {
  const moduleInfo = capture("go", ["version", "-m", binary.absolutePath]);
  if (!moduleInfo.includes(`\n\tpath\t${expectedModule}/go/cmd/`)
    || !moduleInfo.includes(`\n\tmod\t${expectedModule}\t`)
    || !moduleInfo.includes("-trimpath=true")) {
    throw new Error(`Go build info 不符合发布要求：${binary.relativePath}\n${moduleInfo}`);
  }
  const rawText = (await readFile(binary.absolutePath)).toString("latin1").toLowerCase();
  if (!rawText.includes(packageJson.version.toLowerCase())) {
    throw new Error(`${binary.relativePath} 未包含发布版本 ${packageJson.version}`);
  }
  for (const [label, value] of forbidden) {
    if (rawText.includes(value.toLowerCase())) throw new Error(`${binary.relativePath} 仍包含${label}`);
  }

  let runtimeVersion = null;
  const isNativeWindows = process.platform === "win32" && binary.absolutePath.toLowerCase().endsWith(".exe");
  const isNativeMac = process.platform === "darwin" && !binary.absolutePath.toLowerCase().endsWith(".exe");
  if (isNativeWindows || isNativeMac) {
    const version = JSON.parse(capture(binary.absolutePath, ["--version", "--json"]));
    runtimeVersion = version.version;
    if (runtimeVersion !== packageJson.version) {
      throw new Error(`${binary.relativePath} 运行时版本 ${runtimeVersion} 与 ${packageJson.version} 不一致`);
    }
  }
  results.push({ path: binary.relativePath.replaceAll("\\", "/"), runtimeVersion, status: "PASS" });
}

process.stdout.write(`${JSON.stringify({ status: "PASS", version: packageJson.version, binaries: results }, null, 2)}\n`);
