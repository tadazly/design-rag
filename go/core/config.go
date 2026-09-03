package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const ConfigSchemaVersion = 1

var commonExcludes = []string{
	".git", ".svn", ".hg", ".cursor", ".codex", ".agents", ".claude", ".windsurf",
	".continue", ".aider", ".cline", ".roo", ".gemini", ".openai", ".github", ".gitlab",
	".vscode", ".idea", ".vs", ".devcontainer", ".obsidian", ".aws", ".azure", ".gcloud",
	".kube", ".ssh", ".gnupg", ".docker", "node_modules", "dist", "build", "temp", "tmp",
	"__macosx", ".env", ".env.*", ".npmrc", ".pypirc", ".netrc", "credentials.json",
	"credentials.yaml", "credentials.yml", ".credentials.json", "credential.json", "secrets.json",
	"secrets.yaml", "secrets.yml", ".secrets.json", "token.json", "token.yaml", "token.yml",
	"tokens.json", ".token.json", "*-credentials.json", "*-secrets.json", "*.token.json",
	"client_secret*.json", "client-secret*.json", "service-account*.json", "service_account*.json",
	"application_default_credentials.json", "config.local.*", "config.private.*", "config.secret.*",
	"settings.local.*", "local.settings.*", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519",
	"authorized_keys", "private.key", "private.pem", "server.key", "client.key",
}

type ConfigFileFingerprint struct {
	MtimeMS   int64  `json:"mtimeMs"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}

type ConfigSnapshot struct {
	Config      AppConfig
	Fingerprint ConfigFileFingerprint
}

type ConfigStore struct {
	ConfigDir  string
	DataDir    string
	ConfigPath string
}

func resolveStateNamespace() string {
	value := strings.TrimSpace(os.Getenv("DESIGN_RAG_STATE_NAMESPACE"))
	if value == "" {
		return "design-rag"
	}
	if len(value) > 64 {
		return "design-rag"
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' || character == '_') {
			return "design-rag"
		}
	}
	return value
}

func ResolveConfigDir() string {
	if value := firstNonEmptyEnvironment("DESIGN_RAG_CONFIG_DIR"); value != "" {
		return absoluteCleanPath(value)
	}
	home, _ := os.UserHomeDir()
	namespace := resolveStateNamespace()
	if runtime.GOOS == "darwin" {
		modernName := namespace
		if namespace == "design-rag" {
			modernName = "DesignRag"
		}
		modern := filepath.Join(home, "Library", "Application Support", modernName, "config")
		previous := filepath.Join(home, "Library", "Application Support", namespace, "config")
		if namespace != "design-rag" {
			return modern
		}
		return preferModernPath(modern, previous)
	}
	base := strings.TrimSpace(os.Getenv("APPDATA"))
	if base == "" {
		base = filepath.Join(home, ".config")
	}
	modernName := namespace
	if namespace == "design-rag" {
		modernName = "DesignRag"
	}
	modern := filepath.Join(base, modernName)
	if namespace != "design-rag" {
		return modern
	}
	return preferModernPath(modern, filepath.Join(base, namespace))
}

func ResolveDataDir() string {
	if value := firstNonEmptyEnvironment("DESIGN_RAG_DATA_DIR"); value != "" {
		return absoluteCleanPath(value)
	}
	home, _ := os.UserHomeDir()
	namespace := resolveStateNamespace()
	if runtime.GOOS == "darwin" {
		modernName := namespace
		if namespace == "design-rag" {
			modernName = "DesignRag"
		}
		modern := filepath.Join(home, "Library", "Application Support", modernName, "data")
		previous := filepath.Join(home, "Library", "Application Support", namespace, "data")
		if namespace != "design-rag" {
			return modern
		}
		return preferModernPath(modern, previous)
	}
	base := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if base == "" {
		base = filepath.Join(home, ".local", "share")
	}
	modernName := namespace
	if namespace == "design-rag" {
		modernName = "DesignRag"
	}
	modern := filepath.Join(base, modernName)
	if namespace != "design-rag" {
		return modern
	}
	return preferModernPath(modern, filepath.Join(base, namespace))
}

func firstNonEmptyEnvironment(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func absoluteCleanPath(value string) string {
	abs, err := filepath.Abs(value)
	if err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(value)
}

func preferModernPath(modern, legacy string) string {
	if _, err := os.Stat(modern); err == nil || !errors.Is(err, os.ErrNotExist) {
		return modern
	}
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return modern
}

func NewConfigStore(configDir, dataDir string) *ConfigStore {
	if strings.TrimSpace(configDir) == "" {
		configDir = ResolveConfigDir()
	}
	if strings.TrimSpace(dataDir) == "" {
		dataDir = ResolveDataDir()
	}
	configDir = absoluteCleanPath(configDir)
	dataDir = absoluteCleanPath(dataDir)
	return &ConfigStore{ConfigDir: configDir, DataDir: dataDir, ConfigPath: filepath.Join(configDir, "config.json")}
}

func SourceIndexIdentity(source Source) string {
	return HashString("v1\x00" + source.Kind + "\x00" + CanonicalPathKey(source.RootPath))
}

func sourceWithIdentity(source Source) Source {
	result := source
	result.IndexIdentity = SourceIndexIdentity(source)
	return result
}

func ConfigForIndex(config AppConfig) AppConfig {
	result := config
	result.Sources = make([]Source, len(config.Sources))
	for index, source := range config.Sources {
		result.Sources[index] = sourceWithIdentity(source)
	}
	return result
}

func NormalizeSourceRootPath(rootPath string) (string, error) {
	value := strings.TrimSpace(rootPath)
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("资料源目录必须使用绝对路径：%s", rootPath)
	}
	return absoluteCleanPath(value), nil
}

func ValidateSourceRootPath(rootPath string) (string, error) {
	normalized, err := NormalizeSourceRootPath(rootPath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(normalized)
	if err != nil {
		// os.Stat reports ENOENT/ERROR_PATH_NOT_FOUND both for a genuinely
		// offline directory and for "existing-file/child". Walk to the nearest
		// existing ancestor so the latter cannot be saved as a retryable source.
		for current := filepath.Dir(normalized); ; current = filepath.Dir(current) {
			ancestor, ancestorErr := os.Stat(current)
			if ancestorErr == nil {
				if !ancestor.IsDir() {
					return "", fmt.Errorf("资料源路径的上级不是目录：%s", normalized)
				}
				break
			}
			parent := filepath.Dir(current)
			if parent == current || !errors.Is(ancestorErr, os.ErrNotExist) {
				break
			}
		}
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			return normalized, nil
		}
		return normalized, nil // Offline/disconnected sources remain pending and are diagnosed by discovery.
	}
	if !info.IsDir() {
		return "", fmt.Errorf("资料源路径已存在但不是目录：%s", normalized)
	}
	return normalized, nil
}

func CreateSourceConfig(id, label, kind, rootPath string, enabled bool) (Source, error) {
	normalized, err := NormalizeSourceRootPath(rootPath)
	if err != nil {
		return Source{}, err
	}
	source := Source{
		ID: id, Label: label, Kind: kind, RootPath: normalized, Enabled: enabled,
		ExcludeDirectoryNames: append([]string(nil), commonExcludes...), MaxFileBytes: 128 * 1024 * 1024,
	}
	if kind == "design" {
		source.IncludeExtensions = []string{".docx", ".xlsx", ".xlsm", ".xls", ".pdf", ".xmind", ".md", ".markdown", ".txt", ".html", ".json", ".yaml", ".yml"}
	} else if kind == "table" {
		source.IncludeExtensions = []string{".xlsx", ".xlsm", ".xls", ".csv", ".pdf"}
	} else {
		return Source{}, fmt.Errorf("资料源类型必须为 design 或 table：%s", kind)
	}
	return source, nil
}

func CreateDefaultConfig() AppConfig {
	return AppConfig{
		SchemaVersion: ConfigSchemaVersion,
		Sources:       []Source{},
		Search: SearchConfig{
			DefaultSort: "newest", DefaultLimit: 12, MaxEvidenceChars: 24_000, SynonymExpansion: true,
			Embedding: EmbeddingConfig{Enabled: false, Provider: "ollama", Endpoint: "http://127.0.0.1:11434/api/embed", Model: "embeddinggemma", TimeoutMS: 30_000},
		},
		Indexing: IndexingConfig{AutomaticScan: true, ScanIntervalMinutes: 10, Concurrency: 16},
		Codex:    CodexConfig{},
	}
}

func withDefaultExcludes(values []string) []string {
	result := append([]string(nil), values...)
	seen := map[string]bool{}
	for _, value := range values {
		seen[strings.ToLower(strings.TrimSpace(value))] = true
	}
	for _, value := range commonExcludes {
		key := strings.ToLower(value)
		if !seen[key] {
			result = append(result, value)
			seen[key] = true
		}
	}
	return result
}

func pathsOverlap(left, right string) bool {
	relative, err := filepath.Rel(left, right)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)))
}

func validateSourceTopology(sources []Source, keys []string) error {
	seen := map[string]int{}
	for index, source := range sources {
		if previous, ok := seen[source.ID]; ok {
			return fmt.Errorf("资料源 id 与第 %d 项重复：%s", previous+1, source.ID)
		}
		seen[source.ID] = index
		for previous := 0; previous < index; previous++ {
			if pathsOverlap(keys[previous], keys[index]) || pathsOverlap(keys[index], keys[previous]) {
				return fmt.Errorf("资料源目录不能相同或互为父子目录：%s 与 %s", sources[previous].RootPath, source.RootPath)
			}
		}
	}
	return nil
}

func ValidateConfig(config AppConfig) (AppConfig, error) {
	if config.SchemaVersion != ConfigSchemaVersion {
		return AppConfig{}, fmt.Errorf("配置 schemaVersion 必须为 %d", ConfigSchemaVersion)
	}
	if config.Search.DefaultSort != "newest" && config.Search.DefaultSort != "relevance" && config.Search.DefaultSort != "hybrid" {
		return AppConfig{}, fmt.Errorf("默认检索排序无效：%s", config.Search.DefaultSort)
	}
	if config.Search.DefaultLimit < 1 || config.Search.DefaultLimit > 50 || config.Search.MaxEvidenceChars < 2_000 || config.Search.MaxEvidenceChars > 60_000 {
		return AppConfig{}, fmt.Errorf("检索配置超出允许范围")
	}
	endpoint, endpointErr := url.Parse(strings.TrimSpace(config.Search.Embedding.Endpoint))
	hostname := strings.ToLower(endpoint.Hostname())
	loopback := hostname == "localhost"
	if parsedIP := net.ParseIP(hostname); parsedIP != nil {
		loopback = parsedIP.IsLoopback()
	}
	if config.Search.Embedding.Provider != "ollama" || endpointErr != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil || !loopback || strings.TrimSpace(config.Search.Embedding.Model) == "" || config.Search.Embedding.TimeoutMS < 500 || config.Search.Embedding.TimeoutMS > 120_000 {
		return AppConfig{}, fmt.Errorf("embedding 配置无效")
	}
	if config.Indexing.Concurrency < 1 || config.Indexing.Concurrency > 32 || config.Indexing.ScanIntervalMinutes < 1 || config.Indexing.ScanIntervalMinutes > 1_440 {
		return AppConfig{}, fmt.Errorf("索引配置超出允许范围")
	}
	normalized := config
	normalized.Sources = make([]Source, len(config.Sources))
	keys := make([]string, len(config.Sources))
	realKeys := make([]string, len(config.Sources))
	for index, source := range config.Sources {
		if source.ID == "" || strings.Trim(source.ID, "abcdefghijklmnopqrstuvwxyz0123456789_-") != "" {
			return AppConfig{}, fmt.Errorf("资料源 id 只能包含小写字母、数字、下划线和连字符：%s", source.ID)
		}
		if strings.TrimSpace(source.Label) == "" || (source.Kind != "design" && source.Kind != "table") {
			return AppConfig{}, fmt.Errorf("资料源 %s 的名称或类型无效", source.ID)
		}
		root, err := ValidateSourceRootPath(source.RootPath)
		if err != nil {
			return AppConfig{}, err
		}
		if len(source.IncludeExtensions) == 0 || source.MaxFileBytes < 1_024 || source.MaxFileBytes > 2_147_483_647 {
			return AppConfig{}, fmt.Errorf("资料源 %s 的扩展名、排除项或大小限制无效", source.ID)
		}
		for _, extension := range source.IncludeExtensions {
			if strings.TrimSpace(extension) == "" {
				return AppConfig{}, fmt.Errorf("资料源 %s 包含空扩展名", source.ID)
			}
		}
		for _, excluded := range source.ExcludeDirectoryNames {
			if strings.TrimSpace(excluded) == "" {
				return AppConfig{}, fmt.Errorf("资料源 %s 包含空排除项", source.ID)
			}
		}
		source.RootPath = root
		source.IndexIdentity = ""
		source.ExcludeDirectoryNames = withDefaultExcludes(source.ExcludeDirectoryNames)
		normalized.Sources[index] = source
		keys[index] = CanonicalPathKey(root)
		realKeys[index] = keys[index]
		if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
			realKeys[index] = CanonicalPathKey(resolved)
		}
	}
	if err := validateSourceTopology(normalized.Sources, keys); err != nil {
		return AppConfig{}, err
	}
	if err := validateSourceTopology(normalized.Sources, realKeys); err != nil {
		return AppConfig{}, err
	}
	for _, value := range []*string{normalized.Codex.CodexPath, normalized.Codex.Model, normalized.Codex.ReasoningEffort} {
		if value != nil && strings.TrimSpace(*value) == "" {
			return AppConfig{}, fmt.Errorf("Codex 配置字段不能是空字符串")
		}
	}
	return normalized, nil
}

func (store *ConfigStore) ensureDirectories() error {
	if err := os.MkdirAll(store.ConfigDir, 0o700); err != nil {
		return err
	}
	return os.MkdirAll(store.DataDir, 0o700)
}

func fingerprintBytes(info os.FileInfo, raw []byte) ConfigFileFingerprint {
	hash := sha256.Sum256(raw)
	return ConfigFileFingerprint{MtimeMS: info.ModTime().UnixMilli(), SizeBytes: int64(len(raw)), SHA256: hex.EncodeToString(hash[:])}
}

func (store *ConfigStore) readStableFile() ([]byte, ConfigFileFingerprint, error) {
	for attempt := 0; attempt < 3; attempt++ {
		before, err := os.Stat(store.ConfigPath)
		if err != nil {
			return nil, ConfigFileFingerprint{}, err
		}
		raw, err := os.ReadFile(store.ConfigPath)
		if err != nil {
			return nil, ConfigFileFingerprint{}, err
		}
		after, err := os.Stat(store.ConfigPath)
		if err == nil && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime()) {
			return raw, fingerprintBytes(after, raw), nil
		}
	}
	return nil, ConfigFileFingerprint{}, fmt.Errorf("配置文件在读取期间持续变化，请稍后重试")
}

func requiredJSONObject(raw json.RawMessage, label string, required []string, nullable map[string]bool) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("配置 %s 必须是对象", label)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, fmt.Errorf("配置 %s 必须是对象", label)
	}
	for _, field := range required {
		value, exists := object[field]
		if !exists || (!nullable[field] && bytes.Equal(bytes.TrimSpace(value), []byte("null"))) {
			return nil, fmt.Errorf("配置缺少必需字段 %s.%s", label, field)
		}
	}
	return object, nil
}

func validateRawConfigShape(raw []byte) error {
	var rootRaw json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&rootRaw); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("配置包含多个 JSON 值")
		}
		return fmt.Errorf("配置存在尾随内容: %w", err)
	}
	root, err := requiredJSONObject(rootRaw, "root", []string{"schemaVersion", "sources", "search", "indexing", "codex"}, nil)
	if err != nil {
		return err
	}
	var sources []json.RawMessage
	if trimmed := bytes.TrimSpace(root["sources"]); len(trimmed) == 0 || trimmed[0] != '[' || json.Unmarshal(trimmed, &sources) != nil {
		return fmt.Errorf("配置 root.sources 必须是数组")
	}
	for index, sourceRaw := range sources {
		if _, err := requiredJSONObject(sourceRaw, fmt.Sprintf("sources[%d]", index), []string{"id", "label", "kind", "rootPath", "enabled", "includeExtensions", "excludeDirectoryNames", "maxFileBytes"}, nil); err != nil {
			return err
		}
	}
	search, err := requiredJSONObject(root["search"], "search", []string{"defaultSort", "defaultLimit", "maxEvidenceChars", "synonymExpansion", "embedding"}, nil)
	if err != nil {
		return err
	}
	if _, err := requiredJSONObject(search["embedding"], "search.embedding", []string{"enabled", "provider", "endpoint", "model", "timeoutMs"}, nil); err != nil {
		return err
	}
	if _, err := requiredJSONObject(root["indexing"], "indexing", []string{"automaticScan", "scanIntervalMinutes", "concurrency"}, nil); err != nil {
		return err
	}
	if _, err := requiredJSONObject(root["codex"], "codex", []string{"codexPath", "model", "reasoningEffort"}, map[string]bool{"codexPath": true, "model": true, "reasoningEffort": true}); err != nil {
		return err
	}
	return nil
}

func (store *ConfigStore) LoadSnapshot() (ConfigSnapshot, error) {
	if err := store.ensureDirectories(); err != nil {
		return ConfigSnapshot{}, err
	}
	raw, fingerprint, err := store.readStableFile()
	if errors.Is(err, os.ErrNotExist) {
		return store.SaveSnapshot(CreateDefaultConfig())
	}
	if err != nil {
		return ConfigSnapshot{}, err
	}
	return decodeConfigSnapshot(raw, fingerprint)
}

func (store *ConfigStore) LoadSnapshotReadOnly() (ConfigSnapshot, error) {
	raw, fingerprint, err := store.readStableFile()
	if errors.Is(err, os.ErrNotExist) {
		return ConfigSnapshot{}, fmt.Errorf("配置不存在；请先运行 drag init")
	}
	if err != nil {
		return ConfigSnapshot{}, err
	}
	return decodeConfigSnapshot(raw, fingerprint)
}

func decodeConfigSnapshot(raw []byte, fingerprint ConfigFileFingerprint) (ConfigSnapshot, error) {
	if err := validateRawConfigShape(raw); err != nil {
		return ConfigSnapshot{}, fmt.Errorf("读取配置失败: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var config AppConfig
	if err := decoder.Decode(&config); err != nil {
		return ConfigSnapshot{}, fmt.Errorf("读取配置失败: %w", err)
	}
	validated, err := ValidateConfig(config)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	return ConfigSnapshot{Config: validated, Fingerprint: fingerprint}, nil
}

func (store *ConfigStore) SaveSnapshot(config AppConfig) (ConfigSnapshot, error) {
	validated, err := ValidateConfig(config)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	if err = store.ensureDirectories(); err != nil {
		return ConfigSnapshot{}, err
	}
	raw, err := json.MarshalIndent(validated, "", "  ")
	if err != nil {
		return ConfigSnapshot{}, err
	}
	raw = append(raw, '\n')
	temporary, err := os.CreateTemp(store.ConfigDir, ".config-*.tmp")
	if err != nil {
		return ConfigSnapshot{}, err
	}
	temporaryPath := temporary.Name()
	cleanup := func() { _ = os.Remove(temporaryPath) }
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(raw)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		cleanup()
		return ConfigSnapshot{}, err
	}
	if err = replaceFileAtomic(temporaryPath, store.ConfigPath); err != nil {
		cleanup()
		return ConfigSnapshot{}, fmt.Errorf("原子保存配置失败: %w", err)
	}
	info, err := os.Stat(store.ConfigPath)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	return ConfigSnapshot{Config: validated, Fingerprint: fingerprintBytes(info, raw)}, nil
}

func (store *ConfigStore) Fingerprint() (ConfigFileFingerprint, error) {
	_, fingerprint, err := store.readStableFile()
	return fingerprint, err
}

func normalizedStringSet(values []string) string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return strings.Join(result, "\x00")
}

func configTimestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
