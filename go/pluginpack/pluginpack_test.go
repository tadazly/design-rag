package pluginpack

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSourceContractIsProductionNamedAndNodeFree(t *testing.T) {
	evidence, err := ValidateSource(repositoryRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Status != "PASS" || evidence.PluginName != ProductionPluginName || evidence.MCPCommand != "./bin/drag" || len(evidence.MCPArgs) != 1 || evidence.MCPArgs[0] != "mcp" {
		t.Fatalf("unexpected evidence: %#v", evidence)
	}
}

func TestDistributionVersionRejectsCodexCachebuster(t *testing.T) {
	if err := validateDistributionVersion("0.3.0", "0.3.0"); err != nil {
		t.Fatalf("clean distribution version rejected: %v", err)
	}
	for _, version := range []string{"0.3.0+codex.20260903024303", "0.3.0+build.1", "0.3.0-beta.1"} {
		if err := validateDistributionVersion(version, "0.3.0"); err == nil {
			t.Fatalf("non-clean distribution version accepted: %s", version)
		}
	}
}

func TestArchiveRejectsCodexCachebusterVersion(t *testing.T) {
	root := filepath.Join(t.TempDir(), "design-rag-local")
	pluginRoot := filepath.Join(root, "plugins", ProductionPluginName)
	for _, relative := range []string{filepath.Join(".codex-plugin", "plugin.json"), ".mcp.json", filepath.Join("bin", "drag.exe")} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(pluginRoot, relative)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, ".codex-plugin", "plugin.json"), []byte(`{"name":"design-rag","version":"0.3.0+codex.20260903024303"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, ".mcp.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "bin", "drag.exe"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(filepath.Dir(root), "cachebuster.zip")
	err := createZip(root, archivePath, ProductionPluginName, "drag.exe", "0.3.0")
	if err == nil || !strings.Contains(err.Error(), "不含 build metadata") {
		t.Fatalf("cachebuster archive error=%v", err)
	}
	if _, statErr := os.Stat(archivePath); !os.IsNotExist(statErr) {
		t.Fatalf("rejected archive should be removed: %v", statErr)
	}
}

func TestWindowsGoTestStageIsIsolatedAndHasRealMCPHandshake(t *testing.T) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("Windows x64 native stage only")
	}
	evidence, err := Build(context.Background(), Options{ProjectRoot: repositoryRoot(t), OutputRoot: t.TempDir(), Target: "win32-x64", TestMarker: true})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Status != "PASS" || evidence.PluginName != TestPluginName || evidence.MCPHandshake != "PASS" || evidence.RuntimeExecution != "PASS" || evidence.LicenseFileCount < 9 || evidence.NodeArtifactCount != 0 || !evidence.TestMarkerIsolated {
		t.Fatalf("unexpected evidence: %#v", evidence)
	}
	pluginRoot := filepath.Join(evidence.StageRoot, "plugins", TestPluginName)
	mcpConfig := string(mustRead(filepath.Join(pluginRoot, ".mcp.json")))
	for _, marker := range []string{TestPluginName, "game-design-rag-go-test", "DESIGN_RAG_STATE_NAMESPACE"} {
		if !strings.Contains(mcpConfig, marker) {
			t.Fatalf("isolated stage missing %s", marker)
		}
	}
	skill := string(mustRead(filepath.Join(pluginRoot, "skills", "game-design-rag-go-test", "SKILL.md")))
	if !regexp.MustCompile(`(?m)^name:\s*game-design-rag-go-test\s*$`).MatchString(skill) || strings.Contains(skill, "go-test-go-test") {
		t.Fatalf("isolated Skill identity is invalid: %s", skill)
	}
}

func TestDarwinStageIsStaticallyNodeFreeAndProductionNamed(t *testing.T) {
	evidence, err := Build(context.Background(), Options{ProjectRoot: repositoryRoot(t), OutputRoot: t.TempDir(), Target: "darwin-arm64"})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Status != "PASS" || evidence.PluginName != ProductionPluginName || evidence.Binary.Format != "mach-o" || evidence.Binary.Architecture != "arm64" || evidence.LicenseFileCount < 9 || evidence.NodeArtifactCount != 0 {
		t.Fatalf("unexpected evidence: %#v", evidence)
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		if evidence.RuntimeExecution != "NOT_TESTED" || evidence.MCPHandshake != "NOT_TESTED" {
			t.Fatalf("cross-host execution must remain NOT_TESTED: %#v", evidence)
		}
	}
	for _, path := range []string{
		filepath.Join(evidence.StageRoot, "plugins", ProductionPluginName, ".mcp.json"),
		filepath.Join(evidence.StageRoot, "plugins", ProductionPluginName, ".codex-plugin", "plugin.json"),
		filepath.Join(evidence.StageRoot, ".agents", "plugins", "marketplace.json"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "go-test") {
			t.Fatalf("production stage leaked marker: %s", path)
		}
	}
}

func TestForeignFinalPackFailsBeforeMutation(t *testing.T) {
	foreign := "darwin-arm64"
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		foreign = "win32-x64"
	}
	output := t.TempDir()
	_, err := Build(context.Background(), Options{ProjectRoot: repositoryRoot(t), OutputRoot: output, Target: foreign, Pack: true})
	if err == nil || !strings.Contains(err.Error(), "目标原生 runner") {
		t.Fatalf("foreign pack error=%v", err)
	}
	entries, readErr := os.ReadDir(output)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("foreign pack mutated output: entries=%v err=%v", entries, readErr)
	}
}

func TestUnownedTargetIsNeverDeleted(t *testing.T) {
	outputRoot := t.TempDir()
	finalTarget := filepath.Join(outputRoot, "win32-x64")
	workTarget := filepath.Join(outputRoot, ".win32-x64-work")
	if err := os.MkdirAll(finalTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(finalTarget, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("user-owned"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceGeneratedTarget(outputRoot, workTarget, finalTarget, "win32-x64"); err == nil || !strings.Contains(err.Error(), "ownership marker") {
		t.Fatalf("unowned target replacement error=%v", err)
	}
	if raw, err := os.ReadFile(sentinel); err != nil || string(raw) != "user-owned" {
		t.Fatalf("unowned target was modified: %q %v", raw, err)
	}
}

func TestPluginSourceAllowlistRejectsUntrackedSecret(t *testing.T) {
	source := filepath.Join(t.TempDir(), "plugin")
	repositorySource := filepath.Join(repositoryRoot(t), "plugins", ProductionPluginName)
	for _, relative := range pluginSourceFiles {
		target := filepath.Join(source, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := copyFile(filepath.Join(repositorySource, filepath.FromSlash(relative)), target, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, ".env"), []byte("SECRET=value"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validatePluginSourceFiles(source); err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("unexpected source file was accepted: %v", err)
	}
}

func TestLibraryRejectsGoTestFinalArchive(t *testing.T) {
	output := t.TempDir()
	_, err := Build(context.Background(), Options{ProjectRoot: repositoryRoot(t), OutputRoot: output, Target: "win32-x64", TestMarker: true, Pack: true})
	if err == nil || !strings.Contains(err.Error(), "go-test") {
		t.Fatalf("go-test final archive error=%v", err)
	}
	entries, readErr := os.ReadDir(output)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("rejected archive mutated output: %v %v", entries, readErr)
	}
}

func TestNodeArtifactScannerCoversNativeAndSourceArtifacts(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{"addon.node", "source.ts", "package.json", filepath.Join("node_modules", "x", "index.js")} {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	count, files, err := findNodeArtifacts(root)
	if err != nil || count != 4 {
		t.Fatalf("node artifact scan count=%d files=%v err=%v", count, files, err)
	}
}
