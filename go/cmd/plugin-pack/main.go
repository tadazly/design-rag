package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tadazly/design-rag/go/pluginpack"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法：plugin-pack validate | stage --target <target> [--test-marker] | pack --target <target>")
		os.Exit(2)
	}
	projectRoot, err := filepath.Abs(".")
	if err != nil {
		fatal(err)
	}
	var result any
	switch os.Args[1] {
	case "validate":
		flags := flag.NewFlagSet("validate", flag.ExitOnError)
		root := flags.String("project-root", projectRoot, "project root")
		_ = flags.Parse(os.Args[2:])
		result, err = pluginpack.ValidateSource(*root)
	case "stage", "pack":
		flags := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
		target := flags.String("target", "", "win32-x64 or darwin-arm64")
		root := flags.String("project-root", projectRoot, "project root")
		output := flags.String("output-root", "", "generated output root")
		testMarker := flags.Bool("test-marker", false, "use isolated go-test identity and state")
		_ = flags.Parse(os.Args[2:])
		if *target == "" {
			fatal(fmt.Errorf("缺少 --target"))
		}
		if os.Args[1] == "pack" && *testMarker {
			fatal(fmt.Errorf("最终 archive 禁止使用 --test-marker"))
		}
		result, err = pluginpack.Build(context.Background(), pluginpack.Options{ProjectRoot: *root, OutputRoot: *output, Target: *target, TestMarker: *testMarker, Pack: os.Args[1] == "pack"})
	default:
		fatal(fmt.Errorf("未知命令：%s", os.Args[1]))
	}
	if err != nil {
		fatal(err)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
