# Local read-only patch

This directory is a source copy of `github.com/nkiri/xls` v0.0.4 under its
original MIT license.

The project carries a read-only compatibility and hardening patch:

- BIFF formula token streams are decoded and exposed through `Cell.Formula()`.
- Workbook defined names (including `_xlfn.XLOOKUP` / `_xlfn.TEXTJOIN`) and
  shared-formula `PtgExp` records are resolved to the stored expression.
- Formula token streams are never evaluated when the workbook has no cached
  result. `Cell.Value()` remains empty in that case.
- CFB sector/FAT/DIFAT/miniFAT traversal, SST allocation, and BIFF version
  parsing are bounded and fail closed on malformed input.
- Only BIFF8 workbook/sheet streams are accepted; older codepage-dependent
  BIFF variants are explicitly unsupported.

This preserves formula evidence for search while guaranteeing that indexing
does not execute or recalculate workbook formulas.
