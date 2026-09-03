import assert from "node:assert/strict";
import { rm } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import type { IndexRunSummary } from "../src/shared/contracts.js";
import { IndexDatabase } from "../src/core/database.js";

function run(runId: string, startedAt: string, phase: IndexRunSummary["phase"], finishedAt: string | null): IndexRunSummary {
  return {
    runId,
    phase,
    startedAt,
    finishedAt,
    discovered: 1,
    indexed: phase === "complete" ? 1 : 0,
    unchanged: 0,
    skipped: 0,
    failed: 0,
    deleted: 0,
    currentPath: null,
    error: null,
  };
}

test("较旧的中断 run 不会覆盖更新的已完成状态", async () => {
  const root = path.resolve("tests/.tmp/index-status");
  await rm(root, { recursive: true, force: true });
  const database = new IndexDatabase(path.join(root, "index.sqlite"));
  try {
    const orphan = run("orphan", "2026-08-31T01:00:00.000Z", "extract", null);
    database.startRun(orphan);
    const complete = run("complete", "2026-08-31T02:00:00.000Z", "complete", "2026-08-31T02:01:00.000Z");
    database.startRun(complete);
    const status = database.status(path.join(root, "config.json"));
    assert.equal(status.activeRun, null);
    assert.equal(status.lastRun?.runId, "complete");
  } finally {
    database.close();
  }
});
