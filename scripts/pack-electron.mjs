import { spawn } from "node:child_process";
import { readFile, readdir, stat } from "node:fs/promises";
import path from "node:path";
import process from "node:process";

const projectRoot = path.resolve(import.meta.dirname, "..");
const builderCli = path.join(projectRoot, "node_modules", "electron-builder", "cli.js");
const stagingPath = path.join(projectRoot, "release", "win-unpacked.tmp");
const packageJson = JSON.parse(await readFile(path.join(projectRoot, "package.json"), "utf8"));
const expectedElectronVersion = String(packageJson.devDependencies?.electron ?? "").replace(/^[^0-9]*/, "");

async function runBuilder(args) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [builderCli, ...args], {
      cwd: projectRoot,
      env: process.env,
      shell: false,
      windowsHide: true,
      stdio: ["inherit", "pipe", "pipe"],
    });
    let output = "";
    child.stdout.setEncoding("utf8");
    child.stderr.setEncoding("utf8");
    child.stdout.on("data", (chunk) => {
      output += chunk;
      process.stdout.write(chunk);
    });
    child.stderr.on("data", (chunk) => {
      output += chunk;
      process.stderr.write(chunk);
    });
    child.once("error", reject);
    child.once("exit", (code) => resolve({ code: code ?? 1, output }));
  });
}

async function stagingLooksComplete() {
  try {
    const required = [
      "electron.exe",
      "icudtl.dat",
      "resources.pak",
      "snapshot_blob.bin",
      "v8_context_snapshot.bin",
      path.join("resources", "default_app.asar"),
      path.join("locales", "en-US.pak"),
    ];
    for (const relativePath of required) {
      const info = await stat(path.join(stagingPath, relativePath));
      if (!info.isFile() || info.size === 0) return false;
    }
    const actualVersion = (await readFile(path.join(stagingPath, "version"), "utf8")).trim();
    if (expectedElectronVersion && actualVersion !== expectedElectronVersion) return false;
    const entries = await readdir(stagingPath, { recursive: true, withFileTypes: true });
    const files = entries.filter((entry) => entry.isFile());
    if (files.length < 50) return false;
    let totalBytes = 0;
    for (const entry of files) {
      totalBytes += (await stat(path.join(entry.parentPath, entry.name))).size;
    }
    return totalBytes > 200 * 1024 * 1024;
  } catch {
    return false;
  }
}

async function buildFromStaging() {
  process.stderr.write("检测到完整的 Electron staging；使用 electronDist 复制封装，绕过 Windows 目录 rename 短暂锁。\n");
  return runBuilder(["--dir", `-c.electronDist=${path.relative(projectRoot, stagingPath)}`]);
}

let result;
if (process.platform === "win32" && await stagingLooksComplete()) {
  result = await buildFromStaging();
} else {
  result = await runBuilder(["--dir"]);
  const knownRenameLock = /EPERM:[^\r\n]*rename[^\r\n]*win-unpacked\.tmp[^\r\n]*win-unpacked/i.test(result.output);
  if (result.code !== 0 && process.platform === "win32" && knownRenameLock && await stagingLooksComplete()) {
    result = await buildFromStaging();
  }
}

if (result.code !== 0) process.exitCode = result.code;
