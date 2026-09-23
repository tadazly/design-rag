import { execFileSync } from "node:child_process";
import { readdir, readFile, stat } from "node:fs/promises";
import path from "node:path";
import process from "node:process";

const projectRoot = path.resolve(import.meta.dirname, "..");
const pluginRoot = path.resolve(projectRoot, process.argv[2] ?? "plugins/design-rag");
const packageJson = JSON.parse(await readFile(path.join(projectRoot, "package.json"), "utf8"));
const expectedVersion = process.argv[3] ?? packageJson.version;
const expectedModule = "github.com/tadazly/design-rag";
const expectedWebsite = "https://github.com/tadazly/design-rag";

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function capture(command, args) {
  return execFileSync(command, args, { cwd: projectRoot, encoding: "utf8", windowsHide: true }).trim();
}

const manifest = JSON.parse(await readFile(path.join(pluginRoot, ".codex-plugin/plugin.json"), "utf8"));
const mcp = JSON.parse(await readFile(path.join(pluginRoot, ".codex-mcp.json"), "utf8"));
const server = mcp.mcpServers?.["design-rag"];
assert(/^\d+\.\d+\.\d+$/.test(expectedVersion), `目标版本必须为严格 x.y.z：${expectedVersion}`);
assert(manifest.version === expectedVersion, `Plugin manifest 版本不一致：${manifest.version} != ${expectedVersion}`);
assert(manifest.interface?.websiteURL === expectedWebsite, "tag Plugin websiteURL 与公开网站不一致");
assert(manifest.mcpServers === "./.codex-mcp.json", "tag Codex manifest 必须引用 ./.codex-mcp.json");
assert(server?.command === "./bin/drag" && server?.cwd === "." && JSON.stringify(server?.args) === '["mcp"]', "tag Plugin 必须使用跨平台 ./bin/drag mcp");

// Claude Code 会自动加载根目录 .mcp.json，且按用户项目目录解析相对 command。
const claudeManifest = JSON.parse(await readFile(path.join(pluginRoot, ".claude-plugin/plugin.json"), "utf8"));
const claudeServers = claudeManifest.mcpServers ?? {};
const claudeServer = claudeServers["design-rag"];
// Claude marketplace 以条目 version 决定缓存目录和是否更新，两份 manifest 必须同名同版本。
assert(manifest.name === "design-rag" && claudeManifest.name === "design-rag", "tag Codex 与 Claude manifest name 必须都是 design-rag");
assert(claudeManifest.version === manifest.version, `Claude 与 Codex manifest 版本不一致：${claudeManifest.version} != ${manifest.version}`);
assert(claudeManifest.version === expectedVersion, `Claude manifest 版本不一致：${claudeManifest.version} != ${expectedVersion}`);
assert(claudeManifest.homepage === expectedWebsite, "tag Claude manifest homepage 与公开网站不一致");
assert(Object.keys(claudeServers).length === 1 && claudeServer?.command === "${CLAUDE_PLUGIN_ROOT}/bin/drag" && JSON.stringify(claudeServer?.args) === '["mcp"]', "tag Claude Plugin 必须使用跨平台 ${CLAUDE_PLUGIN_ROOT}/bin/drag mcp");
assert(claudeServer.env === undefined && claudeServer.cwd === undefined, "tag Claude MCP 不得内置 env 或 cwd");
await stat(path.join(pluginRoot, ".mcp.json")).then(
  () => { throw new Error("tag Plugin 不得包含 Claude Code 会自动加载的根目录 .mcp.json"); },
  (error) => { if (error.code !== "ENOENT") throw error; },
);

for (const legalFile of ["LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md"]) {
  assert((await stat(path.join(pluginRoot, legalFile))).isFile(), `tag Plugin 缺少 ${legalFile}`);
}
assert((await stat(path.join(pluginRoot, "THIRD_PARTY_NOTICES"))).isDirectory(), "tag Plugin 缺少第三方许可证目录");

const binRoot = path.join(pluginRoot, "bin");
const binEntries = (await readdir(binRoot, { withFileTypes: true })).filter((entry) => entry.isFile()).map((entry) => entry.name).sort();
assert(JSON.stringify(binEntries) === JSON.stringify(["drag", "drag.exe"]), `tag Plugin bin 必须且只能包含 drag 与 drag.exe：${binEntries.join(", ")}`);
if (process.platform !== "win32") {
  const mode = (await stat(path.join(binRoot, "drag"))).mode & 0o777;
  assert((mode & 0o111) !== 0, "macOS drag 缺少可执行 mode");
}

const targets = [
  { file: "drag.exe", goos: "windows", goarch: "amd64" },
  { file: "drag", goos: "darwin", goarch: "arm64" },
];
const forbidden = ["s" + "plan", "tao" + "mee.com", "s" + "plan-frontend"];
const results = [];
for (const target of targets) {
  const binaryPath = path.join(binRoot, target.file);
  const info = capture("go", ["version", "-m", binaryPath]);
  assert(info.includes(`\n\tpath\t${expectedModule}/go/cmd/drag`), `${target.file} Go main path 不正确`);
  assert(info.includes(`\n\tmod\t${expectedModule}\t`), `${target.file} Go module 不正确`);
  assert(info.includes("-trimpath=true"), `${target.file} 未启用 trimpath`);
  assert(info.includes(`GOOS=${target.goos}`) && info.includes(`GOARCH=${target.goarch}`), `${target.file} 目标平台不正确`);
  const raw = (await readFile(binaryPath)).toString("latin1").toLowerCase();
  assert(raw.includes(expectedVersion.toLowerCase()), `${target.file} 未包含版本 ${expectedVersion}`);
  for (const value of forbidden) assert(!raw.includes(value), `${target.file} 包含禁止字符串`);
  results.push({ file: target.file, platform: `${target.goos}/${target.goarch}`, status: "PASS" });
}

process.stdout.write(`${JSON.stringify({ status: "PASS", version: expectedVersion, pluginRoot: path.relative(projectRoot, pluginRoot).replaceAll("\\", "/"), binaries: results }, null, 2)}\n`);
