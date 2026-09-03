// xlsdump は XLS ファイルを読み込み、全シートのセルデータを
// [][]string として取得して標準出力に表示するサンプルプログラムです。
//
// 使い方:
//
//	go run ./example/xlsdump <file.xls>
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/nkiri/xls"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "使い方: xlsdump <file.xls>")
		os.Exit(1)
	}

	// ── 1. XLS ファイルを開いて Workbook を取得 ──────────────────────────────
	wb, err := xls.Open(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "エラー: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("シート数: %d\n\n", wb.SheetCount())

	// ── 2. 各シートを走査 ─────────────────────────────────────────────────────
	for i := 0; i < wb.SheetCount(); i++ {
		sh := wb.Sheet(i)

		// ── 3. Sheet.Strings() でセルデータを [][]string として取得 ──────────
		rows := sh.Strings()

		// ── 4. 標準出力に表示 ──────────────────────────────────────────────────
		fmt.Printf("=== Sheet %d: %s (%d 行) ===\n", i+1, sh.Name(), len(rows))
		printTable(rows)
		fmt.Println()
	}
}

// printTable は [][]string を揃えた表形式で標準出力に出力します。
func printTable(rows [][]string) {
	if len(rows) == 0 {
		fmt.Println("  (データなし)")
		return
	}

	// 列ごとの最大幅を計算してカラムを揃える
	colWidths := columnWidths(rows)

	// ヘッダ区切り線
	sep := buildSeparator(colWidths)

	fmt.Println(sep)
	for _, row := range rows {
		fmt.Print("|")
		for col, w := range colWidths {
			cell := ""
			if col < len(row) {
				cell = row[col]
			}
			// 右にスペースを埋めて幅を揃える
			fmt.Printf(" %-*s |", w, cell)
		}
		fmt.Println()
	}
	fmt.Println(sep)
}

// columnWidths は各列の最大文字幅を返します。
func columnWidths(rows [][]string) []int {
	maxCols := 0
	for _, row := range rows {
		if len(row) > maxCols {
			maxCols = len(row)
		}
	}

	widths := make([]int, maxCols)
	for _, row := range rows {
		for col, cell := range row {
			// マルチバイト文字を考慮した表示幅
			w := displayWidth(cell)
			if w > widths[col] {
				widths[col] = w
			}
		}
	}
	// 最低幅 1 を保証
	for i, w := range widths {
		if w < 1 {
			widths[i] = 1
		}
	}
	return widths
}

// buildSeparator は列幅に合わせた区切り線を生成します (例: +------+-------+)
func buildSeparator(colWidths []int) string {
	var sb strings.Builder
	sb.WriteByte('+')
	for _, w := range colWidths {
		sb.WriteString(strings.Repeat("-", w+2))
		sb.WriteByte('+')
	}
	return sb.String()
}

// displayWidth は文字列の表示幅を返します。
// ASCII は 1、それ以外（CJK など）は 2 として概算します。
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		if r > 0x7F {
			w += 2
		} else {
			w++
		}
	}
	return w
}
