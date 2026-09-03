import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import process from "node:process";
import { spawn } from "node:child_process";

const projectRoot = path.resolve(import.meta.dirname, "..");
const targets = new Map([
  ["win32-x64", { goos: "windows", goarch: "amd64", binary: "drag-core.exe" }],
  ["darwin-arm64", { goos: "darwin", goarch: "arm64", binary: "drag-core" }],
]);
const requested = process.argv.find((value) => value.startsWith("--target="))?.slice("--target=".length)
  ?? `${process.platform}-${process.arch}`;
const target = targets.get(requested);
if (!target) throw new Error(`不支持的 Go 核心目标：${requested}`);

function run(command, args, options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd: projectRoot,
      env: options.env ?? process.env,
      shell: false,
      windowsHide: true,
      stdio: options.capture ? ["ignore", "pipe", "pipe"] : "inherit",
    });
    let stdout = "";
    let stderr = "";
    if (options.capture) {
      child.stdout.setEncoding("utf8");
      child.stderr.setEncoding("utf8");
      child.stdout.on("data", (chunk) => { stdout += chunk; });
      child.stderr.on("data", (chunk) => { stderr += chunk; });
    }
    child.once("error", reject);
    child.once("exit", (code) => code === 0
      ? resolve({ stdout, stderr })
      : reject(new Error(`${command} 退出码 ${code}${stderr ? `\n${stderr}` : ""}`)));
  });
}

async function sha256(filePath) {
  const hash = createHash("sha256");
  hash.update(await readFile(filePath));
  return hash.digest("hex");
}

const outputDir = path.join(projectRoot, "dist", "native");
const outputPath = path.join(outputDir, target.binary);
await mkdir(outputDir, { recursive: true });
await run("go", ["build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", outputPath, "./go/cmd/drag-core"], {
  env: { ...process.env, CGO_ENABLED: "0", GOOS: target.goos, GOARCH: target.goarch },
});

const moduleInfo = await run("go", ["version", "-m", outputPath], { capture: true });
if (!moduleInfo.stdout.includes(`GOOS=${target.goos}`) || !moduleInfo.stdout.includes(`GOARCH=${target.goarch}`)) {
  throw new Error(`Go 二进制目标验证失败：${moduleInfo.stdout}`);
}
let runtimeVersion = null;
if (process.platform === (target.goos === "windows" ? "win32" : "darwin") && process.arch === (target.goarch === "amd64" ? "x64" : "arm64")) {
  const result = await run(outputPath, ["--version", "--json"], { capture: true });
  runtimeVersion = JSON.parse(result.stdout.trim());
}
const evidence = {
  target: requested,
  goos: target.goos,
  goarch: target.goarch,
  cgoEnabled: false,
  binary: path.relative(projectRoot, outputPath),
  bytes: (await readFile(outputPath)).byteLength,
  sha256: await sha256(outputPath),
  runtimeVersion,
  moduleInfo: moduleInfo.stdout.trim().split(/\r?\n/),
  createdAt: new Date().toISOString(),
};
await writeFile(path.join(outputDir, `drag-core-${requested}.json`), `${JSON.stringify(evidence, null, 2)}\n`, "utf8");
process.stdout.write(`${JSON.stringify(evidence, null, 2)}\n`);
