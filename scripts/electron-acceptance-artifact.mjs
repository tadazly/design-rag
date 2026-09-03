import { createHash } from "node:crypto";
import { readdir, readFile, stat } from "node:fs/promises";
import path from "node:path";
import { extractFile } from "@electron/asar";

function sha256Buffer(value) {
  return createHash("sha256").update(value).digest("hex");
}

async function listFiles(root, relative = "") {
  const files = [];
  for (const entry of await readdir(path.join(root, relative), { withFileTypes: true })) {
    const child = path.join(relative, entry.name);
    if (entry.isDirectory()) files.push(...await listFiles(root, child));
    else if (entry.isFile()) files.push(child);
  }
  return files;
}

export async function verifyCurrentElectronArtifact(projectRoot, executable) {
  const executableInfo = await stat(executable);
  if (!executableInfo.isFile()) throw new Error(`Electron 验收目标不是文件：${executable}`);
  const asarPath = path.join(path.dirname(executable), "resources", "app.asar");
  const asarInfo = await stat(asarPath);
  if (!asarInfo.isFile()) throw new Error(`Electron 验收目标缺少 app.asar：${asarPath}`);

  const distRoot = path.join(projectRoot, "dist");
  const runtimeDirectories = ["core", "main", "shared", "renderer"];
  const relativeFiles = [];
  for (const directory of runtimeDirectories) {
    relativeFiles.push(...(await listFiles(path.join(distRoot, directory)))
      .filter((file) => !file.endsWith(".d.ts") && !file.endsWith(".d.ts.map"))
      .map((file) => path.join(directory, file)));
  }

  const mismatches = [];
  for (const relative of relativeFiles) {
    const current = await readFile(path.join(distRoot, relative));
    let packaged;
    try {
      packaged = extractFile(asarPath, path.join("dist", relative));
    } catch (error) {
      mismatches.push({ relative, reason: `packaged missing: ${error instanceof Error ? error.message : String(error)}` });
      continue;
    }
    const currentSha256 = sha256Buffer(current);
    const packagedSha256 = sha256Buffer(packaged);
    if (currentSha256 !== packagedSha256) mismatches.push({ relative, currentSha256, packagedSha256 });
  }
  const nativeRelativeFiles = ["drag-core.exe", "drag-core-win32-x64.json"];
  const packagedNativeRoot = path.join(path.dirname(asarPath), "app.asar.unpacked", "dist", "native");
  const nativeArtifacts = [];
  for (const relative of nativeRelativeFiles) {
    const currentPath = path.join(distRoot, "native", relative);
    const packagedPath = path.join(packagedNativeRoot, relative);
    try {
      const [current, packaged] = await Promise.all([readFile(currentPath), readFile(packagedPath)]);
      const currentSha256 = sha256Buffer(current);
      const packagedSha256 = sha256Buffer(packaged);
      let semanticMatched = currentSha256 === packagedSha256;
      if (relative.endsWith(".json")) {
        const currentManifest = JSON.parse(current.toString("utf8"));
        const packagedManifest = JSON.parse(packaged.toString("utf8"));
        delete currentManifest.createdAt;
        delete packagedManifest.createdAt;
        semanticMatched = JSON.stringify(currentManifest) === JSON.stringify(packagedManifest);
      }
      nativeArtifacts.push({ relative, currentPath, packagedPath, currentSha256, packagedSha256, semanticMatched });
      if (!semanticMatched) mismatches.push({ relative: path.join("native", relative), currentSha256, packagedSha256 });
    } catch (error) {
      mismatches.push({ relative: path.join("native", relative), reason: error instanceof Error ? error.message : String(error) });
    }
  }
  if (mismatches.length > 0) {
    throw new Error(`drag-gui.exe 不是当前 dist 构建，拒绝用 stale EXE 验收；请先重建。差异：${JSON.stringify(mismatches.slice(0, 12))}`);
  }

  return {
    executable,
    executableSizeBytes: executableInfo.size,
    executableModifiedAt: executableInfo.mtime.toISOString(),
    executableSha256: sha256Buffer(await readFile(executable)),
    appAsar: asarPath,
    appAsarSizeBytes: asarInfo.size,
    appAsarSha256: sha256Buffer(await readFile(asarPath)),
    checkedRuntimeFiles: relativeFiles.length + nativeRelativeFiles.length,
    nativeArtifacts,
    currentDistMatched: true,
  };
}
