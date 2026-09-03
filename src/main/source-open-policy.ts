export function parseRegistryString(output: string): string | null {
  for (const line of output.split(/\r?\n/)) {
    const match = /\sREG_(?:EXPAND_)?SZ\s+(.+?)\s*$/i.exec(line);
    if (match?.[1]) return match[1].trim();
  }
  return null;
}

export function usesMicrosoftExcel(progId: string | null): boolean {
  return Boolean(progId && /^Excel\./i.test(progId));
}
