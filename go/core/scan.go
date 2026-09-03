package core

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type DiscoveryResult struct {
	Source     Source
	Candidates []Candidate
	Skipped    int
	Err        error
}

var defaultToolDirectories = map[string]struct{}{
	".git": {}, ".svn": {}, ".hg": {},
	".cursor": {}, ".codex": {}, ".agents": {}, ".claude": {},
	".windsurf": {}, ".continue": {}, ".aider": {}, ".cline": {}, ".roo": {}, ".gemini": {},
	".openai": {}, ".github": {}, ".gitlab": {}, ".vscode": {}, ".idea": {}, ".vs": {},
	".devcontainer": {}, ".obsidian": {},
	".aws": {}, ".azure": {}, ".gcloud": {}, ".kube": {}, ".ssh": {}, ".gnupg": {}, ".docker": {},
	"node_modules": {}, "dist": {}, "build": {}, "temp": {}, "tmp": {}, "__macosx": {},
}

var sensitiveLocalFileNames = map[string]struct{}{
	".env": {}, ".npmrc": {}, ".pypirc": {}, ".netrc": {},
	"credentials.json": {}, "credentials.yaml": {}, "credentials.yml": {}, ".credentials.json": {}, "credential.json": {},
	"secrets.json": {}, "secrets.yaml": {}, "secrets.yml": {}, ".secrets.json": {},
	"token.json": {}, "token.yaml": {}, "token.yml": {}, "tokens.json": {}, ".token.json": {},
	"application_default_credentials.json": {},
	"id_rsa":                               {}, "id_dsa": {}, "id_ecdsa": {}, "id_ed25519": {}, "authorized_keys": {},
	"private.key": {}, "private.pem": {}, "server.key": {}, "client.key": {},
}

var sensitiveLocalFilePatterns = []string{
	".env.*",
	"*-credentials.json",
	"*-secrets.json",
	"*.token.json",
	"client_secret*.json",
	"client-secret*.json",
	"service-account*.json",
	"service_account*.json",
	"config.local.*",
	"config.private.*",
	"config.secret.*",
	"settings.local.*",
	"local.settings.*",
}

func temporaryFile(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(name, "~$") || strings.HasPrefix(name, ".~") || (strings.HasPrefix(strings.ToUpper(name), "~WRL") && strings.HasSuffix(lower, ".tmp")) || strings.HasSuffix(lower, ".tmp") || strings.HasSuffix(lower, ".bak")
}

func matchesBaseName(name string, patterns []string) bool {
	lower := strings.ToLower(name)
	for _, rawPattern := range patterns {
		pattern := strings.ToLower(strings.TrimSpace(rawPattern))
		if pattern == "" {
			continue
		}
		if lower == pattern {
			return true
		}
		if matched, err := filepath.Match(pattern, lower); err == nil && matched {
			return true
		}
	}
	return false
}

func excludedToolDirectory(name string) bool {
	_, excluded := defaultToolDirectories[strings.ToLower(name)]
	return excluded
}

func sensitiveLocalFile(name string) bool {
	lower := strings.ToLower(name)
	if _, excluded := sensitiveLocalFileNames[lower]; excluded {
		return true
	}
	return matchesBaseName(lower, sensitiveLocalFilePatterns)
}

func discoverSource(ctx context.Context, source Source) DiscoveryResult {
	result := DiscoveryResult{Source: source}
	root, err := filepath.Abs(source.RootPath)
	if err != nil {
		result.Err = err
		return result
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		result.Err = err
		return result
	}
	if !rootInfo.IsDir() {
		result.Err = fmt.Errorf("资料源不是目录：%s", root)
		return result
	}
	walkRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		result.Err = fmt.Errorf("无法解析资料源真实路径：%w", err)
		return result
	}
	walkRoot, err = filepath.Abs(walkRoot)
	if err != nil {
		result.Err = err
		return result
	}
	extensions := make(map[string]struct{}, len(source.IncludeExtensions))
	for _, extension := range source.IncludeExtensions {
		extensions[strings.ToLower(extension)] = struct{}{}
	}
	err = filepath.WalkDir(walkRoot, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if current == walkRoot {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			result.Skipped++
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if excludedToolDirectory(entry.Name()) || matchesBaseName(entry.Name(), source.ExcludeDirectoryNames) {
				return filepath.SkipDir
			}
			return nil
		}
		if temporaryFile(entry.Name()) || sensitiveLocalFile(entry.Name()) || matchesBaseName(entry.Name(), source.ExcludeDirectoryNames) {
			result.Skipped++
			return nil
		}
		extension := strings.ToLower(filepath.Ext(entry.Name()))
		if _, allowed := extensions[extension]; !allowed {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() {
			result.Skipped++
			return nil
		}
		if info.Size() > source.MaxFileBytes {
			result.Skipped++
			return nil
		}
		relative, relErr := filepath.Rel(walkRoot, current)
		if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			result.Skipped++
			return nil
		}
		readPath, absErr := filepath.Abs(current)
		if absErr != nil {
			return absErr
		}
		absolute := filepath.Join(root, relative)
		result.Candidates = append(result.Candidates, Candidate{
			SourceID:          source.ID,
			SourceLabel:       source.Label,
			SourceKind:        source.Kind,
			SourceIdentity:    source.IndexIdentity,
			RootPath:          root,
			AbsolutePath:      absolute,
			ReadPath:          readPath,
			RelativePath:      relative,
			Extension:         extension,
			SizeBytes:         info.Size(),
			FilesystemMtimeMS: info.ModTime().UnixMilli(),
		})
		return nil
	})
	if err != nil {
		result.Err = err
		return result
	}
	sort.Slice(result.Candidates, func(i, j int) bool {
		return strings.ToLower(result.Candidates[i].RelativePath) < strings.ToLower(result.Candidates[j].RelativePath)
	})
	return result
}

func DiscoverSources(ctx context.Context, sources []Source) []DiscoveryResult {
	results := make([]DiscoveryResult, len(sources))
	var wait sync.WaitGroup
	for index, source := range sources {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[index] = discoverSource(ctx, source)
		}()
	}
	wait.Wait()
	return results
}
