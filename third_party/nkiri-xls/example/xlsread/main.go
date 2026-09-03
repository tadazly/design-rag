// xlsread reads an XLS file and prints each sheet as a [][]string.
//
// Usage:
//
//	xlsread [flags] <file.xls>
//
// Flags:
//
//	-fmt  tsv   Tab-separated values (default)
//	      csv   Comma-separated values
//	      json  JSON array of {"name":…,"rows":[…]} objects
//	      go    Go [][]string literal
//	-sheet N    Print only sheet N (0-based index; default: all sheets)
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nkiri/xls"
)

func main() {
	fmtFlag := flag.String("fmt", "tsv", "output format: tsv | csv | json | go")
	sheetFlag := flag.Int("sheet", -1, "sheet index to print (-1 = all)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: xlsread [flags] <file.xls>\n\nFlags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}

	wb, err := xls.Open(flag.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "xlsread: %v\n", err)
		os.Exit(1)
	}

	for i := 0; i < wb.SheetCount(); i++ {
		if *sheetFlag >= 0 && i != *sheetFlag {
			continue
		}
		sh := wb.Sheet(i)
		rows := sheetToStrings(sh)

		switch *fmtFlag {
		case "tsv":
			printTSV(os.Stdout, sh.Name(), rows)
		case "csv":
			printCSV(os.Stdout, sh.Name(), rows)
		case "json":
			printJSON(os.Stdout, sh.Name(), rows, i == wb.SheetCount()-1 || *sheetFlag >= 0)
		case "go":
			printGo(os.Stdout, sh.Name(), rows)
		default:
			fmt.Fprintf(os.Stderr, "xlsread: unknown format %q (use tsv, csv, json, go)\n", *fmtFlag)
			os.Exit(1)
		}
	}
}

// sheetToStrings converts a Sheet into a [][]string.
// Nil rows become empty slices; nil cells become empty strings.
func sheetToStrings(sh *xls.Sheet) [][]string {
	rows := make([][]string, sh.RowCount())
	for r := 0; r < sh.RowCount(); r++ {
		row := sh.Row(r)
		if row == nil {
			rows[r] = []string{}
			continue
		}
		rows[r] = make([]string, row.CellCount())
		for c := 0; c < row.CellCount(); c++ {
			rows[r][c] = row.Cell(c).Value()
		}
	}
	return rows
}

// ── Output formatters ─────────────────────────────────────────────────────────

func printTSV(w io.Writer, name string, rows [][]string) {
	fmt.Fprintf(w, "# Sheet: %s\n", name)
	for _, row := range rows {
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}
}

func printCSV(w io.Writer, name string, rows [][]string) {
	fmt.Fprintf(w, "# Sheet: %s\n", name)
	cw := csv.NewWriter(w)
	for _, row := range rows {
		_ = cw.Write(row)
	}
	cw.Flush()
}

// jsonSheet is the JSON representation of one sheet.
type jsonSheet struct {
	Name string     `json:"name"`
	Rows [][]string `json:"rows"`
}

func printJSON(w io.Writer, name string, rows [][]string, last bool) {
	data, err := json.MarshalIndent(jsonSheet{Name: name, Rows: rows}, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "xlsread: json marshal: %v\n", err)
		return
	}
	fmt.Fprintln(w, string(data))
}

func printGo(w io.Writer, name string, rows [][]string) {
	fmt.Fprintf(w, "// Sheet: %s\n", name)
	fmt.Fprintln(w, "[][]string{")
	for _, row := range rows {
		fmt.Fprint(w, "\t{")
		for i, cell := range row {
			fmt.Fprintf(w, "%q", cell)
			if i < len(row)-1 {
				fmt.Fprint(w, ", ")
			}
		}
		fmt.Fprintln(w, "},")
	}
	fmt.Fprintln(w, "}")
}
