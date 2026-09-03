import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import process from "node:process";

const projectRoot = path.resolve(process.argv[3] ?? path.resolve(import.meta.dirname, "../../../.."));
const version = process.argv[2];
if (!/^\d+\.\d+\.\d+$/.test(version ?? "")) {
  throw new Error("用法：node set-version.mjs X.Y.Z [project-root]；正式版本必须为严格 x.y.z");
}

async function updateJson(relativePath, mutate) {
  const filePath = path.join(projectRoot, relativePath);
  const value = JSON.parse(await readFile(filePath, "utf8"));
  mutate(value);
  await writeFile(filePath, `${JSON.stringify(value, null, 2)}\n`, "utf8");
}

async function replaceOnce(relativePath, pattern, replacement, label) {
  const filePath = path.join(projectRoot, relativePath);
  const source = await readFile(filePath, "utf8");
  const matches = source.match(new RegExp(pattern.source, `${pattern.flags.includes("g") ? pattern.flags : `${pattern.flags}g`}`));
  if (matches?.length !== 1) throw new Error(`${label} 预期命中 1 次，实际 ${matches?.length ?? 0} 次`);
  await writeFile(filePath, source.replace(pattern, replacement), "utf8");
}

await updateJson("package.json", (value) => { value.version = version; });
await updateJson("package-lock.json", (value) => {
  value.version = version;
  if (!value.packages?.[""]) throw new Error("package-lock.json 缺少根 package");
  value.packages[""].version = version;
});
await updateJson("plugins/design-rag/.codex-plugin/plugin.json", (value) => { value.version = version; });
await replaceOnce("go/core/model.go", /BackendVersion\s*=\s*"[^"]+"/, `BackendVersion  = "${version}"`, "Go BackendVersion");
await replaceOnce("src/shared/contracts.ts", /APP_VERSION\s*=\s*"[^"]+"/, `APP_VERSION = "${version}"`, "GUI APP_VERSION");

process.stdout.write(`${JSON.stringify({ status: "PASS", version }, null, 2)}\n`);
