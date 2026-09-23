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
	if evidence.ClaudeMarketplaceName != "design-rag-local" || evidence.ClaudeMCPCommand != "${CLAUDE_PLUGIN_ROOT}/bin/drag" || len(evidence.ClaudeMCPArgs) != 1 || evidence.ClaudeMCPArgs[0] != "mcp" {
		t.Fatalf("unexpected Claude evidence: %#v", evidence)
	}
}

// copyProjectForValidation copies exactly the inputs ValidateSource reads.
func copyProjectForValidation(t *testing.T) string {
	t.Helper()
	source := repositoryRoot(t)
	target := t.TempDir()
	relatives := []string{"package.json", "LICENSE", "NOTICE", filepath.Join("packaging", "design-rag-marketplace.json"), filepath.Join("packaging", "design-rag-claude-marketplace.json")}
	for _, relative := range pluginSourceFiles {
		relatives = append(relatives, filepath.Join("plugins", ProductionPluginName, filepath.FromSlash(relative)))
	}
	for _, relative := range relatives {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(target, relative)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := copyFile(filepath.Join(source, relative), filepath.Join(target, relative), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ValidateSource(target); err != nil {
		t.Fatalf("copied project must validate before mutation: %v", err)
	}
	return target
}

func TestSourceRejectsRootMCPConfigLoadedByClaudeCode(t *testing.T) {
	project := copyProjectForValidation(t)
	codexConfig := filepath.Join(project, "plugins", ProductionPluginName, codexMCPConfigFile)
	if err := copyFile(codexConfig, filepath.Join(project, "plugins", ProductionPluginName, ".mcp.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateSource(project); err == nil || !strings.Contains(err.Error(), ".mcp.json") {
		t.Fatalf("root .mcp.json must be rejected before Claude Code can auto-load it: %v", err)
	}
}

func TestClaudeManifestContractRejectsUnsafeLaunchConfig(t *testing.T) {
	manifestPath := filepath.Join(repositoryRoot(t), "plugins", ProductionPluginName, ".claude-plugin", "plugin.json")
	load := func() map[string]any {
		manifest, err := readJSONObject(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		return manifest
	}
	server := func(manifest map[string]any) map[string]any {
		return manifest["mcpServers"].(map[string]any)[ProductionPluginName].(map[string]any)
	}
	version := stringValue(load(), "version")
	if err := validateClaudeManifest(load(), ProductionPluginName, "DRAG 游戏策划知识库", "game-design-rag", version, false); err != nil {
		t.Fatalf("source Claude manifest rejected: %v", err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"cwd-relative command": func(manifest map[string]any) { server(manifest)["command"] = "./bin/drag" },
		"unsupported cwd":      func(manifest map[string]any) { server(manifest)["cwd"] = "." },
		"production env": func(manifest map[string]any) {
			server(manifest)["env"] = map[string]any{"DESIGN_RAG_STATE_NAMESPACE": "x"}
		},
		"config file path": func(manifest map[string]any) { manifest["mcpServers"] = "./.codex-mcp.json" },
		"second server": func(manifest map[string]any) {
			manifest["mcpServers"].(map[string]any)["extra"] = map[string]any{"command": "x"}
		},
		"skills override":     func(manifest map[string]any) { manifest["skills"] = "./skills/" },
		"cachebuster version": func(manifest map[string]any) { manifest["version"] = version + "+codex.1" },
		"foreign repository":  func(manifest map[string]any) { manifest["repository"] = "https://example.com/fork.git" },
	} {
		manifest := load()
		mutate(manifest)
		if err := validateClaudeManifest(manifest, ProductionPluginName, "DRAG 游戏策划知识库", "game-design-rag", version, false); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestSkillAllowedToolsPreapprovesOnlyReadOnlyTools(t *testing.T) {
	skill := string(mustRead(filepath.Join(repositoryRoot(t), "plugins", ProductionPluginName, "skills", "game-design-rag", "SKILL.md")))
	if err := validateSkillAllowedTools(skill, ProductionPluginName); err != nil {
		t.Fatalf("source Skill rejected: %v", err)
	}
	line := regexp.MustCompile(`(?m)^allowed-tools:.*$`).FindString(skill)
	prefix := "mcp__plugin_design-rag_design-rag__"
	for name, replacement := range map[string]string{
		"management tool":  line + " " + prefix + "drag_cache_clear",
		"missing tool":     strings.Replace(line, " "+prefix+"drag_index_status", "", 1),
		"wrong namespace":  strings.ReplaceAll(line, prefix, "mcp__design-rag__"),
		"no preapproval":   "",
		"server wildcards": "allowed-tools: mcp__plugin_design-rag_design-rag__*",
	} {
		if err := validateSkillAllowedTools(strings.Replace(skill, line, replacement, 1), ProductionPluginName); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestClaudeLaunchSubstitutesPluginRootWithoutCWD(t *testing.T) {
	pluginRoot := filepath.Join(t.TempDir(), "plugin")
	project := t.TempDir()
	write := func(server map[string]any) {
		if err := os.MkdirAll(filepath.Join(pluginRoot, ".claude-plugin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(filepath.Join(pluginRoot, ".claude-plugin", "plugin.json"), map[string]any{"name": TestPluginName, "mcpServers": map[string]any{TestPluginName: server}}); err != nil {
			t.Fatal(err)
		}
	}
	write(map[string]any{"command": "${CLAUDE_PLUGIN_ROOT}/bin/drag", "args": []any{"mcp"}, "env": goTestEnvironment(TestPluginName, "game-design-rag-go-test", "${CLAUDE_PLUGIN_ROOT}")})
	launch, err := claudeMCPLaunch(pluginRoot, TestPluginName, project)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Command != filepath.Join(pluginRoot, "bin", "drag") || launch.Dir != project || launch.Env["DESIGN_RAG_PLUGIN_ROOT"] != pluginRoot || launch.Env["CLAUDE_PLUGIN_ROOT"] != pluginRoot || len(launch.Args) != 1 || launch.Args[0] != "mcp" {
		t.Fatalf("unexpected Claude launch: %#v", launch)
	}
	for name, server := range map[string]map[string]any{
		"unknown placeholder": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/drag", "args": []any{"mcp"}, "env": map[string]any{"X": "${HOME}"}},
		"escaping command":    {"command": "${CLAUDE_PLUGIN_ROOT}/../drag", "args": []any{"mcp"}},
		"relative command":    {"command": "./bin/drag", "args": []any{"mcp"}},
	} {
		write(server)
		if _, err := claudeMCPLaunch(pluginRoot, TestPluginName, project); err == nil {
			t.Fatalf("%s was accepted", name)
		}
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
	for _, relative := range []string{filepath.Join(".codex-plugin", "plugin.json"), codexMCPConfigFile, filepath.Join("bin", "drag.exe")} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(pluginRoot, relative)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, ".codex-plugin", "plugin.json"), []byte(`{"name":"design-rag","version":"0.3.0+codex.20260903024303"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, codexMCPConfigFile), []byte(`{}`), 0o644); err != nil {
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

func TestArchiveRequiresBothHostsAndRejectsClaudeDefaultMCPConfig(t *testing.T) {
	root := filepath.Join(t.TempDir(), "design-rag-local")
	pluginRoot := filepath.Join(root, "plugins", ProductionPluginName)
	files := map[string]string{
		filepath.Join(pluginRoot, ".codex-plugin", "plugin.json"):     `{"name":"design-rag","version":"0.3.0"}`,
		filepath.Join(pluginRoot, ".claude-plugin", "plugin.json"):    `{"name":"design-rag","version":"0.3.0"}`,
		filepath.Join(pluginRoot, codexMCPConfigFile):                 `{}`,
		filepath.Join(pluginRoot, "bin", "drag.exe"):                  "binary",
		filepath.Join(pluginRoot, "LICENSE"):                          "license",
		filepath.Join(pluginRoot, "NOTICE"):                           "notice",
		filepath.Join(root, ".agents", "plugins", "marketplace.json"): `{}`,
		filepath.Join(root, ".claude-plugin", "marketplace.json"):     `{}`,
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	archive := func(name string) error {
		return createZip(root, filepath.Join(filepath.Dir(root), name), ProductionPluginName, "drag.exe", "0.3.0")
	}
	if err := archive("complete.zip"); err != nil {
		t.Fatalf("complete dual-host archive rejected: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, ".mcp.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := archive("root-mcp.zip"); err == nil || !strings.Contains(err.Error(), ".mcp.json") {
		t.Fatalf("archive with root .mcp.json error=%v", err)
	}
	if err := os.Remove(filepath.Join(pluginRoot, ".mcp.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, ".claude-plugin", "marketplace.json")); err != nil {
		t.Fatal(err)
	}
	if err := archive("missing-claude.zip"); err == nil || !strings.Contains(err.Error(), ".claude-plugin/marketplace.json") {
		t.Fatalf("archive without Claude marketplace error=%v", err)
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
	if evidence.Status != "PASS" || evidence.PluginName != TestPluginName || evidence.MCPHandshake != "PASS" || evidence.ClaudeMCPHandshake != "PASS" || evidence.RuntimeExecution != "PASS" || evidence.LicenseFileCount < 9 || evidence.NodeArtifactCount != 0 || !evidence.TestMarkerIsolated {
		t.Fatalf("unexpected evidence: %#v", evidence)
	}
	pluginRoot := filepath.Join(evidence.StageRoot, "plugins", TestPluginName)
	for _, config := range []string{codexMCPConfigFile, filepath.Join(".claude-plugin", "plugin.json")} {
		text := string(mustRead(filepath.Join(pluginRoot, config)))
		for _, marker := range []string{TestPluginName, "game-design-rag-go-test", "DESIGN_RAG_STATE_NAMESPACE"} {
			if !strings.Contains(text, marker) {
				t.Fatalf("isolated %s missing %s", config, marker)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(pluginRoot, ".mcp.json")); !os.IsNotExist(err) {
		t.Fatalf("stage must not ship a root .mcp.json for Claude Code: %v", err)
	}
	claudeMarketplace := string(mustRead(filepath.Join(evidence.StageRoot, ".claude-plugin", "marketplace.json")))
	if !strings.Contains(claudeMarketplace, `"./plugins/`+TestPluginName+`"`) || !strings.Contains(claudeMarketplace, `"`+TestPluginName+`-local"`) {
		t.Fatalf("isolated Claude marketplace identity is invalid: %s", claudeMarketplace)
	}
	skill := string(mustRead(filepath.Join(pluginRoot, "skills", "game-design-rag-go-test", "SKILL.md")))
	if !regexp.MustCompile(`(?m)^name:\s*game-design-rag-go-test\s*$`).MatchString(skill) || strings.Contains(skill, "go-test-go-test") || !strings.Contains(skill, "mcp__plugin_design-rag-go-test_design-rag-go-test__drag_search") {
		t.Fatalf("isolated Skill identity is invalid: %s", skill)
	}
	guide := string(mustRead(filepath.Join(evidence.StageRoot, "INSTALL.md")))
	if !strings.Contains(guide, "claude plugin install "+TestPluginName+"@"+TestPluginName+"-local") || !strings.Contains(guide, "codex plugin add "+TestPluginName+"@"+TestPluginName+"-local") {
		t.Fatalf("install guide must cover both hosts: %s", guide)
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
		if evidence.RuntimeExecution != "NOT_TESTED" || evidence.MCPHandshake != "NOT_TESTED" || evidence.ClaudeMCPHandshake != "NOT_TESTED" {
			t.Fatalf("cross-host execution must remain NOT_TESTED: %#v", evidence)
		}
	}
	claudeManifest, err := readJSONObject(filepath.Join(evidence.StageRoot, "plugins", ProductionPluginName, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	claudeServer := claudeManifest["mcpServers"].(map[string]any)[ProductionPluginName].(map[string]any)
	if claudeServer["command"] != "${CLAUDE_PLUGIN_ROOT}/bin/drag" {
		t.Fatalf("Claude command must stay cross-platform: %#v", claudeServer)
	}
	for _, path := range []string{
		filepath.Join(evidence.StageRoot, "plugins", ProductionPluginName, codexMCPConfigFile),
		filepath.Join(evidence.StageRoot, "plugins", ProductionPluginName, ".codex-plugin", "plugin.json"),
		filepath.Join(evidence.StageRoot, "plugins", ProductionPluginName, ".claude-plugin", "plugin.json"),
		filepath.Join(evidence.StageRoot, ".agents", "plugins", "marketplace.json"),
		filepath.Join(evidence.StageRoot, ".claude-plugin", "marketplace.json"),
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
