import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { createHash } from "node:crypto";
import { readFile, readdir, writeFile } from "node:fs/promises";
import path from "node:path";
import process from "node:process";
import { promisify } from "node:util";
import { DatabaseSync } from "node:sqlite";
import { pathToFileURL } from "node:url";
import { SOURCE_INVENTORY_ALGORITHM, sameSourceInventory } from "./full-corpus-evidence.mjs";

const execFileAsync = promisify(execFile);

function flag(name) {
  return process.argv.find((value) => value.startsWith(`--${name}=`))?.slice(name.length + 3) ?? null;
}

function requiredFlag(name) {
  const value = flag(name)?.trim();
  if (!value) throw new Error(`缺少 --${name}=...`);
  return path.resolve(value);
}

function pathKey(value) {
  return path.resolve(value).normalize("NFC").replaceAll("\\", "/").toLowerCase();
}

export const queryCases = [
  {
    id: "latest-888",
    query: "找到最新的一个 888活动，说明一下里面的玩法和产出逻辑",
    required: [/888/i],
    top1: /888/i,
    top1DocumentIdentity: true,
  },
  {
    id: "gacha-tables",
    query: "我要新增一个扭蛋机，需要配置哪些表格",
    required: [/newLottery/i, /newPrizePool/i],
  },
  {
    id: "reuse-demon-888",
    query: "我要复用妖王888，需要配哪些表，帮我把新的配置列出来",
    required: [/(妖王.*888|888.*妖王)/i],
    requiredDocumentIdentity: [/(妖王.*888|888.*妖王)/i],
  },
  {
    id: "wheel-reuse",
    query: "我想复用轮盘抽奖活动，有哪些可以复用",
    required: [/(轮盘|转盘)/i],
  },
  {
    id: "ring-tide-888-output",
    query: "环潮龙888 产出逻辑",
    required: [/环潮龙.*888/i],
    top1: /环潮龙.*888/i,
    top1DocumentIdentity: true,
  },
  {
    id: "explicit-table-ids",
    query: "newLottery newPrizePool 配置",
    required: [/newLottery/i, /newPrizePool/i],
  },
];

function searchableHit(hit) {
  return `${hit.title}\n${hit.relativePath}\n${hit.excerpts.map((excerpt) => `${excerpt.locator}\n${excerpt.text}`).join("\n")}`;
}

function top1Matches(testCase, hit) {
  if (!testCase.top1 || !hit) return false;
  const value = testCase.top1DocumentIdentity
    ? `${hit.title}\n${hit.relativePath}`
    : searchableHit(hit);
  return testCase.top1.test(value);
}

function documentIdentityText(hit) {
  return `${hit?.title ?? ""}\n${hit?.relativePath ?? ""}`;
}

const hiddenToolDirectories = new Set([
  ".git", ".svn", ".hg", ".cursor", ".codex", ".agents", ".claude", ".windsurf", ".continue", ".aider", ".cline", ".roo",
  ".gemini", ".openai", ".github", ".gitlab", ".vscode", ".idea", ".vs", ".devcontainer", ".obsidian",
  ".aws", ".azure", ".gcloud", ".kube", ".ssh", ".gnupg", ".docker",
]);

const sensitiveFilePatterns = [
  /^\.env(?:\..+)?$/i,
  /^\.(?:npmrc|pypirc|netrc)$/i,
  /^\.(?:credentials?|secrets?|tokens?)\.(?:json|ya?ml)$/i,
  /^(?:credentials?|secrets?|tokens?)\.(?:json|ya?ml)$/i,
  /^(?:client[_-]?secret|service[_-]?account).*\.json$/i,
  /^application_default_credentials\.json$/i,
  /^(?:config|settings)\.(?:local|private|secret)(?:\..+)?$/i,
  /^local\.settings(?:\..+)?$/i,
  /^id_(?:rsa|dsa|ecdsa|ed25519)$/i,
  /^authorized_keys$/i,
  /^(?:private|server|client)\.(?:key|pem)$/i,
];

export function isHiddenToolOrSensitivePath(value) {
  const normalized = String(value).normalize("NFC").replaceAll("\\", "/");
  const segments = normalized.split("/").filter(Boolean);
  if (segments.some((segment) => hiddenToolDirectories.has(segment.toLowerCase()))) return true;
  const fileName = segments.at(-1) ?? "";
  return sensitiveFilePatterns.some((pattern) => pattern.test(fileName));
}

async function resolveActiveIndexPath(dataDir) {
  const pointerPath = path.join(dataDir, "index.active.json");
  try {
    const pointer = JSON.parse(await readFile(pointerPath, "utf8"));
    if (pointer?.schemaVersion !== 1 || typeof pointer.fileName !== "string"
      || path.basename(pointer.fileName) !== pointer.fileName
      || !/^index(?:\.[a-z0-9-]+)?\.sqlite$/i.test(pointer.fileName)) {
      throw new Error(`索引指针无效：${pointerPath}`);
    }
    return path.join(dataDir, pointer.fileName);
  } catch (error) {
    if (error?.code === "ENOENT") return path.join(dataDir, "index.sqlite");
    throw error;
  }
}

function hashRows(rows, fields) {
  const hash = createHash("sha256");
  let count = 0;
  for (const row of rows) {
    for (const field of fields) {
      hash.update(field);
      hash.update("\0");
      hash.update(String(row[field] ?? "").normalize("NFC"));
      hash.update("\0");
    }
    hash.update("\n");
    count++;
  }
  return { count, sha256: hash.digest("hex") };
}

function loadIndexSnapshot(databasePath) {
  const database = new DatabaseSync(databasePath, { readOnly: true, timeout: 30_000 });
  try {
    const documents = database.prepare(`
      SELECT absolute_path, relative_path, source_id, source_kind, title, family_key,
             effective_updated_at, effective_updated_at_ms, date_source, content_hash,
             source_identity, stale, deleted, chunk_count
      FROM documents WHERE deleted=0
      ORDER BY absolute_path
    `).all();
    const documentProjection = hashRows(documents, [
      "absolute_path", "relative_path", "source_id", "source_kind", "title", "family_key",
      "effective_updated_at_ms", "date_source", "content_hash", "source_identity", "stale", "chunk_count",
    ]);
    const chunkProjection = hashRows(database.prepare(`
      SELECT d.absolute_path, c.ordinal, c.section_type, c.heading_path_json, c.locator, c.content_hash, c.text
      FROM chunks c JOIN documents d ON d.id=c.document_id
      WHERE d.deleted=0
      ORDER BY d.absolute_path,c.ordinal,c.id
    `).iterate(), ["absolute_path", "ordinal", "section_type", "heading_path_json", "locator", "content_hash", "text"]);
    return { documents, documentProjection, chunkProjection };
  } finally {
    database.close();
  }
}

async function sha256File(filePath) {
  return createHash("sha256").update(await readFile(filePath)).digest("hex");
}

async function nodeBundleProvenance() {
  const coreRoot = path.resolve("dist/core");
  const names = (await readdir(coreRoot)).filter((name) => name.endsWith(".js")).sort();
  const hash = createHash("sha256");
  for (const name of names) {
    hash.update(name);
    hash.update("\0");
    hash.update(await readFile(path.join(coreRoot, name)));
    hash.update("\n");
  }
  return { root: coreRoot, fileCount: names.length, sha256: hash.digest("hex") };
}

async function gitProvenance() {
  const [{ stdout: head }, { stdout: status }] = await Promise.all([
    execFileAsync("git", ["rev-parse", "HEAD"], { windowsHide: true }),
    execFileAsync("git", ["status", "--porcelain=v1"], { windowsHide: true, maxBuffer: 16 * 1024 * 1024 }),
  ]);
  return { head: head.trim(), dirty: status.trim().length > 0, changedPathCount: status.split(/\r?\n/).filter(Boolean).length };
}

async function evaluate(root, label) {
  const configDir = path.join(root, "config");
  const dataDir = path.join(root, "data");
  const environment = {
    ...process.env,
    DESIGN_RAG_CONFIG_DIR: configDir,
    DESIGN_RAG_DATA_DIR: dataDir,
    DESIGN_RAG_CONFIG_DIR: configDir,
    DESIGN_RAG_DATA_DIR: dataDir,
  };
  let service = null;
  let goCli = null;
  let engineProvenance;
  if (label === "node") {
    const { KnowledgeBaseService } = await import("../dist/core/service.js");
    service = await KnowledgeBaseService.create({ configDir, dataDir, readOnly: true });
    engineProvenance = await nodeBundleProvenance();
  } else {
    const configured = flag("go-cli")?.trim() || process.env.DRAG_GO_CLI?.trim();
    if (!configured) throw new Error("Go 搜索 A/B 必须显式提供 --go-cli=<纯 Go drag binary>，禁止复用 TypeScript SearchEngine 冒充 Go 结果");
    goCli = path.resolve(configured);
    engineProvenance = { executable: goCli, sha256: await sha256File(goCli) };
  }
  const goCall = async (args) => {
    const result = await execFileAsync(goCli, args, {
      cwd: path.dirname(goCli),
      env: environment,
      windowsHide: true,
      maxBuffer: 64 * 1024 * 1024,
    });
    return JSON.parse(result.stdout);
  };
  if (label === "go") {
    engineProvenance.version = await goCall(["--version", "--json"]);
  }
  const results = [];
  try {
    for (const testCase of queryCases) {
      const bundle = label === "node"
        ? await service.retrieve({
          query: testCase.query,
          sort: "newest",
          maxDocuments: 8,
          maxChunksPerDocument: 3,
          maxChars: 24_000,
        })
        : await goCall(["retrieve", testCase.query, "--sort", "newest", "--max-documents", "8", "--max-chunks-per-document", "3", "--max-chars", "24000", "--json"]);
      const top8 = bundle.search.hits.slice(0, 8);
      const citationChecks = await Promise.all(bundle.evidence.map(async (evidence) => {
        const read = label === "node"
          ? service.readCitation(evidence.citationId, bundle.indexRevision)
          : await goCall(["citation", evidence.citationId, "--revision", String(bundle.indexRevision), "--json"]);
        return {
          citationId: evidence.citationId,
          readable: !read.changed && read.content.length > 0,
          roundTripExact: read.content === evidence.content
            && read.citation?.locator === evidence.locator
            && read.citation?.citationId === evidence.citationId,
          locator: evidence.locator,
          sourceLink: evidence.sourceLink.markdown,
        };
      }));
      const requiredRecall = testCase.required.map((pattern) => ({
        pattern: pattern.source,
        matched: top8.some((hit) => pattern.test(searchableHit(hit)))
          || bundle.evidence.some((item) => pattern.test(`${item.title}\n${item.absolutePath}\n${item.content}`)),
      }));
      const requiredDocumentIdentityRecall = (testCase.requiredDocumentIdentity ?? []).map((pattern) => ({
        pattern: pattern.source,
        matched: top8.some((hit) => pattern.test(documentIdentityText(hit))),
      }));
      results.push({
        id: testCase.id,
        query: testCase.query,
        tookMs: bundle.search.tookMs,
        top1: top8[0]?.title ?? null,
        top1Passed: testCase.top1 ? top1Matches(testCase, top8[0]) : null,
        top8: top8.map((hit) => ({
          title: hit.title,
          relativePath: hit.relativePath,
          sourceKind: hit.sourceKind,
          effectiveUpdatedAt: hit.effectiveUpdatedAt,
          dateSource: hit.dateSource,
          locators: hit.excerpts.map((excerpt) => excerpt.locator),
          chunkIds: hit.excerpts.map((excerpt) => excerpt.chunkId),
        })),
        evidence: bundle.evidence.map((item) => ({
          citationId: item.citationId,
          title: item.title,
          absolutePath: item.absolutePath,
          locator: item.locator,
          sourceLink: item.sourceLink.markdown,
          contentPreview: item.content.slice(0, 500),
          contentSha256: createHash("sha256").update(item.content).digest("hex"),
        })),
        requiredRecall,
        requiredRecallAt8: requiredRecall.every((item) => item.matched),
        requiredDocumentIdentityRecall,
        requiredDocumentIdentityRecallAt8: requiredDocumentIdentityRecall.every((item) => item.matched),
        citationsReadable: citationChecks.length > 0 && citationChecks.every((item) => item.readable && item.roundTripExact),
        citationChecks,
      });
    }
  } finally {
    service?.close();
  }
  const databasePath = await resolveActiveIndexPath(dataDir);
  const indexSnapshot = loadIndexSnapshot(databasePath);
  const documents = indexSnapshot.documents;
  const hiddenToolDocuments = documents.filter((row) => isHiddenToolOrSensitivePath(row.relative_path) || isHiddenToolOrSensitivePath(row.absolute_path));
  const hiddenToolEvidence = results.flatMap((result) => result.evidence
    .filter((evidence) => isHiddenToolOrSensitivePath(evidence.absolutePath))
    .map((evidence) => ({ queryId: result.id, absolutePath: evidence.absolutePath, locator: evidence.locator })));
  return {
    label,
    engine: label === "node" ? "typescript-search" : "go-search-cli",
    executable: goCli,
    databasePath,
    engineProvenance,
    root,
    documentCount: documents.length,
    chunkCount: indexSnapshot.chunkProjection.count,
    documentProjectionSha256: indexSnapshot.documentProjection.sha256,
    chunkProjectionSha256: indexSnapshot.chunkProjection.sha256,
    hiddenToolDocuments,
    hiddenToolEvidence,
    documents,
    results,
  };
}

function inventoryDiagnostic(value, label) {
  const errors = [];
  if (!value || typeof value !== "object") errors.push(`${label}.sourceInventory 缺失`);
  const inventory = value && typeof value === "object" ? value : {};
  if (inventory.algorithm !== SOURCE_INVENTORY_ALGORITHM) errors.push(`${label}.sourceInventory.algorithm 不受支持`);
  if (!/^[0-9a-f]{64}$/i.test(String(inventory.fingerprint ?? ""))) errors.push(`${label}.sourceInventory.fingerprint 非 SHA-256`);
  if (!Number.isInteger(inventory.fileCount) || inventory.fileCount < 0) errors.push(`${label}.sourceInventory.fileCount 非非负整数`);
  if (!Number.isInteger(inventory.sourceCount) || inventory.sourceCount < 1) errors.push(`${label}.sourceInventory.sourceCount 非正整数`);
  if (!Number.isFinite(inventory.totalBytes) || inventory.totalBytes < 0) errors.push(`${label}.sourceInventory.totalBytes 非非负数字`);
  if (!Number.isInteger(inventory.discoveredFileCount) || inventory.discoveredFileCount < 0) errors.push(`${label}.sourceInventory.discoveredFileCount 非非负整数`);
  if (inventory.matchesDiscoveredCandidates !== true) errors.push(`${label}.sourceInventory 未与实际 discovered candidates 对齐`);
  if (inventory.stableDuringRun !== true) errors.push(`${label}.sourceInventory 未证明 full-corpus 期间保持不变`);
  return {
    label,
    valid: errors.length === 0,
    errors,
    algorithm: inventory.algorithm ?? null,
    fingerprint: inventory.fingerprint ?? null,
    fileCount: inventory.fileCount ?? null,
    sourceCount: inventory.sourceCount ?? null,
    totalBytes: inventory.totalBytes ?? null,
    discoveredFileCount: inventory.discoveredFileCount ?? null,
    matchesDiscoveredCandidates: inventory.matchesDiscoveredCandidates ?? null,
    capturedAt: inventory.capturedAt ?? null,
    stableDuringRun: inventory.stableDuringRun ?? null,
  };
}

export function compareAcceptanceInventories(nodeInventory, goInventory) {
  const node = inventoryDiagnostic(nodeInventory, "node");
  const go = inventoryDiagnostic(goInventory, "go");
  const matches = node.valid && go.valid && sameSourceInventory(node, go);
  return {
    matches,
    validationErrors: [...node.errors, ...go.errors],
    node,
    go,
  };
}

export function buildQueryCaseGates(nodeResults, goResults) {
  return queryCases.map((testCase) => {
    const node = nodeResults.find((item) => item.id === testCase.id);
    const go = goResults.find((item) => item.id === testCase.id);
    const presentInBoth = Boolean(node && go);
    const top1Required = Boolean(testCase.top1);
    const nodeTop1Passed = !top1Required || node?.top1Passed === true;
    const goTop1Passed = !top1Required || go?.top1Passed === true;
    const top1Parity = !top1Required || (Boolean(node?.top1) && node?.top1 === go?.top1);
    const nodeRequiredRecallAt8 = node?.requiredRecallAt8 === true;
    const goRequiredRecallAt8 = go?.requiredRecallAt8 === true;
    const identityRecallRequired = (testCase.requiredDocumentIdentity?.length ?? 0) > 0;
    const nodeRequiredDocumentIdentityRecallAt8 = !identityRecallRequired || node?.requiredDocumentIdentityRecallAt8 === true;
    const goRequiredDocumentIdentityRecallAt8 = !identityRecallRequired || go?.requiredDocumentIdentityRecallAt8 === true;
    const nodeCitationsReadable = node?.citationsReadable === true;
    const goCitationsReadable = go?.citationsReadable === true;
    const topNProjectionParity = JSON.stringify(node?.top8 ?? null) === JSON.stringify(go?.top8 ?? null);
    const evidenceProjectionParity = JSON.stringify(node?.evidence ?? null) === JSON.stringify(go?.evidence ?? null);
    return {
      id: testCase.id,
      presentInBoth,
      top1Required,
      nodeTop1: node?.top1 ?? null,
      goTop1: go?.top1 ?? null,
      nodeTop1Passed,
      goTop1Passed,
      top1Parity,
      nodeRequiredRecallAt8,
      goRequiredRecallAt8,
      identityRecallRequired,
      nodeRequiredDocumentIdentityRecallAt8,
      goRequiredDocumentIdentityRecallAt8,
      nodeCitationsReadable,
      goCitationsReadable,
      topNProjectionParity,
      evidenceProjectionParity,
      passed: presentInBoth
        && nodeTop1Passed
        && goTop1Passed
        && top1Parity
        && nodeRequiredRecallAt8
        && goRequiredRecallAt8
        && nodeRequiredDocumentIdentityRecallAt8
        && goRequiredDocumentIdentityRecallAt8
        && nodeCitationsReadable
        && goCitationsReadable,
    };
  });
}

export function buildComparisonGates({
  sourceInventoryComparison,
  effectiveDateDiffs,
  queryCaseGates,
  nodeResult,
  goResult,
}) {
  const top1Cases = queryCaseGates.filter((item) => item.top1Required);
  return {
    independentSearchEngines: nodeResult.engine === "typescript-search" && goResult.engine === "go-search-cli",
    sameSourceInventory: sourceInventoryComparison.matches,
    documentCountParity: nodeResult.documentCount === goResult.documentCount,
    noNodeOnlyDocuments: nodeResult.nodeOnlyCount === 0,
    noGoOnlyDocuments: goResult.goOnlyCount === 0,
    chunkCountParity: nodeResult.chunkCount === goResult.chunkCount,
    documentProjectionParity: nodeResult.documentProjectionSha256 === goResult.documentProjectionSha256,
    chunkProjectionParity: nodeResult.chunkProjectionSha256 === goResult.chunkProjectionSha256,
    effectiveDateDiffsZero: effectiveDateDiffs.length === 0,
    allSixQueryCasesPresent: queryCaseGates.length === 6 && queryCaseGates.every((item) => item.presentInBoth),
    latestTop1Passed: top1Cases.every((item) => item.nodeTop1Passed && item.goTop1Passed),
    latestTop1Parity: top1Cases.every((item) => item.top1Parity),
    requiredDocumentRecallAt8: queryCaseGates.every((item) => item.nodeRequiredRecallAt8 && item.goRequiredRecallAt8),
    requiredDocumentIdentityRecallAt8: queryCaseGates.every((item) =>
      item.nodeRequiredDocumentIdentityRecallAt8 && item.goRequiredDocumentIdentityRecallAt8),
    citationsReadable: queryCaseGates.every((item) => item.nodeCitationsReadable && item.goCitationsReadable),
    allSixQueryCasesPassed: queryCaseGates.length === 6 && queryCaseGates.every((item) => item.passed),
    hiddenToolEvidenceZero: [nodeResult, goResult].every((result) =>
      result.hiddenToolDocuments.length === 0 && result.hiddenToolEvidence.length === 0),
  };
}

async function readAcceptanceReport(root, label) {
  const reportPath = path.join(root, "acceptance-report.json");
  let report;
  try {
    report = JSON.parse(await readFile(reportPath, "utf8"));
  } catch (error) {
    throw new Error(`${label} acceptance report 不可读：${reportPath}`, { cause: error });
  }
  return { reportPath, report };
}

async function selfTest() {
  assert.equal(queryCases.length, 6);
  assert.equal(new Set(queryCases.map((item) => item.id)).size, 6);
  const latestCase = queryCases.find((item) => item.id === "latest-888");
  assert(latestCase);
  assert.equal(top1Matches(latestCase, {
    title: "rule",
    relativePath: "tables/rule.xlsx",
    excerpts: [{ locator: "rule!A888", text: "正文包含 888", headingPath: [] }],
  }), false, "latest top1 不得由 excerpt/body 中的 anchor 通过");
  assert.equal(top1Matches(latestCase, {
    title: "环潮龙888活动_20260722",
    relativePath: "2026/环潮龙888活动_20260722.xlsx",
    excerpts: [],
  }), true, "latest top1 的 title/path anchor 应通过");
  const ringCase = queryCases.find((item) => item.id === "ring-tide-888-output");
  assert(ringCase);
  assert.equal(top1Matches(ringCase, {
    title: "errorCode",
    relativePath: "tables/errorCode.xlsx",
    excerpts: [{ locator: "errorCode!A1:G24", text: "正文包含环潮龙888", headingPath: [] }],
  }), false, "ring-tide top1 不得由 excerpt/body 冒充活动身份");
  assert.equal(top1Matches(ringCase, {
    title: "环潮龙888活动_20260722",
    relativePath: "2026/环潮龙888活动_20260722.xlsx",
    excerpts: [],
  }), true, "ring-tide top1 的 title/path 身份应通过");
  const demonCase = queryCases.find((item) => item.id === "reuse-demon-888");
  assert(demonCase?.requiredDocumentIdentity?.[0]);
  assert.equal(demonCase.requiredDocumentIdentity[0].test(documentIdentityText({
    title: "statistic",
    relativePath: "tables/statistic.xlsx",
  })), false, "demon required identity 不得由 statistic 正文替代");
  assert.equal(demonCase.requiredDocumentIdentity[0].test(documentIdentityText({
    title: "万妖王·摩哥斯888活动_20260506",
    relativePath: "2026/万妖王·摩哥斯888活动_20260506.docx",
  })), true);
  const result = (label) => ({
    label,
    engine: label === "node" ? "typescript-search" : "go-search-cli",
    hiddenToolDocuments: [],
    hiddenToolEvidence: [],
    documentCount: 12,
    chunkCount: 24,
    nodeOnlyCount: 0,
    goOnlyCount: 0,
    documentProjectionSha256: "c".repeat(64),
    chunkProjectionSha256: "d".repeat(64),
    results: queryCases.map((testCase) => ({
      id: testCase.id,
      top1: `top:${testCase.id}`,
      top1Passed: testCase.top1 ? true : null,
      requiredRecallAt8: true,
      requiredDocumentIdentityRecallAt8: true,
      citationsReadable: true,
    })),
  });
  const inventory = {
    algorithm: SOURCE_INVENTORY_ALGORITHM,
    fingerprint: "a".repeat(64),
    fileCount: 12,
    sourceCount: 2,
    totalBytes: 345,
    discoveredFileCount: 12,
    matchesDiscoveredCandidates: true,
    stableDuringRun: true,
  };
  const sourceInventoryComparison = compareAcceptanceInventories(inventory, { ...inventory });
  assert.equal(sourceInventoryComparison.matches, true);
  assert.equal(compareAcceptanceInventories(inventory, { ...inventory, fingerprint: "b".repeat(64) }).matches, false);
  assert.equal(compareAcceptanceInventories(inventory, { ...inventory, fileCount: null }).matches, false);
  assert.equal(compareAcceptanceInventories(inventory, { ...inventory, matchesDiscoveredCandidates: false }).matches, false);
  assert.equal(compareAcceptanceInventories(inventory, { ...inventory, stableDuringRun: null }).matches, false);
  assert.equal(compareAcceptanceInventories(undefined, inventory).matches, false);

  const nodeResult = result("node");
  const goResult = result("go");
  const passingCases = buildQueryCaseGates(nodeResult.results, goResult.results);
  assert.equal(passingCases.length, 6);
  assert.equal(passingCases.every((item) => item.passed), true);
  assert.equal(Object.values(buildComparisonGates({
    sourceInventoryComparison,
    effectiveDateDiffs: [],
    queryCaseGates: passingCases,
    nodeResult,
    goResult,
  })).every(Boolean), true);

  const missingDocument = {
    ...goResult,
    documentCount: 11,
    goOnlyCount: 1,
    documentProjectionSha256: "e".repeat(64),
  };
  const missingDocumentGates = buildComparisonGates({
    sourceInventoryComparison,
    effectiveDateDiffs: [],
    queryCaseGates: passingCases,
    nodeResult,
    goResult: missingDocument,
  });
  assert.equal(missingDocumentGates.documentCountParity, false);
  assert.equal(missingDocumentGates.noGoOnlyDocuments, false);
  assert.equal(missingDocumentGates.documentProjectionParity, false);

  for (const testCase of queryCases) {
    const failingNode = result("node");
    const target = failingNode.results.find((item) => item.id === testCase.id);
    target.requiredRecallAt8 = false;
    const cases = buildQueryCaseGates(failingNode.results, goResult.results);
    assert.equal(cases.find((item) => item.id === testCase.id)?.passed, false, `${testCase.id} 必须独立失败`);
    assert.equal(buildComparisonGates({
      sourceInventoryComparison,
      effectiveDateDiffs: [],
      queryCaseGates: cases,
      nodeResult: failingNode,
      goResult,
    }).allSixQueryCasesPassed, false);
  }

  const identityFailedNode = result("node");
  identityFailedNode.results.find((item) => item.id === "reuse-demon-888").requiredDocumentIdentityRecallAt8 = false;
  const identityCases = buildQueryCaseGates(identityFailedNode.results, goResult.results);
  assert.equal(identityCases.find((item) => item.id === "reuse-demon-888")?.passed, false);
  assert.equal(buildComparisonGates({
    sourceInventoryComparison,
    effectiveDateDiffs: [],
    queryCaseGates: identityCases,
    nodeResult: identityFailedNode,
    goResult,
  }).requiredDocumentIdentityRecallAt8, false);

  const missingNode = result("node");
  missingNode.results = missingNode.results.filter((item) => item.id !== queryCases[0].id);
  assert.equal(buildComparisonGates({
    sourceInventoryComparison,
    effectiveDateDiffs: [],
    queryCaseGates: buildQueryCaseGates(missingNode.results, goResult.results),
    nodeResult: missingNode,
    goResult,
  }).allSixQueryCasesPresent, false);

  const top1FailedNode = result("node");
  top1FailedNode.results.find((item) => item.id === "latest-888").top1Passed = false;
  assert.equal(buildComparisonGates({
    sourceInventoryComparison,
    effectiveDateDiffs: [],
    queryCaseGates: buildQueryCaseGates(top1FailedNode.results, goResult.results),
    nodeResult: top1FailedNode,
    goResult,
  }).latestTop1Passed, false);

  const parityGo = result("go");
  parityGo.results.find((item) => item.id === "latest-888").top1 = "different-top1";
  assert.equal(buildComparisonGates({
    sourceInventoryComparison,
    effectiveDateDiffs: [],
    queryCaseGates: buildQueryCaseGates(nodeResult.results, parityGo.results),
    nodeResult,
    goResult: parityGo,
  }).latestTop1Parity, false);

  const citationFailedNode = result("node");
  citationFailedNode.results.find((item) => item.id === "gacha-tables").citationsReadable = false;
  assert.equal(buildComparisonGates({
    sourceInventoryComparison,
    effectiveDateDiffs: [],
    queryCaseGates: buildQueryCaseGates(citationFailedNode.results, goResult.results),
    nodeResult: citationFailedNode,
    goResult,
  }).citationsReadable, false);

  assert.equal(buildComparisonGates({
    sourceInventoryComparison,
    effectiveDateDiffs: [{
      absolutePath: "fixture.md",
      node: { effectiveUpdatedAt: "2026-01-01T00:00:00.000Z", dateSource: "version_log" },
      go: { effectiveUpdatedAt: "2025-12-01T00:00:00.000Z", dateSource: "embedded_modified" },
      involvesPathOrFilenameDateSource: false,
    }],
    queryCaseGates: passingCases,
    nodeResult,
    goResult,
  }).effectiveDateDiffsZero, false, "version_log/embedded_modified 差异也必须 fail closed");

  assert.equal(isHiddenToolOrSensitivePath("策划/.codex/skills/secret.md"), true);
  assert.equal(isHiddenToolOrSensitivePath("策划/.agents/rules.md"), true);
  assert.equal(isHiddenToolOrSensitivePath("策划/.git/config"), true);
  assert.equal(isHiddenToolOrSensitivePath("策划/config.local.json"), true);
  assert.equal(isHiddenToolOrSensitivePath("策划/.draft/正常隐藏业务.md"), false);
  const hiddenNode = result("node");
  hiddenNode.hiddenToolEvidence.push({ queryId: "latest-888", absolutePath: "策划/.cursor/rules.md" });
  assert.equal(buildComparisonGates({
    sourceInventoryComparison,
    effectiveDateDiffs: [],
    queryCaseGates: buildQueryCaseGates(hiddenNode.results, goResult.results),
    nodeResult: hiddenNode,
    goResult,
  }).hiddenToolEvidenceZero, false);
  process.stdout.write(`${JSON.stringify({ status: "PASS", queryCaseCount: queryCases.length, effectiveDateGate: "all-differences-fail-closed", latestTop1IdentityGate: "title+relativePath", requiredIdentityRecallGate: "title+relativePath", hiddenEvidenceGate: "fail-closed", sourceInventoryAlgorithm: SOURCE_INVENTORY_ALGORITHM })}\n`);
}

async function main() {
  if (process.argv.includes("--self-test")) {
    await selfTest();
    return;
  }
  const nodeRoot = requiredFlag("node-root");
  const goRoot = requiredFlag("go-root");
  const outputPath = path.resolve(flag("output") ?? path.join(goRoot, "node-go-evidence-ab.json"));
  const [nodeAcceptance, goAcceptance] = await Promise.all([
    readAcceptanceReport(nodeRoot, "Node"),
    readAcceptanceReport(goRoot, "Go"),
  ]);
  const sourceInventoryComparison = compareAcceptanceInventories(
    nodeAcceptance.report.sourceInventory,
    goAcceptance.report.sourceInventory,
  );
  if (!sourceInventoryComparison.matches) {
    const rejection = {
      schema: "drag_node_go_evidence_ab_v5",
      createdAt: new Date().toISOString(),
      status: "FAIL",
      reason: "source_inventory_mismatch",
      inputs: {
        nodeRoot,
        goRoot,
        nodeAcceptanceReport: nodeAcceptance.reportPath,
        goAcceptanceReport: goAcceptance.reportPath,
      },
      gates: { sameSourceInventory: false },
      sourceInventory: sourceInventoryComparison,
    };
    await writeFile(outputPath, `${JSON.stringify(rejection, null, 2)}\n`, "utf8");
    throw new Error(`Node/Go 输入语料不一致，已拒绝执行查询 A/B：${JSON.stringify({ outputPath, sourceInventory: sourceInventoryComparison })}`);
  }

  const [nodeResult, goResult] = await Promise.all([
    evaluate(nodeRoot, "node"),
    evaluate(goRoot, "go"),
  ]);

  const nodeByPath = new Map(nodeResult.documents.map((row) => [pathKey(String(row.absolute_path)), row]));
  const goByPath = new Map(goResult.documents.map((row) => [pathKey(String(row.absolute_path)), row]));
  const commonPaths = [...nodeByPath.keys()].filter((key) => goByPath.has(key));
  const nodeOnly = [...nodeByPath.keys()].filter((key) => !goByPath.has(key)).map((key) => nodeByPath.get(key)?.absolute_path);
  const goOnly = [...goByPath.keys()].filter((key) => !nodeByPath.has(key)).map((key) => goByPath.get(key)?.absolute_path);
  nodeResult.nodeOnlyCount = nodeOnly.length;
  nodeResult.goOnlyCount = goOnly.length;
  goResult.nodeOnlyCount = nodeOnly.length;
  goResult.goOnlyCount = goOnly.length;
  const effectiveDateDiffs = commonPaths.flatMap((key) => {
    const node = nodeByPath.get(key);
    const go = goByPath.get(key);
    if (!node || !go || Number(node.effective_updated_at_ms) === Number(go.effective_updated_at_ms)) return [];
    return [{
      absolutePath: node.absolute_path,
      node: { effectiveUpdatedAt: node.effective_updated_at, dateSource: node.date_source },
      go: { effectiveUpdatedAt: go.effective_updated_at, dateSource: go.date_source },
      involvesPathOrFilenameDateSource: [node.date_source, go.date_source].some((source) => source === "path" || source === "filename"),
    }];
  });
  const pathOrFilenameDateDiffs = effectiveDateDiffs.filter((item) => item.involvesPathOrFilenameDateSource);
  const queryCaseGates = buildQueryCaseGates(nodeResult.results, goResult.results);
  const gates = buildComparisonGates({
    sourceInventoryComparison,
    effectiveDateDiffs,
    queryCaseGates,
    nodeResult,
    goResult,
  });
  const corpus = {
    sourceInventory: sourceInventoryComparison,
    nodeDocuments: nodeResult.documentCount,
    goDocuments: goResult.documentCount,
    commonDocuments: commonPaths.length,
    nodeOnly,
    goOnly,
    nodeChunks: nodeResult.chunkCount,
    goChunks: goResult.chunkCount,
    documentProjectionSha256: { node: nodeResult.documentProjectionSha256, go: goResult.documentProjectionSha256 },
    chunkProjectionSha256: { node: nodeResult.chunkProjectionSha256, go: goResult.chunkProjectionSha256 },
    effectiveDateDiffCount: effectiveDateDiffs.length,
    pathOrFilenameDateDiffCount: pathOrFilenameDateDiffs.length,
    effectiveDateDiffs: effectiveDateDiffs.slice(0, 1_000),
  };
  const report = {
    schema: "drag_node_go_evidence_ab_v5",
    createdAt: new Date().toISOString(),
    inputs: {
      nodeRoot,
      goRoot,
      nodeAcceptanceReport: nodeAcceptance.reportPath,
      goAcceptanceReport: goAcceptance.reportPath,
      goCli: goResult.executable,
      nodeDatabase: nodeResult.databasePath,
      goDatabase: goResult.databasePath,
      sharedPhysicalIndex: pathKey(nodeResult.databasePath) === pathKey(goResult.databasePath),
      git: await gitProvenance(),
    },
    gates,
    corpus,
    queryCaseGates,
    node: { ...nodeResult, documents: undefined },
    go: { ...goResult, documents: undefined },
  };
  await writeFile(outputPath, `${JSON.stringify(report, null, 2)}\n`, "utf8");
  if (!Object.values(gates).every(Boolean)) {
    throw new Error(`Node/Go evidence A/B 未通过：${JSON.stringify({ outputPath, gates, sourceInventory: sourceInventoryComparison })}`);
  }
  process.stdout.write(`${JSON.stringify({ outputPath, gates, corpus, queryCaseGates }, null, 2)}\n`);
}

const isMain = process.argv[1] && pathToFileURL(path.resolve(process.argv[1])).href === import.meta.url;
if (isMain) await main();
