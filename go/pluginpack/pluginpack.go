package pluginpack

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"debug/macho"
	"debug/pe"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tadazly/design-rag/go/core"
)

const (
	ProductionPluginName = "design-rag"
	TestPluginName       = "design-rag-go-test"
	productionWebsite    = "https://github.com/tadazly/design-rag"
	ownerMarkerFile      = ".design-rag-pluginpack-owned.json"
)

var pluginSourceFiles = []string{
	".mcp.json",
	"STAGING.md",
	"THIRD_PARTY_NOTICES.md",
	".codex-plugin/plugin.json",
	"skills/game-design-rag/SKILL.md",
	"skills/game-design-rag/agents/openai.yaml",
	"skills/game-design-rag/references/administration.md",
	"skills/game-design-rag/references/analysis-workflows.md",
}

var distributionVersionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

type Target struct {
	ID         string
	GOOS       string
	GOARCH     string
	BinaryName string
	Format     string
	Arch       string
}

var targets = map[string]Target{
	"win32-x64":    {ID: "win32-x64", GOOS: "windows", GOARCH: "amd64", BinaryName: "drag.exe", Format: "pe", Arch: "x64"},
	"darwin-arm64": {ID: "darwin-arm64", GOOS: "darwin", GOARCH: "arm64", BinaryName: "drag", Format: "mach-o", Arch: "arm64"},
}

type Options struct {
	ProjectRoot string
	OutputRoot  string
	Target      string
	TestMarker  bool
	Pack        bool
}

type BinaryEvidence struct {
	Path         string `json:"path"`
	SHA256       string `json:"sha256"`
	SizeBytes    int64  `json:"sizeBytes"`
	Format       string `json:"format"`
	Architecture string `json:"architecture"`
}

type Evidence struct {
	Status                   string         `json:"status"`
	Mode                     string         `json:"mode"`
	Target                   string         `json:"target"`
	PluginName               string         `json:"pluginName"`
	MarketplaceName          string         `json:"marketplaceName"`
	MCPServerName            string         `json:"mcpServerName"`
	SkillName                string         `json:"skillName"`
	Version                  string         `json:"version"`
	StageRoot                string         `json:"stageRoot"`
	Binary                   BinaryEvidence `json:"binary"`
	RuntimeExecution         string         `json:"runtimeExecution"`
	RuntimeReason            string         `json:"runtimeReason,omitempty"`
	MCPHandshake             string         `json:"mcpHandshake"`
	MCPReason                string         `json:"mcpReason,omitempty"`
	LicenseFileCount         int            `json:"licenseFileCount"`
	SourceManifestSHA256     string         `json:"sourceManifestSha256"`
	DependencyManifestSHA256 string         `json:"dependencyManifestSha256"`
	LocalPatchSHA256         string         `json:"localPatchSha256"`
	NodeArtifactCount        int            `json:"nodeArtifactCount"`
	TestMarkerIsolated       bool           `json:"testMarkerIsolated"`
	ArchiveValidation        string         `json:"archiveValidation"`
	Archive                  *Archive       `json:"archive"`
}

type Archive struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
}

type StaticEvidence struct {
	Status          string `json:"status"`
	PluginName      string `json:"pluginName"`
	Version         string `json:"version"`
	DisplayName     string `json:"displayName"`
	MarketplaceName string `json:"marketplaceName"`
	MCPCommand      string `json:"mcpCommand"`
	MCPArgs         []any  `json:"mcpArgs"`
}

func targetFor(value string) (Target, error) {
	target, ok := targets[value]
	if !ok {
		return Target{}, fmt.Errorf("不支持的 Plugin 目标：%s", value)
	}
	return target, nil
}

func readJSONObject(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("%s 含有多余 JSON 内容", path)
	}
	return value, nil
}

func nestedObject(value map[string]any, key string) (map[string]any, error) {
	object, ok := value[key].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("缺少对象字段 %s", key)
	}
	return object, nil
}

func stringValue(value map[string]any, key string) string {
	result, _ := value[key].(string)
	return result
}

func validateDistributionVersion(version, expected string) error {
	if !distributionVersionPattern.MatchString(version) {
		return fmt.Errorf("正式分发版本必须为不含 build metadata 的 x.y.z：%s", version)
	}
	if expected != "" && version != expected {
		return fmt.Errorf("正式分发版本与基础版本不一致：%s != %s", version, expected)
	}
	return nil
}

func writeJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644)
}

func ValidateSource(projectRoot string) (StaticEvidence, error) {
	projectRoot, _ = filepath.Abs(projectRoot)
	pluginRoot := filepath.Join(projectRoot, "plugins", ProductionPluginName)
	manifest, err := readJSONObject(filepath.Join(pluginRoot, ".codex-plugin", "plugin.json"))
	if err != nil {
		return StaticEvidence{}, err
	}
	rootPackage, err := readJSONObject(filepath.Join(projectRoot, "package.json"))
	if err != nil {
		return StaticEvidence{}, err
	}
	mcpConfig, err := readJSONObject(filepath.Join(pluginRoot, ".mcp.json"))
	if err != nil {
		return StaticEvidence{}, err
	}
	marketplace, err := readJSONObject(filepath.Join(projectRoot, "packaging", "design-rag-marketplace.json"))
	if err != nil {
		return StaticEvidence{}, err
	}
	for _, legalFile := range []string{"LICENSE", "NOTICE"} {
		info, statErr := os.Stat(filepath.Join(projectRoot, legalFile))
		if statErr != nil {
			return StaticEvidence{}, fmt.Errorf("项目缺少可分发的 %s: %w", legalFile, statErr)
		}
		if info.IsDir() {
			return StaticEvidence{}, fmt.Errorf("项目的 %s 不能是目录", legalFile)
		}
	}
	if stringValue(manifest, "name") != ProductionPluginName {
		return StaticEvidence{}, errors.New("Plugin 源 manifest 必须使用 design-rag")
	}
	version := stringValue(manifest, "version")
	if err := validateDistributionVersion(version, stringValue(rootPackage, "version")); err != nil {
		return StaticEvidence{}, err
	}
	if stringValue(manifest, "mcpServers") != "./.mcp.json" || stringValue(manifest, "skills") != "./skills/" {
		return StaticEvidence{}, errors.New("manifest 必须引用 ./.mcp.json 与 ./skills/")
	}
	interfaceObject, err := nestedObject(manifest, "interface")
	if err != nil || stringValue(interfaceObject, "displayName") != "DRAG 游戏策划知识库" {
		return StaticEvidence{}, errors.New("Plugin displayName 必须为 DRAG 游戏策划知识库")
	}
	if stringValue(interfaceObject, "websiteURL") != productionWebsite {
		return StaticEvidence{}, errors.New("Plugin websiteURL 必须指向公开项目网站")
	}
	servers, err := nestedObject(mcpConfig, "mcpServers")
	if err != nil || len(servers) != 1 {
		return StaticEvidence{}, errors.New(".mcp.json 必须且只能声明一个 MCP server")
	}
	server, ok := servers[ProductionPluginName].(map[string]any)
	if !ok {
		return StaticEvidence{}, errors.New(".mcp.json 缺少 design-rag server")
	}
	args, ok := server["args"].([]any)
	if !ok || len(args) != 1 || args[0] != "mcp" || stringValue(server, "command") != "./bin/drag" || stringValue(server, "cwd") != "." {
		return StaticEvidence{}, errors.New("源码 MCP 必须由 ./bin/drag mcp 启动，cwd 必须为 .")
	}
	if _, exists := server["env"]; exists {
		return StaticEvidence{}, errors.New("正式源码 MCP 不得内置测试环境变量")
	}
	if enabled, ok := server["enabled"].(bool); !ok || !enabled || stringValue(server, "default_tools_approval_mode") != "approve" {
		return StaticEvidence{}, errors.New("MCP 必须启用且默认只读工具自动 approve")
	}
	toolApprovals, ok := server["tools"].(map[string]any)
	mutatingTools := []string{"drag_source_add", "drag_source_update", "drag_source_remove", "drag_index_update", "drag_index_pause", "drag_index_resume", "drag_cache_clear"}
	if !ok || len(toolApprovals) != len(mutatingTools) {
		return StaticEvidence{}, errors.New("MCP 管理工具 approval 清单不完整")
	}
	for _, toolName := range mutatingTools {
		approval, _ := toolApprovals[toolName].(map[string]any)
		if stringValue(approval, "approval_mode") != "prompt" {
			return StaticEvidence{}, fmt.Errorf("MCP 管理工具必须 prompt approval：%s", toolName)
		}
	}
	if bytes.Contains(mustRead(filepath.Join(pluginRoot, "skills", "game-design-rag", "SKILL.md")), []byte("go-test")) {
		return StaticEvidence{}, errors.New("正式 Skill 不得残留 go-test 标记")
	}
	plugins, ok := marketplace["plugins"].([]any)
	if !ok || len(plugins) != 1 {
		return StaticEvidence{}, errors.New("marketplace 必须且只能声明一个 Plugin")
	}
	entry, ok := plugins[0].(map[string]any)
	if !ok || stringValue(entry, "name") != ProductionPluginName || stringValue(entry, "category") != "Productivity" {
		return StaticEvidence{}, errors.New("marketplace entry 必须为 design-rag")
	}
	marketplaceInterface, err := nestedObject(marketplace, "interface")
	if err != nil || stringValue(marketplaceInterface, "displayName") != "DRAG Local Marketplace" {
		return StaticEvidence{}, errors.New("marketplace displayName 无效")
	}
	source, _ := nestedObject(entry, "source")
	policy, _ := nestedObject(entry, "policy")
	if stringValue(source, "source") != "local" || stringValue(source, "path") != "./plugins/design-rag" || stringValue(policy, "installation") != "AVAILABLE" || stringValue(policy, "authentication") != "ON_INSTALL" {
		return StaticEvidence{}, errors.New("marketplace source 或 policy 不完整")
	}
	if err := validatePluginSourceFiles(pluginRoot); err != nil {
		return StaticEvidence{}, err
	}
	if count, files, err := findNodeArtifacts(pluginRoot); err != nil {
		return StaticEvidence{}, err
	} else if count > 0 {
		return StaticEvidence{}, fmt.Errorf("Plugin 源仍含 Node/JavaScript runtime 产物：%s", strings.Join(files, ", "))
	}
	return StaticEvidence{Status: "PASS", PluginName: ProductionPluginName, Version: version, DisplayName: stringValue(interfaceObject, "displayName"), MarketplaceName: stringValue(marketplace, "name"), MCPCommand: stringValue(server, "command"), MCPArgs: args}, nil
}

func mustRead(path string) []byte {
	raw, _ := os.ReadFile(path)
	return raw
}

func identity(testMarker bool) (pluginName, marketplaceName, displayName, skillName string) {
	if testMarker {
		return TestPluginName, TestPluginName + "-local", "DRAG 游戏策划知识库 (Go Test)", "game-design-rag-go-test"
	}
	return ProductionPluginName, ProductionPluginName + "-local", "DRAG 游戏策划知识库", "game-design-rag"
}

func Build(ctx context.Context, options Options) (Evidence, error) {
	root, err := filepath.Abs(options.ProjectRoot)
	if err != nil {
		return Evidence{}, err
	}
	static, err := ValidateSource(root)
	if err != nil {
		return Evidence{}, fmt.Errorf("Plugin 源校验失败: %w", err)
	}
	sourceHash, err := sourceManifestSHA256(filepath.Join(root, "plugins", ProductionPluginName))
	if err != nil {
		return Evidence{}, fmt.Errorf("计算 Plugin 源清单哈希失败: %w", err)
	}
	target, err := targetFor(options.Target)
	if err != nil {
		return Evidence{}, err
	}
	if options.Pack && options.TestMarker {
		return Evidence{}, errors.New("最终 archive 禁止使用 go-test 标记")
	}
	if options.Pack && (runtime.GOOS != target.GOOS || runtime.GOARCH != target.GOARCH) {
		return Evidence{}, fmt.Errorf("最终 Plugin archive 必须在目标原生 runner 构建：当前 %s/%s，目标 %s/%s", runtime.GOOS, runtime.GOARCH, target.GOOS, target.GOARCH)
	}
	outputRoot := options.OutputRoot
	if strings.TrimSpace(outputRoot) == "" {
		if options.Pack {
			outputRoot = filepath.Join(root, "release", "plugin")
		} else if options.TestMarker {
			outputRoot = filepath.Join(root, "tests", ".tmp", "plugin-stage-go-test")
		} else {
			outputRoot = filepath.Join(root, "tests", ".tmp", "plugin-stage")
		}
	}
	outputRoot, err = filepath.Abs(outputRoot)
	if err != nil {
		return Evidence{}, err
	}
	if err := validateOutputRoot(outputRoot); err != nil {
		return Evidence{}, err
	}
	if err := os.MkdirAll(outputRoot, 0o755); err != nil {
		return Evidence{}, err
	}
	workTargetRoot, err := os.MkdirTemp(outputRoot, "."+target.ID+"-")
	if err != nil {
		return Evidence{}, err
	}
	workPending := true
	defer func() {
		if workPending {
			_ = os.RemoveAll(workTargetRoot)
		}
	}()
	if err := writeOwnershipMarker(workTargetRoot, target.ID); err != nil {
		return Evidence{}, err
	}
	pluginName, marketplaceName, displayName, skillName := identity(options.TestMarker)
	marketplaceRoot := filepath.Join(workTargetRoot, marketplaceName)
	pluginStage := filepath.Join(marketplaceRoot, "plugins", pluginName)
	if err := os.MkdirAll(filepath.Join(pluginStage, "bin"), 0o755); err != nil {
		return Evidence{}, err
	}
	if err := copyPluginSource(filepath.Join(root, "plugins", ProductionPluginName), pluginStage); err != nil {
		return Evidence{}, err
	}
	for _, legalFile := range []string{"LICENSE", "NOTICE"} {
		if err := copyFile(filepath.Join(root, legalFile), filepath.Join(pluginStage, legalFile), 0o644); err != nil {
			return Evidence{}, err
		}
	}
	if err := os.MkdirAll(filepath.Join(marketplaceRoot, ".agents", "plugins"), 0o755); err != nil {
		return Evidence{}, err
	}
	if err := rewriteStageMetadata(root, marketplaceRoot, pluginStage, pluginName, marketplaceName, displayName, skillName, options.TestMarker, target); err != nil {
		return Evidence{}, err
	}
	binaryPath := filepath.Join(pluginStage, "bin", target.BinaryName)
	if err := buildBinary(ctx, root, target, binaryPath); err != nil {
		return Evidence{}, err
	}
	if target.GOOS != "windows" {
		_ = os.Chmod(binaryPath, 0o755)
	}
	licenses, err := collectLicenseFiles(ctx, root, pluginStage)
	if err != nil {
		return Evidence{}, err
	}
	binary, err := inspectBinary(binaryPath, pluginStage)
	if err != nil {
		return Evidence{}, err
	}
	if binary.Format != target.Format || binary.Architecture != target.Arch {
		return Evidence{}, fmt.Errorf("Go binary 目标格式错误：%s/%s，预期 %s/%s", binary.Format, binary.Architecture, target.Format, target.Arch)
	}
	if err := writeInstallGuide(filepath.Join(marketplaceRoot, "INSTALL.md"), pluginName, marketplaceName, target); err != nil {
		return Evidence{}, err
	}
	count, files, err := findNodeArtifacts(pluginStage)
	if err != nil {
		return Evidence{}, err
	}
	if count != 0 {
		return Evidence{}, fmt.Errorf("stage 含 Node/JavaScript 产物：%s", strings.Join(files, ", "))
	}
	if err := validateStage(pluginStage, marketplaceRoot, pluginName, marketplaceName, skillName, displayName, static.Version, options.TestMarker, target); err != nil {
		return Evidence{}, err
	}
	runtimeStatus, runtimeReason := "NOT_TESTED", fmt.Sprintf("当前宿主 %s/%s 无法执行 %s/%s", runtime.GOOS, runtime.GOARCH, target.GOOS, target.GOARCH)
	mcpHandshake, mcpReason := "NOT_TESTED", runtimeReason
	if runtime.GOOS == target.GOOS && runtime.GOARCH == target.GOARCH {
		command := exec.CommandContext(ctx, binaryPath, "--version", "--json")
		command.Dir = pluginStage
		output, runErr := command.CombinedOutput()
		if runErr != nil {
			return Evidence{}, fmt.Errorf("staged drag --version 失败: %w: %s", runErr, strings.TrimSpace(string(output)))
		}
		var version map[string]any
		if json.Unmarshal(output, &version) != nil || version["name"] != ProductionPluginName || version["version"] != static.Version {
			return Evidence{}, fmt.Errorf("staged drag --version 输出无效：%s", strings.TrimSpace(string(output)))
		}
		runtimeStatus, runtimeReason = "PASS", ""
		if options.TestMarker {
			if err := smokeMCP(ctx, pluginStage, workTargetRoot, pluginName, skillName); err != nil {
				return Evidence{}, err
			}
			mcpHandshake, mcpReason = "PASS", ""
		} else {
			mcpReason = "正式身份不在本机启动 MCP；使用隔离的 go-test stage 完成运行时验收"
		}
	}
	finalTargetRoot := filepath.Join(outputRoot, target.ID)
	finalMarketplaceRoot := filepath.Join(finalTargetRoot, marketplaceName)
	evidence := Evidence{Status: "PASS", Mode: map[bool]string{true: "pack", false: "stage-only"}[options.Pack], Target: target.ID, PluginName: pluginName, MarketplaceName: marketplaceName, MCPServerName: pluginName, SkillName: skillName, Version: static.Version, StageRoot: finalMarketplaceRoot, Binary: binary, RuntimeExecution: runtimeStatus, RuntimeReason: runtimeReason, MCPHandshake: mcpHandshake, MCPReason: mcpReason, LicenseFileCount: licenses.FileCount, SourceManifestSHA256: sourceHash, DependencyManifestSHA256: licenses.ManifestSHA256, LocalPatchSHA256: licenses.LocalPatchSHA256, NodeArtifactCount: count, TestMarkerIsolated: options.TestMarker, ArchiveValidation: "NOT_APPLICABLE"}
	if options.Pack {
		archiveName := fmt.Sprintf("%s-%s-%s.zip", marketplaceName, static.Version, target.ID)
		archivePath := filepath.Join(workTargetRoot, archiveName)
		if err := createZip(marketplaceRoot, archivePath, pluginName, target.BinaryName, static.Version); err != nil {
			return Evidence{}, err
		}
		info, statErr := os.Stat(archivePath)
		if statErr != nil {
			return Evidence{}, statErr
		}
		archiveHash, hashErr := hashFileChecked(archivePath)
		if hashErr != nil {
			return Evidence{}, hashErr
		}
		evidence.Archive = &Archive{Path: filepath.Join(finalTargetRoot, archiveName), SHA256: archiveHash, SizeBytes: info.Size()}
		evidence.ArchiveValidation = "PASS"
	}
	if err := writeJSON(filepath.Join(workTargetRoot, "stage-evidence.json"), evidence); err != nil {
		return Evidence{}, err
	}
	if err := replaceGeneratedTarget(outputRoot, workTargetRoot, finalTargetRoot, target.ID); err != nil {
		return Evidence{}, err
	}
	workPending = false
	return evidence, nil
}

type goModule struct {
	Path, Version, Dir string
	Main               bool
	Replace            *goModule
}

type goListPackage struct {
	Module *goModule
}

type dependencyNotice struct {
	Path              string   `json:"path"`
	Version           string   `json:"version,omitempty"`
	Replacement       string   `json:"replacement,omitempty"`
	LicenseFiles      []string `json:"licenseFiles"`
	LocalSourceSHA256 string   `json:"localSourceSha256,omitempty"`
}

type licenseEvidence struct {
	FileCount        int
	ManifestSHA256   string
	LocalPatchSHA256 string
}

func collectLicenseFiles(ctx context.Context, projectRoot, pluginRoot string) (licenseEvidence, error) {
	command := exec.CommandContext(ctx, "go", "list", "-deps", "-json", "./go/cmd/drag")
	command.Dir = projectRoot
	output, err := command.CombinedOutput()
	if err != nil {
		return licenseEvidence{}, fmt.Errorf("读取 Go runtime dependency 清单失败: %w: %s", err, strings.TrimSpace(string(output)))
	}
	destination := filepath.Join(pluginRoot, "THIRD_PARTY_NOTICES")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return licenseEvidence{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	modules := map[string]goModule{}
	for {
		var pkg goListPackage
		if err := decoder.Decode(&pkg); err != nil {
			if err == io.EOF {
				break
			}
			return licenseEvidence{}, err
		}
		if pkg.Module != nil && !pkg.Module.Main && pkg.Module.Path != "" {
			modules[pkg.Module.Path] = *pkg.Module
		}
	}
	paths := make([]string, 0, len(modules))
	for modulePath := range modules {
		paths = append(paths, modulePath)
	}
	sort.Strings(paths)
	count := 0
	foundRequired := map[string]bool{}
	notices := make([]dependencyNotice, 0, len(paths))
	localPatchHash := ""
	for _, modulePath := range paths {
		module := modules[modulePath]
		moduleDir := module.Dir
		replacement := ""
		if module.Replace != nil {
			replacement = module.Replace.Path
			if module.Replace.Dir != "" {
				moduleDir = module.Replace.Dir
			}
		}
		if moduleDir == "" {
			continue
		}
		entries, readErr := os.ReadDir(moduleDir)
		if readErr != nil {
			return licenseEvidence{}, fmt.Errorf("读取 dependency 目录失败 %s: %w", module.Path, readErr)
		}
		licenseNames := []string{}
		for _, entry := range entries {
			lower := strings.ToLower(entry.Name())
			if !entry.IsDir() && (lower == "license" || strings.HasPrefix(lower, "license.") || strings.HasPrefix(lower, "license-") || lower == "copying" || strings.HasPrefix(lower, "copying.") || lower == "notice" || strings.HasPrefix(lower, "notice.")) {
				licenseNames = append(licenseNames, entry.Name())
			}
		}
		sort.Strings(licenseNames)
		copied := []string{}
		for _, licenseName := range licenseNames {
			prefix := regexp.MustCompile(`[^a-zA-Z0-9._-]+`).ReplaceAllString(module.Path+"@"+module.Version, "_")
			outputName := prefix + "-" + licenseName
			if err := copyFile(filepath.Join(moduleDir, licenseName), filepath.Join(destination, outputName), 0o644); err != nil {
				return licenseEvidence{}, err
			}
			copied = append(copied, outputName)
			count++
		}
		if len(copied) > 0 {
			foundRequired[module.Path] = true
		}
		notice := dependencyNotice{Path: module.Path, Version: module.Version, Replacement: replacement, LicenseFiles: copied}
		if module.Path == "github.com/nkiri/xls" && module.Replace != nil {
			localPatchHash, err = hashTree(moduleDir)
			if err != nil {
				return licenseEvidence{}, fmt.Errorf("计算本地 BIFF fork 哈希失败: %w", err)
			}
			notice.LocalSourceSHA256 = localPatchHash
		}
		notices = append(notices, notice)
	}
	goRootCommand := exec.CommandContext(ctx, "go", "env", "GOROOT")
	goRootCommand.Dir = projectRoot
	goRootRaw, err := goRootCommand.Output()
	if err != nil {
		return licenseEvidence{}, fmt.Errorf("读取 GOROOT 失败: %w", err)
	}
	goLicense := filepath.Join(strings.TrimSpace(string(goRootRaw)), "LICENSE")
	if err := copyFile(goLicense, filepath.Join(destination, "Go-LICENSE.txt"), 0o644); err != nil {
		return licenseEvidence{}, fmt.Errorf("复制 Go LICENSE 失败: %w", err)
	}
	count++
	required := []string{"github.com/giraffesyo/pdf", "github.com/modelcontextprotocol/go-sdk", "github.com/nkiri/xls", "github.com/xuri/excelize/v2", "golang.org/x/net", "golang.org/x/sys", "golang.org/x/text", "modernc.org/sqlite"}
	missing := []string{}
	for _, modulePath := range required {
		if !foundRequired[modulePath] {
			missing = append(missing, modulePath)
		}
	}
	if len(missing) > 0 {
		return licenseEvidence{}, fmt.Errorf("直接 runtime dependency 缺少可分发 LICENSE：%s", strings.Join(missing, ", "))
	}
	manifestPath := filepath.Join(destination, "runtime-dependencies.json")
	if err := writeJSON(manifestPath, map[string]any{"schemaVersion": 1, "dependencies": notices, "goRuntimeLicense": "Go-LICENSE.txt"}); err != nil {
		return licenseEvidence{}, err
	}
	manifestHash, err := hashFileChecked(manifestPath)
	if err != nil {
		return licenseEvidence{}, err
	}
	return licenseEvidence{FileCount: count, ManifestSHA256: manifestHash, LocalPatchSHA256: localPatchHash}, nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func smokeMCP(parent context.Context, pluginRoot, targetRoot, pluginName, skillName string) error {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	mcpConfig, err := readJSONObject(filepath.Join(pluginRoot, ".mcp.json"))
	if err != nil {
		return err
	}
	servers, err := nestedObject(mcpConfig, "mcpServers")
	if err != nil {
		return err
	}
	server, ok := servers[pluginName].(map[string]any)
	if !ok || len(servers) != 1 {
		return fmt.Errorf("staged .mcp.json 缺少唯一 server：%s", pluginName)
	}
	commandRelative := filepath.FromSlash(strings.TrimPrefix(stringValue(server, "command"), "./"))
	binaryPath := filepath.Join(pluginRoot, commandRelative)
	if relative, relErr := filepath.Rel(pluginRoot, binaryPath); relErr != nil || strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
		return fmt.Errorf("staged MCP command 越界：%s", binaryPath)
	}
	rawArgs, _ := server["args"].([]any)
	args := make([]string, 0, len(rawArgs))
	for _, value := range rawArgs {
		argument, ok := value.(string)
		if !ok {
			return errors.New("staged MCP args 含非字符串")
		}
		args = append(args, argument)
	}
	stateRoot := filepath.Join(targetRoot, ".mcp-smoke-state")
	defer os.RemoveAll(stateRoot)
	sourceRoot := filepath.Join(stateRoot, "source")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		return err
	}
	config := core.CreateDefaultConfig()
	config.Sources = []core.Source{}
	config.Indexing.AutomaticScan = false
	config.Indexing.Concurrency = 1
	configStore := core.NewConfigStore(filepath.Join(stateRoot, "config"), filepath.Join(stateRoot, "data"))
	if _, err := configStore.SaveSnapshot(config); err != nil {
		return fmt.Errorf("准备 staged MCP 隔离配置失败: %w", err)
	}
	command := exec.Command(binaryPath, args...)
	command.Dir = pluginRoot
	command.Env = append([]string{}, os.Environ()...)
	if environment, ok := server["env"].(map[string]any); ok {
		keys := make([]string, 0, len(environment))
		for key := range environment {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, ok := environment[key].(string)
			if !ok {
				return fmt.Errorf("staged MCP env %s 不是字符串", key)
			}
			command.Env = append(command.Env, key+"="+value)
		}
	}
	// Only state paths are overridden for the smoke; command, args, identity,
	// resource scheme, Skill name and Plugin root come from staged .mcp.json.
	command.Env = append(command.Env, "DESIGN_RAG_CONFIG_DIR="+filepath.Join(stateRoot, "config"), "DESIGN_RAG_DATA_DIR="+filepath.Join(stateRoot, "data"))
	stderr := &bytes.Buffer{}
	command.Stderr = stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "drag-plugin-stage-smoke", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		return fmt.Errorf("staged MCP 握手失败: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	defer session.Close()
	resources, err := session.ListResources(ctx, nil)
	if err != nil {
		return fmt.Errorf("staged MCP resources 调用失败：%w", err)
	}
	if len(resources.Resources) != 3 {
		return fmt.Errorf("staged MCP resources 契约失败：count=%d", len(resources.Resources))
	}
	for _, resource := range resources.Resources {
		if !strings.HasPrefix(resource.URI, pluginName+"://") {
			return fmt.Errorf("staged MCP resource scheme 错误：%s", resource.URI)
		}
		read, readErr := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: resource.URI})
		if readErr != nil || len(read.Contents) != 1 || strings.TrimSpace(read.Contents[0].Text) == "" {
			return fmt.Errorf("staged MCP resource 回读失败：%s: %v", resource.URI, readErr)
		}
		if resource.URI == pluginName+"://skill/game-design-rag" && !strings.Contains(read.Contents[0].Text, "name: "+skillName) {
			return fmt.Errorf("staged MCP Skill resource 身份错误：%s", resource.URI)
		}
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("staged MCP tools 调用失败：%w", err)
	}
	if len(tools.Tools) != 13 {
		return fmt.Errorf("staged MCP tools 契约失败：count=%d", len(tools.Tools))
	}
	expectedTools := []string{"drag_cache_clear", "drag_index_pause", "drag_index_resume", "drag_index_status", "drag_index_update", "drag_list_versions", "drag_read_citation", "drag_retrieve", "drag_search", "drag_source_add", "drag_source_remove", "drag_source_update", "drag_sources"}
	actualTools := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		actualTools = append(actualTools, tool.Name)
	}
	sort.Strings(actualTools)
	if strings.Join(actualTools, "\x00") != strings.Join(expectedTools, "\x00") {
		return fmt.Errorf("staged MCP tool names 错误：%v", actualTools)
	}
	call := func(name string, arguments map[string]any, wantError bool) error {
		result, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
		if callErr != nil {
			return fmt.Errorf("%s transport 失败: %w", name, callErr)
		}
		if wantError {
			if result == nil || !result.IsError {
				return fmt.Errorf("%s 应返回可控工具错误", name)
			}
			return nil
		}
		if result == nil || result.IsError || result.StructuredContent == nil {
			raw, _ := json.Marshal(result)
			return fmt.Errorf("%s 失败：isError=%v result=%s stderr=%s", name, result != nil && result.IsError, raw, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
	for _, invocation := range []struct {
		name      string
		arguments map[string]any
		wantError bool
	}{
		{"drag_search", map[string]any{"query": "smoke-empty", "limit": 3}, false},
		{"drag_retrieve", map[string]any{"query": "smoke-empty", "maxDocuments": 3, "maxChars": 2000}, false},
		{"drag_read_citation", map[string]any{"citationId": "invalid"}, true},
		{"drag_list_versions", map[string]any{"familyKey": "missing"}, false},
		{"drag_sources", map[string]any{}, false},
		{"drag_source_add", map[string]any{"id": "smoke-disabled", "label": "Smoke Disabled", "kind": "design", "rootPath": sourceRoot, "enabled": false}, false},
	} {
		if err := call(invocation.name, invocation.arguments, invocation.wantError); err != nil {
			return err
		}
	}
	waitForBackground := func() error {
		lastStatus := ""
		for attempt := 0; attempt < 500; attempt++ {
			result, statusErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "drag_index_status", Arguments: map[string]any{}})
			if statusErr != nil || result == nil || result.IsError || result.StructuredContent == nil {
				return fmt.Errorf("drag_index_status 失败: %v", statusErr)
			}
			raw, _ := json.Marshal(result.StructuredContent)
			lastStatus = string(raw)
			if !bytes.Contains(raw, []byte(`"backgroundJobActive":true`)) {
				return nil
			}
			time.Sleep(10 * time.Millisecond)
		}
		return fmt.Errorf("staged MCP 后台索引在 smoke 时限内未结束：%s", lastStatus)
	}
	if err := waitForBackground(); err != nil {
		return err
	}
	for _, invocation := range []struct {
		name      string
		arguments map[string]any
	}{
		{"drag_source_update", map[string]any{"id": "smoke-disabled", "label": "Smoke Updated", "enabled": false}},
		{"drag_source_remove", map[string]any{"id": "smoke-disabled"}},
		{"drag_index_update", map[string]any{}},
		{"drag_index_pause", map[string]any{}},
		{"drag_index_resume", map[string]any{}},
	} {
		if err := call(invocation.name, invocation.arguments, false); err != nil {
			return err
		}
	}
	if err := waitForBackground(); err != nil {
		return err
	}
	if err := call("drag_cache_clear", map[string]any{}, false); err != nil {
		return err
	}
	return nil
}

type ownershipMarker struct {
	Owner  string `json:"owner"`
	Target string `json:"target"`
	Schema int    `json:"schema"`
}

func validateOutputRoot(outputRoot string) error {
	clean := filepath.Clean(outputRoot)
	volume := filepath.VolumeName(clean)
	if clean == string(filepath.Separator) || (volume != "" && strings.EqualFold(clean, volume+string(filepath.Separator))) {
		return fmt.Errorf("拒绝使用文件系统根目录作为 Plugin 输出：%s", outputRoot)
	}
	if info, err := os.Lstat(clean); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("拒绝使用 symlink 作为 Plugin 输出目录：%s", outputRoot)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func writeOwnershipMarker(targetRoot, targetID string) error {
	return writeJSON(filepath.Join(targetRoot, ownerMarkerFile), ownershipMarker{Owner: "design-rag-pluginpack", Target: targetID, Schema: 1})
}

func verifyOwnedTarget(outputRoot, targetRoot, targetID string) error {
	relative, err := filepath.Rel(outputRoot, targetRoot)
	if err != nil || relative != targetID || relative == "." || strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
		return fmt.Errorf("拒绝替换未验证的生成目录：%s", targetRoot)
	}
	info, err := os.Lstat(targetRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("拒绝替换非普通目录：%s", targetRoot)
	}
	var marker ownershipMarker
	raw, err := os.ReadFile(filepath.Join(targetRoot, ownerMarkerFile))
	if err != nil || json.Unmarshal(raw, &marker) != nil || marker.Owner != "design-rag-pluginpack" || marker.Target != targetID || marker.Schema != 1 {
		return fmt.Errorf("拒绝清理无有效 ownership marker 的目录：%s", targetRoot)
	}
	return nil
}

func replaceGeneratedTarget(outputRoot, workTargetRoot, finalTargetRoot, targetID string) error {
	if err := verifyOwnedTarget(outputRoot, finalTargetRoot, targetID); err != nil {
		return err
	}
	if _, err := os.Stat(finalTargetRoot); err == nil {
		if err := os.RemoveAll(finalTargetRoot); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(workTargetRoot, finalTargetRoot); err != nil {
		return fmt.Errorf("原子发布 Plugin stage 失败: %w", err)
	}
	return nil
}

func validatePluginSourceFiles(source string) error {
	allowed := map[string]bool{}
	for _, relative := range pluginSourceFiles {
		allowed[filepath.ToSlash(relative)] = true
	}
	seen := map[string]bool{}
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("Plugin 源不允许 symlink：%s", path)
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if !allowed[relative] {
			return fmt.Errorf("Plugin 源含非 allowlist 文件：%s", relative)
		}
		seen[relative] = true
		return nil
	})
	if err != nil {
		return err
	}
	missing := []string{}
	for _, relative := range pluginSourceFiles {
		if !seen[filepath.ToSlash(relative)] {
			missing = append(missing, relative)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("Plugin 源缺少 allowlist 文件：%s", strings.Join(missing, ", "))
	}
	return nil
}

func copyPluginSource(source, destination string) error {
	if err := validatePluginSourceFiles(source); err != nil {
		return err
	}
	for _, relative := range pluginSourceFiles {
		target := filepath.Join(destination, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := copyFile(filepath.Join(source, filepath.FromSlash(relative)), target, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func rewriteStageMetadata(projectRoot, marketplaceRoot, pluginStage, pluginName, marketplaceName, displayName, skillName string, testMarker bool, target Target) error {
	manifestPath := filepath.Join(pluginStage, ".codex-plugin", "plugin.json")
	manifest, err := readJSONObject(manifestPath)
	if err != nil {
		return err
	}
	manifest["name"] = pluginName
	interfaceObject, _ := nestedObject(manifest, "interface")
	interfaceObject["displayName"] = displayName
	if err := writeJSON(manifestPath, manifest); err != nil {
		return err
	}
	mcpPath := filepath.Join(pluginStage, ".mcp.json")
	mcpConfig, err := readJSONObject(mcpPath)
	if err != nil {
		return err
	}
	servers, _ := nestedObject(mcpConfig, "mcpServers")
	server, _ := servers[ProductionPluginName].(map[string]any)
	server["command"] = "./bin/" + target.BinaryName
	server["args"] = []any{"mcp"}
	if testMarker {
		server["env"] = map[string]any{
			"DESIGN_RAG_MCP_NAME":        pluginName,
			"DESIGN_RAG_RESOURCE_SCHEME": pluginName,
			"DESIGN_RAG_SKILL_NAME":      skillName,
			"DESIGN_RAG_PLUGIN_ROOT":     ".",
			"DESIGN_RAG_STATE_NAMESPACE": pluginName,
		}
	}
	mcpConfig["mcpServers"] = map[string]any{pluginName: server}
	if err := writeJSON(mcpPath, mcpConfig); err != nil {
		return err
	}
	if testMarker {
		oldSkill := filepath.Join(pluginStage, "skills", "game-design-rag")
		newSkill := filepath.Join(pluginStage, "skills", skillName)
		if err := os.Rename(oldSkill, newSkill); err != nil {
			return err
		}
		if err := replaceTextTree(newSkill, []string{".md", ".yaml", ".yml"}, map[string]string{"game-design-rag": skillName, "design-rag": pluginName, "DRAG 游戏策划知识库": displayName}); err != nil {
			return err
		}
	}
	marketplace, err := readJSONObject(filepath.Join(projectRoot, "packaging", "design-rag-marketplace.json"))
	if err != nil {
		return err
	}
	marketplace["name"] = marketplaceName
	marketplaceInterface, _ := nestedObject(marketplace, "interface")
	if testMarker {
		marketplaceInterface["displayName"] = "DRAG Local Marketplace (Go Test)"
	}
	plugins, _ := marketplace["plugins"].([]any)
	entry, _ := plugins[0].(map[string]any)
	entry["name"] = pluginName
	source, _ := nestedObject(entry, "source")
	source["path"] = "./plugins/" + pluginName
	return writeJSON(filepath.Join(marketplaceRoot, ".agents", "plugins", "marketplace.json"), marketplace)
}

func replaceTextTree(root string, extensions []string, replacements map[string]string) error {
	allowed := map[string]bool{}
	for _, extension := range extensions {
		allowed[extension] = true
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !allowed[strings.ToLower(filepath.Ext(path))] {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(raw)
		keys := make([]string, 0, len(replacements))
		for old := range replacements {
			keys = append(keys, old)
		}
		sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
		placeholders := make([]string, len(keys))
		for index, old := range keys {
			placeholders[index] = fmt.Sprintf("__DRAG_STAGE_REPLACEMENT_%d__", index)
			text = strings.ReplaceAll(text, old, placeholders[index])
		}
		for index, old := range keys {
			text = strings.ReplaceAll(text, placeholders[index], replacements[old])
		}
		return os.WriteFile(path, []byte(text), 0o644)
	})
}

func buildBinary(ctx context.Context, projectRoot string, target Target, destination string) error {
	command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", destination, "./go/cmd/drag")
	command.Dir = projectRoot
	command.Env = append(os.Environ(), "GOOS="+target.GOOS, "GOARCH="+target.GOARCH, "CGO_ENABLED=0")
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("构建 %s Go Plugin runtime 失败: %w: %s", target.ID, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func inspectBinary(path, relativeRoot string) (BinaryEvidence, error) {
	info, err := os.Stat(path)
	if err != nil {
		return BinaryEvidence{}, err
	}
	hash, err := hashFileChecked(path)
	if err != nil {
		return BinaryEvidence{}, err
	}
	evidence := BinaryEvidence{Path: filepath.ToSlash(mustRelative(relativeRoot, path)), SHA256: hash, SizeBytes: info.Size(), Format: "unknown", Architecture: "unknown"}
	if file, openErr := pe.Open(path); openErr == nil {
		defer file.Close()
		evidence.Format = "pe"
		switch file.FileHeader.Machine {
		case pe.IMAGE_FILE_MACHINE_AMD64:
			evidence.Architecture = "x64"
		case pe.IMAGE_FILE_MACHINE_ARM64:
			evidence.Architecture = "arm64"
		}
		return evidence, nil
	}
	if file, openErr := macho.Open(path); openErr == nil {
		defer file.Close()
		evidence.Format = "mach-o"
		switch file.Cpu {
		case macho.CpuArm64:
			evidence.Architecture = "arm64"
		case macho.CpuAmd64:
			evidence.Architecture = "x64"
		}
	}
	return evidence, nil
}

func validateStage(pluginRoot, marketplaceRoot, pluginName, marketplaceName, skillName, displayName, expectedVersion string, testMarker bool, target Target) error {
	manifest, err := readJSONObject(filepath.Join(pluginRoot, ".codex-plugin", "plugin.json"))
	if err != nil || stringValue(manifest, "name") != pluginName {
		return fmt.Errorf("stage manifest identity 无效: %w", err)
	}
	if err := validateDistributionVersion(stringValue(manifest, "version"), expectedVersion); err != nil {
		return fmt.Errorf("stage manifest 版本无效: %w", err)
	}
	interfaceObject, _ := nestedObject(manifest, "interface")
	if stringValue(interfaceObject, "displayName") != displayName {
		return errors.New("stage displayName 无效")
	}
	if stringValue(interfaceObject, "websiteURL") != productionWebsite {
		return errors.New("stage websiteURL 无效")
	}
	mcpConfig, err := readJSONObject(filepath.Join(pluginRoot, ".mcp.json"))
	if err != nil {
		return err
	}
	servers, _ := nestedObject(mcpConfig, "mcpServers")
	server, ok := servers[pluginName].(map[string]any)
	args, _ := server["args"].([]any)
	if !ok || len(servers) != 1 || stringValue(server, "command") != "./bin/"+target.BinaryName || len(args) != 1 || args[0] != "mcp" || stringValue(server, "cwd") != "." {
		return errors.New("stage MCP 未直接调用唯一 Go runtime")
	}
	_, hasEnvironment := server["env"]
	if testMarker != hasEnvironment {
		return errors.New("go-test 隔离环境变量状态与 stage 模式不一致")
	}
	skillPath := filepath.Join(pluginRoot, "skills", skillName, "SKILL.md")
	if _, err := os.Stat(skillPath); err != nil {
		return fmt.Errorf("stage Skill 缺失: %w", err)
	}
	for _, legalFile := range []string{"LICENSE", "NOTICE"} {
		info, statErr := os.Stat(filepath.Join(pluginRoot, legalFile))
		if statErr != nil {
			return fmt.Errorf("stage 缺少 %s: %w", legalFile, statErr)
		}
		if info.IsDir() {
			return fmt.Errorf("stage 中的 %s 不能是目录", legalFile)
		}
	}
	skillText := string(mustRead(skillPath))
	if !regexp.MustCompile(`(?m)^name:\s*`+regexp.QuoteMeta(skillName)+`\s*$`).MatchString(skillText) || strings.Contains(skillText, "go-test-go-test") {
		return fmt.Errorf("stage Skill frontmatter 身份无效：%s", skillName)
	}
	marketplace, err := readJSONObject(filepath.Join(marketplaceRoot, ".agents", "plugins", "marketplace.json"))
	if err != nil || stringValue(marketplace, "name") != marketplaceName {
		return errors.New("stage marketplace identity 无效")
	}
	marketplaceInterface, _ := nestedObject(marketplace, "interface")
	expectedMarketplaceDisplay := "DRAG Local Marketplace"
	if testMarker {
		expectedMarketplaceDisplay += " (Go Test)"
	}
	if stringValue(marketplaceInterface, "displayName") != expectedMarketplaceDisplay {
		return errors.New("stage marketplace displayName 无效")
	}
	if !testMarker {
		identityFiles := []string{
			filepath.Join(pluginRoot, ".mcp.json"),
			filepath.Join(pluginRoot, ".codex-plugin", "plugin.json"),
			filepath.Join(pluginRoot, "skills", skillName, "SKILL.md"),
			filepath.Join(pluginRoot, "skills", skillName, "agents", "openai.yaml"),
			filepath.Join(marketplaceRoot, ".agents", "plugins", "marketplace.json"),
		}
		for _, path := range identityFiles {
			if bytes.Contains(mustRead(path), []byte("go-test")) {
				return fmt.Errorf("正式 stage 残留 go-test 身份标记：%s", path)
			}
		}
	}
	return nil
}

func findNodeArtifacts(root string) (int, []string, error) {
	files := []string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative := filepath.ToSlash(mustRelative(root, path))
		name := strings.ToLower(entry.Name())
		parts := strings.Split(strings.ToLower(relative), "/")
		blockedDirectory := false
		for _, part := range parts {
			if part == "node_modules" || part == "runtime" || part == "runtime-package" {
				blockedDirectory = true
				break
			}
		}
		extension := strings.ToLower(filepath.Ext(name))
		blockedFile := name == "node" || name == "node.exe" || name == "drag.cmd" || name == "package.json" || name == "package-lock.json" || name == "npm-shrinkwrap.json" || name == "yarn.lock" || name == "pnpm-lock.yaml" || name == "bun.lock" || name == "bun.lockb" || extension == ".js" || extension == ".mjs" || extension == ".cjs" || extension == ".jsx" || extension == ".ts" || extension == ".tsx" || extension == ".node"
		if blockedDirectory || (!entry.IsDir() && blockedFile) {
			files = append(files, relative)
			if entry.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
	sort.Strings(files)
	return len(files), files, err
}

func writeInstallGuide(path, pluginName, marketplaceName string, target Target) error {
	content := fmt.Sprintf("# DRAG 游戏策划知识库 Plugin\n\n1. 将 `%s` 目录保持完整。\n2. 运行 `codex plugin marketplace add <该目录绝对路径>`。\n3. 运行 `codex plugin add %s@%s`。\n4. 重启 Codex host/Desktop 以刷新本地 Plugin cache，再新建任务。\n\nCLI：`./plugins/%s/bin/%s --help`。MCP 与 CLI 均由同一个纯 Go binary 提供。\n", marketplaceName, pluginName, marketplaceName, pluginName, target.BinaryName)
	return os.WriteFile(path, []byte(content), 0o644)
}

func createZip(root, destination, pluginName, binaryName, expectedVersion string) error {
	if _, err := os.Stat(destination); err == nil {
		return fmt.Errorf("拒绝覆盖已存在的 archive：%s", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".plugin-*.zip.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	archive := zip.NewWriter(temporary)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		relative := filepath.ToSlash(mustRelative(filepath.Dir(root), path))
		info, err := entry.Info()
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = relative
		header.Method = zip.Deflate
		header.Modified = time.Unix(0, 0).UTC()
		if entry.Name() == binaryName {
			header.SetMode(0o755)
		} else {
			header.SetMode(0o644)
		}
		writer, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	closeArchive := archive.Close()
	syncErr := temporary.Sync()
	closeOutput := temporary.Close()
	if err != nil {
		return err
	}
	if closeArchive != nil {
		return closeArchive
	}
	if syncErr != nil {
		return syncErr
	}
	if closeOutput != nil {
		return closeOutput
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("原子发布 archive 失败: %w", err)
	}
	if err := validateZip(destination, filepath.Base(root), pluginName, binaryName, expectedVersion); err != nil {
		_ = os.Remove(destination)
		return err
	}
	return nil
}

func validateZip(path, marketplaceName, pluginName, binaryName, expectedVersion string) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("重新打开 archive 失败: %w", err)
	}
	defer reader.Close()
	manifestEntry := marketplaceName + "/plugins/" + pluginName + "/.codex-plugin/plugin.json"
	requiredEntries := map[string]bool{
		manifestEntry: false,
		marketplaceName + "/plugins/" + pluginName + "/.mcp.json":         false,
		marketplaceName + "/plugins/" + pluginName + "/bin/" + binaryName: false,
		marketplaceName + "/plugins/" + pluginName + "/LICENSE":           false,
		marketplaceName + "/plugins/" + pluginName + "/NOTICE":            false,
	}
	for _, file := range reader.File {
		name := filepath.ToSlash(file.Name)
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(name)))
		if clean != name || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") || !strings.HasPrefix(name, marketplaceName+"/") {
			return fmt.Errorf("archive 含不安全 entry：%s", name)
		}
		lower := strings.ToLower(name)
		extension := strings.ToLower(filepath.Ext(lower))
		base := strings.ToLower(filepath.Base(lower))
		if strings.Contains(lower, "/node_modules/") || strings.Contains(lower, "/runtime/") || strings.Contains(lower, "/runtime-package/") || base == "node" || base == "node.exe" || base == "drag.cmd" || base == "package.json" || base == "package-lock.json" || extension == ".js" || extension == ".mjs" || extension == ".cjs" || extension == ".jsx" || extension == ".ts" || extension == ".tsx" || extension == ".node" {
			return fmt.Errorf("archive 含 Node/JavaScript entry：%s", name)
		}
		if _, required := requiredEntries[name]; required {
			requiredEntries[name] = true
		}
		if name == manifestEntry {
			input, openErr := file.Open()
			if openErr != nil {
				return fmt.Errorf("读取 archive manifest 失败: %w", openErr)
			}
			raw, readErr := io.ReadAll(io.LimitReader(input, 1<<20))
			closeErr := input.Close()
			if readErr != nil {
				return fmt.Errorf("读取 archive manifest 失败: %w", readErr)
			}
			if closeErr != nil {
				return closeErr
			}
			var manifest map[string]any
			if err := json.Unmarshal(raw, &manifest); err != nil {
				return fmt.Errorf("解析 archive manifest 失败: %w", err)
			}
			if err := validateDistributionVersion(stringValue(manifest, "version"), expectedVersion); err != nil {
				return fmt.Errorf("archive manifest 版本无效: %w", err)
			}
		}
		if strings.HasSuffix(name, "/bin/"+binaryName) && file.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("archive Go binary 缺少可执行 mode：%s", name)
		}
	}
	for entry, found := range requiredEntries {
		if !found {
			return fmt.Errorf("archive 缺少必需 entry：%s", entry)
		}
	}
	return nil
}

func hashFileChecked(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func hashPaths(root string, relativePaths []string) (string, error) {
	paths := append([]string(nil), relativePaths...)
	sort.Strings(paths)
	hash := sha256.New()
	for _, relative := range paths {
		normalized := filepath.ToSlash(relative)
		_, _ = io.WriteString(hash, normalized)
		_, _ = hash.Write([]byte{0})
		file, err := os.Open(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func sourceManifestSHA256(root string) (string, error) {
	if err := validatePluginSourceFiles(root); err != nil {
		return "", err
	}
	return hashPaths(root, pluginSourceFiles)
}

func hashTree(root string) (string, error) {
	paths := []string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("哈希源树不允许 symlink：%s", path)
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return "", err
	}
	return hashPaths(root, paths)
}

func mustRelative(root, path string) string {
	value, _ := filepath.Rel(root, path)
	return value
}
