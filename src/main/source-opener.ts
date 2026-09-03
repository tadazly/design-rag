import { execFile } from "node:child_process";
import path from "node:path";
import { promisify } from "node:util";
import { pathToFileURL } from "node:url";
import { shell } from "electron";
import type { OpenCitationResult } from "../shared/contracts.js";
import { parsePdfPage, parseSpreadsheetLocation } from "../shared/source-location.js";
import { parseRegistryString, usesMicrosoftExcel } from "./source-open-policy.js";

const execFileAsync = promisify(execFile);
const SPREADSHEET_EXTENSIONS = new Set([".xlsx", ".xlsm", ".xls"]);
const openRequests = new Map<string, Promise<OpenCitationResult>>();
const associationRequests = new Map<string, Promise<string | null>>();

const EXCEL_LOCATION_SCRIPT = `
$ErrorActionPreference = 'Stop'
$sourcePath = $env:DESIGN_RAG_SOURCE_PATH
$locator = $env:DESIGN_RAG_SOURCE_LOCATOR
$separator = $locator.LastIndexOf('!')
if ($separator -le 0) { throw '无法解析工作表定位' }
$sheetName = $locator.Substring(0, $separator).Trim()
$rangeAddress = $locator.Substring($separator + 1).Trim()
try { $excel = [Runtime.InteropServices.Marshal]::GetActiveObject('Excel.Application') }
catch { $excel = New-Object -ComObject Excel.Application }
$excel.Visible = $true
$workbook = $null
foreach ($candidate in $excel.Workbooks) {
  if ([String]::Equals($candidate.FullName, $sourcePath, [StringComparison]::OrdinalIgnoreCase)) { $workbook = $candidate; break }
}
if ($null -eq $workbook) { $workbook = $excel.Workbooks.Open($sourcePath, 0, $true) }
$worksheet = $workbook.Worksheets.Item($sheetName)
$worksheet.Activate()
$target = $worksheet.Range($rangeAddress)
$target.Select()
$excel.Goto($target, $true)
`;

async function openDefault(absolutePath: string, locator: string, note: string | null = null): Promise<OpenCitationResult> {
  const error = await shell.openPath(absolutePath);
  if (error) throw new Error(error);
  return { opened: true, method: "default-app", absolutePath, locator, note };
}

async function queryRegistryValue(registryPath: string, valueName: string | null): Promise<string | null> {
  const reg = path.join(process.env.SystemRoot ?? "C:\\Windows", "System32", "reg.exe");
  try {
    const { stdout } = await execFileAsync(reg, ["query", registryPath, valueName ? "/v" : "/ve", ...(valueName ? [valueName] : [])], {
      windowsHide: true,
      timeout: 2_000,
      maxBuffer: 64 * 1024,
    });
    return parseRegistryString(stdout);
  } catch {
    return null;
  }
}

function defaultApplicationProgId(extension: string): Promise<string | null> {
  const existing = associationRequests.get(extension);
  if (existing) return existing;
  const request = (async () => {
    const userChoice = await queryRegistryValue(
      `HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Explorer\\FileExts\\${extension}\\UserChoice`,
      "ProgId",
    );
    return userChoice ?? queryRegistryValue(`HKCR\\${extension}`, null);
  })();
  associationRequests.set(extension, request);
  return request;
}

async function performOpen(absolutePath: string, locator: string): Promise<OpenCitationResult> {
  const extension = path.extname(absolutePath).toLowerCase();
  const spreadsheetLocation = SPREADSHEET_EXTENSIONS.has(extension) ? parseSpreadsheetLocation(locator) : null;
  if (spreadsheetLocation && process.platform === "win32" && usesMicrosoftExcel(await defaultApplicationProgId(extension))) {
    const powershell = path.join(process.env.SystemRoot ?? "C:\\Windows", "System32", "WindowsPowerShell", "v1.0", "powershell.exe");
    try {
      await execFileAsync(powershell, ["-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", EXCEL_LOCATION_SCRIPT], {
        windowsHide: true,
        timeout: 15_000,
        maxBuffer: 256 * 1024,
        env: {
          ...process.env,
          DESIGN_RAG_SOURCE_PATH: absolutePath,
          DESIGN_RAG_SOURCE_LOCATOR: `${spreadsheetLocation.sheetName}!${spreadsheetLocation.range}`,
        },
      });
      return { opened: true, method: "excel-range", absolutePath, locator, note: null };
    } catch {
      return openDefault(absolutePath, locator, "未能自动定位单元格，已改为打开源文件");
    }
  }

  const page = extension === ".pdf" ? parsePdfPage(locator) : null;
  if (page) {
    try {
      await shell.openExternal(`${pathToFileURL(absolutePath).href}#page=${page}`, { activate: true });
      return { opened: true, method: "pdf-page", absolutePath, locator, note: null };
    } catch {
      return openDefault(absolutePath, locator, "默认 PDF 阅读器不支持页码定位，已改为打开源文件");
    }
  }

  return openDefault(absolutePath, locator);
}

export function openSourceLocation(absolutePath: string, locator: string): Promise<OpenCitationResult> {
  const key = `${absolutePath}\u0000${locator}`;
  const existing = openRequests.get(key);
  if (existing) return existing;
  const request = performOpen(absolutePath, locator).finally(() => openRequests.delete(key));
  openRequests.set(key, request);
  return request;
}
