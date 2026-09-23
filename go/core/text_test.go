package core

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"golang.org/x/text/unicode/norm"
)

func TestNormalizeChunkDateAndFamily(t *testing.T) {
	t.Parallel()
	if got := NormalizeText("ＡＢＣ\u200b  奖励\n逻辑"); got != "abc 奖励 逻辑" {
		t.Fatalf("NormalizeText() = %q", got)
	}
	blocks := []Block{{Text: "玩法流程。奖励产出。", HeadingPath: []string{"玩法"}, Locator: "段落 1"}}
	chunks := ChunkBlocks(blocks)
	if len(chunks) != 1 || chunks[0].SectionType != "gameplay" || chunks[0].ContentHash == "" {
		t.Fatalf("unexpected chunks: %#v", chunks)
	}
	candidate := Candidate{AbsolutePath: filepath.Join("C:\\资料", "2026-09-01", "活动_20260831.docx"), FilesystemMtimeMS: 1}
	date := ResolveEffectiveDate(candidate, ExtractedDocument{})
	if date.DateSource != "filename" || date.EffectiveUpdatedAtMS == 0 {
		t.Fatalf("unexpected date: %#v", date)
	}
	key, confidence := MakeFamilyKey("【复用】幸运轮盘策划案_20260901_v2")
	if key == "" || confidence < 0.45 {
		t.Fatalf("unexpected family: %q %f", key, confidence)
	}
}

func TestPureGoTextCompatibilityPreservesHTMLStructureAndUTF16(t *testing.T) {
	root := t.TempDir()
	htmlPath := filepath.Join(root, "玩法.html")
	htmlText := `<html><body><h1>活动玩法</h1><p>点击按钮后消耗抽奖券。</p><script>不可信脚本内容</script><table><tr><th>ID</th><th>奖励</th></tr><tr><td>1001</td><td>钻石</td></tr></table></body></html>`
	if err := os.WriteFile(htmlPath, []byte(htmlText), 0o644); err != nil {
		t.Fatal(err)
	}
	document, err := extractText(htmlPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Blocks) != 2 || document.Blocks[0].Text != "点击按钮后消耗抽奖券。" || len(document.Blocks[0].HeadingPath) != 1 || document.Blocks[0].HeadingPath[0] != "活动玩法" || document.Blocks[1].Locator != "表格 1 行 1-2" || strings.Contains(document.Blocks[1].Text, "脚本") {
		t.Fatalf("HTML structure lost: %#v", document.Blocks)
	}

	utf16Path := filepath.Join(root, "规则.md")
	units := append([]uint16{0xfeff}, utf16.Encode([]rune("# 规则\r\n\r\n龙🙂奖励"))...)
	raw := make([]byte, len(units)*2)
	for index, unit := range units {
		binary.LittleEndian.PutUint16(raw[index*2:], unit)
	}
	if err := os.WriteFile(utf16Path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	decoded, err := extractText(utf16Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Blocks) != 1 || decoded.Blocks[0].Text != "龙🙂奖励" || len(decoded.Warnings) != 1 || !strings.Contains(decoded.Warnings[0], "utf16le") {
		t.Fatalf("UTF-16 decode failed: %#v", decoded)
	}
}

func TestPureGoCSVDelimiterAndDateEvidence(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "版本.csv")
	if err := os.WriteFile(filePath, []byte("版本日期;名称;奖励\n2026-09-02;轮盘;钻石\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	document, err := extractCSV(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Blocks) != 1 || !strings.Contains(document.Blocks[0].Text, "B=轮盘") || len(document.DateEvidence) == 0 {
		t.Fatalf("CSV compatibility failed: %#v", document)
	}
	found := false
	for _, evidence := range document.DateEvidence {
		found = found || evidence.Kind == "version_field" && evidence.Locator == "CSV!A2"
	}
	if !found {
		t.Fatalf("CSV structured date evidence missing: %#v", document.DateEvidence)
	}
}

func TestBuildSearchTermsKeepsStrongASCIIIdentifiersAndAliases(t *testing.T) {
	t.Parallel()
	terms := strings.Fields(BuildSearchTerms("POOL_001_ABC 配置 root/file.xlsx D1:D8"))
	termSet := make(map[string]bool, len(terms))
	for _, term := range terms {
		termSet[term] = true
	}
	for _, want := range []string{"pool_001_abc", "pool", "001", "abc", "root/file.xlsx", "root", "file.xlsx", "file", "xlsx", "d1:d8", "d1", "d8", "配置"} {
		if !termSet[want] {
			t.Fatalf("BuildSearchTerms() omitted %q: %q", want, terms)
		}
	}
}

func regexpNormalizeTextReference(value string) string {
	value = norm.NFKC.String(value)
	value = strings.ToLower(strings.TrimSpace(zeroWidthPattern.ReplaceAllString(value, "")))
	return spacePattern.ReplaceAllString(value, " ")
}

func TestNormalizeTextMatchesRegexpReference(t *testing.T) {
	t.Parallel()
	samples := []string{
		"", " ", "\t\n", "a", "ＡＢＣ\u200b  奖励\n逻辑", "  前后空白\u3000", "\u3000全角空格\u3000中间\u3000",
		"行 12 | A=id | B=名字\r\n行 13 | A=2\t|\tB=\f值", "多  个   空格", "零\u200c宽\u200d字\ufeff符", "\u00a0不换行\u00a0空格\u00a0",
		"İstanbul ǅ Σ ﬁ ① ㈱ ｶﾞ", "\v垂直制表\v", "a \u0085 b", "\xff\xfe无效\x80字节 ", "混合 Mixed\tCASE\nAlphaLottery betaPool",
		strings.Repeat("配置 ", 2000) + "\n\n" + strings.Repeat("x\t", 500),
	}
	alphabet := []rune{' ', '\t', '\n', '\r', '\f', '\v', '\u00a0', '\u3000', '\u200b', '\u200c', '\u200d', '\ufeff', '\u0085', 'A', 'z', 'İ', 'Σ', '配', '表', 'Ａ', '①', 'ﬁ', '|', '='}
	state := uint32(20260924)
	for count := 0; count < 3000; count++ {
		var builder strings.Builder
		for length := int(state % 40); length > 0; length-- {
			state = state*1664525 + 1013904223
			builder.WriteRune(alphabet[int(state>>8)%len(alphabet)])
		}
		state = state*1664525 + 1013904223
		samples = append(samples, builder.String())
	}
	for _, sample := range samples {
		if got, want := NormalizeText(sample), regexpNormalizeTextReference(sample); got != want {
			t.Fatalf("NormalizeText(%q) = %q, want %q", sample, got, want)
		}
	}
}
