# xls

A Go library for reading Microsoft Excel 97–2003 binary format (`.xls` / BIFF8) files.

[![Go Reference](https://pkg.go.dev/badge/github.com/nkiri/xls.svg)](https://pkg.go.dev/github.com/nkiri/xls)

## Installation

```bash
go get github.com/nkiri/xls
```

## Usage

### Open a file

```go
wb, err := xls.Open("book.xls")
if err != nil {
    log.Fatal(err)
}
```

### Access sheets

```go
// By index (0-based)
sh := wb.Sheet(0)

// By name
sh = wb.SheetByName("Sheet1")

fmt.Println(sh.Name)         // sheet name
fmt.Println(wb.SheetCount()) // number of sheets
```

### Read cells

```go
// Get all cell values as [][]string
rows := sh.Strings()
for _, row := range rows {
    fmt.Println(row)
}

// Access rows and cells individually
for i := 0; i < sh.RowCount(); i++ {
    row := sh.Row(i)
    if row == nil {
        continue
    }
    for _, cell := range row.Cells() {
        fmt.Printf("[%d,%d] %s\n", cell.Row, cell.Col, cell.Value())
    }
}
```

### Type-specific cell values

```go
cell := sh.Row(0).Cell(0)

switch cell.Type {
case xls.CellTypeString:
    fmt.Println(cell.String())
case xls.CellTypeNumber:
    fmt.Println(cell.Float())
case xls.CellTypeBool:
    fmt.Println(cell.Bool())
case xls.CellTypeDate:
    fmt.Println(cell.Time())
}
```

## Supported features

| Feature | Status |
|---------|--------|
| Reading (BIFF8 / Excel 97–2003) | ✅ |
| String, number, boolean, date cells | ✅ |
| Shared String Table (SST) | ✅ |
| Formula cells (cached value) | ✅ |
| 1904 date system | ✅ |
| Writing | Not implemented |

## Example

[`example/xlsdump`](./example/xlsdump) is a sample program that prints all cell data from an XLS file in a table format.

```bash
go run ./example/xlsdump path/to/file.xls
```

## Documentation

https://pkg.go.dev/github.com/nkiri/xls

## License

MIT
