package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/tadazly/design-rag/go/core"
)

type arguments struct {
	command    string
	positional []string
	flags      map[string][]string
}

func parseArguments(values []string) (arguments, error) {
	result := arguments{command: "help", flags: map[string][]string{}}
	if len(values) > 0 {
		result.command = values[0]
		values = values[1:]
	}
	for index := 0; index < len(values); index++ {
		value := values[index]
		if !strings.HasPrefix(value, "--") {
			result.positional = append(result.positional, value)
			continue
		}
		keyValue := strings.SplitN(strings.TrimPrefix(value, "--"), "=", 2)
		if keyValue[0] == "" {
			return result, errors.New("存在空的命令参数")
		}
		flagValue := "true"
		if len(keyValue) == 2 {
			flagValue = keyValue[1]
		} else if index+1 < len(values) && !strings.HasPrefix(values[index+1], "--") {
			flagValue = values[index+1]
			index++
		}
		result.flags[keyValue[0]] = append(result.flags[keyValue[0]], flagValue)
	}
	return result, nil
}

func (args arguments) has(name string) bool { _, ok := args.flags[name]; return ok }

func (args arguments) value(name string) string {
	values := args.flags[name]
	if len(values) == 0 || values[len(values)-1] == "true" {
		return ""
	}
	return values[len(values)-1]
}

func (args arguments) values(name string) []string {
	var result []string
	for _, value := range args.flags[name] {
		if value != "true" {
			result = append(result, value)
		}
	}
	return result
}

func (args arguments) required(name string) (string, error) {
	value := strings.TrimSpace(args.value(name))
	if value == "" {
		return "", fmt.Errorf("缺少 --%s", name)
	}
	return value, nil
}

func (args arguments) integer(name string) (int, error) {
	value := args.value(name)
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("--%s 必须是整数", name)
	}
	return parsed, nil
}

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func printHelp() {
	fmt.Fprint(os.Stdout, `drag · 游戏策划知识库 CLI（纯 Go）

用法：
  drag --version [--json]
  drag init [--json]
  drag doctor [--json]
  drag config [--json]
  drag sources list [--json]
  drag sources add --id <id> --label <名称> --kind <design|table> --path <目录> [--disabled] [--json]
  drag sources update <id> [--label <名称>] [--kind <design|table>] [--path <目录>] [--enable|--disable] [--json]
  drag sources remove <id> --yes [--json]
  drag index [--full] [--source <id> ...] [--json]
  drag search "轮盘抽奖" [--sort newest] [--limit 10] [--source <id> ...] [--json]
  drag retrieve "轮盘抽奖有哪些可复用" [--sort newest] [--max-documents 8] [--max-chunks-per-document 3] [--max-chars 24000] [--json]
  drag status [--json]
  drag versions --document-id <id> | --family-key <key> [--limit 20] [--json]
  drag citation <citation-id> [--revision 123] [--json]
  drag cache clear --yes [--json]
  drag mcp
`)
}

func sourceExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func progress(summary core.RunSummary) {
	fmt.Fprintf(os.Stderr, "\r%-9s %d/%d indexed · %d unchanged · %d failed", summary.Phase, summary.Indexed, summary.Discovered, summary.Unchanged, summary.Failed)
}

func handleSources(ctx context.Context, service *core.RuntimeService, args arguments) (any, error) {
	action := "list"
	if len(args.positional) > 0 {
		action = args.positional[0]
	}
	config, baseFingerprint := service.ConfigWithFingerprint()
	if action == "list" {
		status, err := service.Status()
		if err != nil {
			return nil, err
		}
		type sourceStatus struct {
			core.Source
			Exists           bool `json:"exists"`
			IndexedDocuments int  `json:"indexedDocuments"`
		}
		sources := make([]sourceStatus, 0, len(config.Sources))
		active := 0
		for _, source := range config.Sources {
			if source.Enabled {
				active++
			}
			sources = append(sources, sourceStatus{Source: source, Exists: sourceExists(source.RootPath), IndexedDocuments: status.SourceCounts[source.ID]})
		}
		return map[string]any{"sources": sources, "activeSourceCount": active}, nil
	}
	if action == "add" {
		id, err := args.required("id")
		if err != nil {
			return nil, err
		}
		kind, err := args.required("kind")
		if err != nil {
			return nil, err
		}
		path, err := args.required("path")
		if err != nil {
			return nil, err
		}
		label := strings.TrimSpace(args.value("label"))
		if label == "" {
			if kind == "design" {
				label = "策划案"
			} else {
				label = "配置表"
			}
		}
		source, err := core.CreateSourceConfig(id, label, kind, path, !args.has("disabled"))
		if err != nil {
			return nil, err
		}
		config.Sources = append(config.Sources, source)
		return service.ReconcileSourcesCAS(ctx, config, baseFingerprint.SHA256, true, progress)
	}
	if len(args.positional) < 2 || strings.TrimSpace(args.positional[1]) == "" {
		return nil, fmt.Errorf("sources %s 需要来源 id", action)
	}
	id := strings.TrimSpace(args.positional[1])
	index := -1
	for candidateIndex := range config.Sources {
		if config.Sources[candidateIndex].ID == id {
			index = candidateIndex
			break
		}
	}
	if index < 0 {
		return nil, fmt.Errorf("资料源不存在：%s", id)
	}
	switch action {
	case "update":
		if args.has("enable") && args.has("disable") {
			return nil, errors.New("--enable 与 --disable 不能同时使用")
		}
		target := config.Sources[index]
		if label := strings.TrimSpace(args.value("label")); label != "" {
			target.Label = label
		}
		kind := args.value("kind")
		path := strings.TrimSpace(args.value("path"))
		if args.has("path") && path == "" {
			return nil, errors.New("--path 不能是空路径")
		}
		if args.has("path") {
			normalized, err := core.NormalizeSourceRootPath(path)
			if err != nil {
				return nil, err
			}
			target.RootPath = normalized
		}
		if kind != "" && kind != target.Kind {
			updated, err := core.CreateSourceConfig(target.ID, target.Label, kind, target.RootPath, target.Enabled)
			if err != nil {
				return nil, err
			}
			target.Kind = updated.Kind
			target.IncludeExtensions = updated.IncludeExtensions
			target.ExcludeDirectoryNames = updated.ExcludeDirectoryNames
		}
		if args.has("enable") {
			target.Enabled = true
		}
		if args.has("disable") {
			target.Enabled = false
		}
		config.Sources[index] = target
		return service.ReconcileSourcesCAS(ctx, config, baseFingerprint.SHA256, true, progress)
	case "remove":
		if !args.has("yes") {
			return nil, errors.New("删除来源会移除该来源配置与本地索引；确认后请添加 --yes")
		}
		config.Sources = append(config.Sources[:index], config.Sources[index+1:]...)
		return service.ReconcileSourcesCAS(ctx, config, baseFingerprint.SHA256, false, progress)
	default:
		return nil, fmt.Errorf("未知 sources 操作：%s", action)
	}
}

func execute(ctx context.Context, args arguments) (any, bool, error) {
	if args.command == "--version" || args.command == "version" || args.has("version") {
		return map[string]any{"name": "design-rag", "cli": "drag", "version": core.BackendVersion, "go": runtime.Version(), "platform": runtime.GOOS, "arch": runtime.GOARCH}, true, nil
	}
	if args.command == "help" || args.command == "--help" || args.has("help") {
		printHelp()
		return nil, false, nil
	}
	if args.command == "mcp" {
		service, err := core.NewRuntimeService(ctx, core.ServiceOptions{})
		if err != nil {
			return nil, false, err
		}
		defer service.Close()
		return nil, false, core.RunMCPServer(ctx, service)
	}
	readOnlyCommands := map[string]bool{"config": true, "doctor": true, "search": true, "retrieve": true, "status": true, "versions": true, "citation": true}
	readOnly := readOnlyCommands[args.command] || (args.command == "sources" && (len(args.positional) == 0 || args.positional[0] == "list"))
	service, err := core.NewRuntimeService(ctx, core.ServiceOptions{ReadOnly: readOnly})
	if err != nil {
		return nil, false, err
	}
	defer service.Close()
	config := service.Config()
	switch args.command {
	case "init":
		return map[string]any{"configPath": service.ConfigStore.ConfigPath, "databasePath": service.Database.Path(), "config": config}, true, nil
	case "config":
		return map[string]any{"configPath": service.ConfigStore.ConfigPath, "dataDir": service.ConfigStore.DataDir, "config": config}, true, nil
	case "doctor":
		integrity, checkErr := service.Database.IntegrityCheck()
		formatStats, formatErr := service.Database.FormatStats()
		sources := []map[string]any{}
		allSourcesExist := true
		for _, source := range config.Sources {
			exists := sourceExists(source.RootPath)
			if source.Enabled && !exists {
				allSourcesExist = false
			}
			sources = append(sources, map[string]any{"id": source.ID, "kind": source.Kind, "rootPath": source.RootPath, "enabled": source.Enabled, "exists": exists})
		}
		status, statusErr := service.Status()
		ok := checkErr == nil && formatErr == nil && statusErr == nil && len(integrity) == 1 && integrity[0] == "ok" && allSourcesExist
		return map[string]any{"ok": ok, "go": runtime.Version(), "platform": runtime.GOOS + "-" + runtime.GOARCH, "sources": sources, "integrity": integrity, "integrityError": errorText(checkErr), "formatStats": formatStats, "formatStatsError": errorText(formatErr), "status": status, "statusError": errorText(statusErr)}, true, nil
	case "sources":
		value, err := handleSources(ctx, service, args)
		return value, true, err
	case "index":
		if args.has("full") && len(args.values("source")) > 0 {
			return nil, false, errors.New("--full 不能与 --source 同时使用；来源级更新始终使用增量索引")
		}
		var progressSink core.ProgressSink = progress
		if args.has("json") {
			progressSink = nil
		}
		result, err := service.Index(ctx, core.IndexOptions{Full: args.has("full"), SourceIDs: args.values("source")}, progressSink)
		if progressSink != nil {
			fmt.Fprintln(os.Stderr)
		}
		return result, true, err
	case "search":
		query := strings.TrimSpace(strings.Join(args.positional, " "))
		if query == "" {
			return nil, false, errors.New("search 需要查询文本")
		}
		limit, err := args.integer("limit")
		if err != nil {
			return nil, false, err
		}
		result, err := service.Search.Search(ctx, core.SearchRequest{Query: query, SourceIDs: args.values("source"), Sort: args.value("sort"), Limit: limit, LatestPerFamily: args.has("latest-per-family")})
		return result, true, err
	case "retrieve":
		query := strings.TrimSpace(strings.Join(args.positional, " "))
		if query == "" {
			return nil, false, errors.New("retrieve 需要查询文本")
		}
		maxChars, err := args.integer("max-chars")
		if err != nil {
			return nil, false, err
		}
		maxDocuments, err := args.integer("max-documents")
		if err != nil {
			return nil, false, err
		}
		maxChunks, err := args.integer("max-chunks-per-document")
		if err != nil {
			return nil, false, err
		}
		result, err := service.Search.Retrieve(ctx, core.RetrievalRequest{SearchRequest: core.SearchRequest{Query: query, Sort: args.value("sort"), SourceIDs: args.values("source")}, MaxDocuments: maxDocuments, MaxChunksPerDocument: maxChunks, MaxChars: maxChars})
		return result, true, err
	case "status":
		status, err := service.Status()
		return map[string]any{"status": status, "sources": config.Sources}, true, err
	case "versions":
		limit, err := args.integer("limit")
		if err != nil {
			return nil, false, err
		}
		result, err := service.Search.ListVersions(ctx, args.value("document-id"), args.value("family-key"), limit)
		return result, true, err
	case "citation":
		if len(args.positional) == 0 {
			return nil, false, errors.New("citation 需要 citationId")
		}
		revision, err := args.integer("revision")
		if err != nil {
			return nil, false, err
		}
		var expected *int64
		if args.has("revision") {
			value := int64(revision)
			expected = &value
		}
		result, err := service.Search.ReadCitation(ctx, args.positional[0], expected)
		return result, true, err
	case "cache":
		if len(args.positional) == 0 || args.positional[0] != "clear" {
			return nil, false, errors.New("cache 仅支持 clear 操作")
		}
		if !args.has("yes") {
			return nil, false, errors.New("删除全部本地检索缓存需添加 --yes")
		}
		result, err := service.ClearIndexCache(ctx)
		return result, true, err
	default:
		printHelp()
		return nil, false, flag.ErrHelp
	}
}

func errorText(err error) any {
	if err == nil {
		return nil
	}
	return err.Error()
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	args, err := parseArguments(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	value, shouldWrite, err := execute(ctx, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		if shouldWrite && value != nil && args.has("json") {
			_ = writeJSON(map[string]any{"result": value, "error": err.Error()})
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if shouldWrite {
		if mapValue, ok := value.(map[string]any); ok {
			keys := make([]string, 0, len(mapValue))
			for key := range mapValue {
				keys = append(keys, key)
			}
			sort.Strings(keys)
		}
		if err := writeJSON(value); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
