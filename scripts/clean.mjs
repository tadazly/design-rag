import { rm } from "node:fs/promises";

await Promise.all(
  ["dist", "dist-tests"].map((target) =>
    rm(new URL(`../${target}`, import.meta.url), { recursive: true, force: true }),
  ),
);
