package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/compose-spec/compose-go/v2/dotenv"

	"github.com/IceCodeNew/maniud/internal/llm"
	"github.com/IceCodeNew/maniud/internal/tui"
)

const (
	llmProviderEnvironment    = "MANIUD_LLM_PROVIDER"
	llmModelEnvironment       = "MANIUD_LLM_MODEL"
	llmTimeoutEnvironment     = "MANIUD_LLM_TIMEOUT"
	openAIEndpointEnvironment = "OPENAI_BASE_URL"
	openAIKeyEnvironment      = "OPENAI_API_KEY"
	deepSeekKeyEnvironment    = "DEEPSEEK_API_KEY"
	maximumLLMEnvBytes        = 1 << 20
	defaultConfigDirectory    = ".config"
	llmConfigDirectory        = "maniud"
	llmConfigName             = ".env"
	homeEnvironment           = "HOME"
	xdgConfigHomeEnvironment  = "XDG_CONFIG_HOME"
	maximumLLMSourceWarnings  = 2
	llmSecretNamesPerSource   = 2
)

var (
	errLLMConfigInvalid     = errors.New("LLM configuration is invalid")
	errLLMConfigPathInvalid = errors.New("LLM configuration path is invalid")
	errLLMConfigSaveStale   = errors.New("LLM configuration changed before save")
	errLLMConfigSaveUnknown = errors.New("LLM configuration save outcome is unknown")
)

type llmFileIdentity struct {
	device uint64
	inode  uint64
	links  uint64
	owner  uint32
}

type llmEnvState struct {
	exists   bool
	valid    bool
	mode     os.FileMode
	identity llmFileIdentity
	digest   [sha256.Size]byte
	raw      []byte
	values   map[string]string
}

type llmConfigBaseline struct {
	initialized bool
	state       llmEnvState
}

type llmEnvSource struct {
	name   string
	values map[string]string
}

type llmResolvedConfig struct {
	config    llm.Config
	identity  string
	keySource string
	secrets   []string
	warnings  []string
	baseline  llmConfigBaseline
}

func loadLLMConfiguration(
	ctx context.Context,
	environment map[string]string,
	workingDirectory string,
) (llmResolvedConfig, error) {
	if err := ctx.Err(); err != nil {
		return llmResolvedConfig{}, fmt.Errorf("load LLM configuration: %w", err)
	}
	current, currentWarning := readCurrentLLMEnv(workingDirectory)
	xdg, xdgWarning := readXDGLLMEnv(environment)
	sources := []llmEnvSource{{name: "process environment", values: environment}}
	warnings := make([]string, 0, maximumLLMSourceWarnings)
	if currentWarning != "" {
		warnings = append(warnings, currentWarning)
	} else {
		sources = append(sources, llmEnvSource{name: "current .env", values: current.values})
	}
	if xdgWarning != "" {
		warnings = append(warnings, xdgWarning)
	} else {
		sources = append(sources, llmEnvSource{name: "XDG .env", values: xdg.values})
	}

	provider, providerSource := resolveLLMValue(sources, llmProviderEnvironment, false)
	model, modelSource := resolveLLMValue(sources, llmModelEnvironment, false)
	endpoint, endpointSource := resolveLLMValue(sources, openAIEndpointEnvironment, false)
	timeoutValue, timeoutSource := resolveLLMValue(sources, llmTimeoutEnvironment, false)
	providerValue := llm.Provider(provider)
	keyName := openAIKeyEnvironment
	if providerValue == llm.ProviderDeepSeek {
		keyName = deepSeekKeyEnvironment
	}
	apiKey, keySource := resolveLLMValue(sources, keyName, true)
	timeout, timeoutErr := llm.ParseTimeout(timeoutValue)
	config := llm.Config{
		Provider: providerValue, Model: model, Endpoint: endpoint,
		Timeout: timeout, APIKey: apiKey,
	}
	if providerValue != llm.ProviderOpenAICompatible {
		config.Endpoint = ""
	}
	identity := ""
	if timeoutErr == nil && config.Validate() == nil {
		identity = llmConfigIdentity(config, []string{
			providerSource, modelSource, endpointSource, timeoutSource, keySource,
		})
	}
	secrets := knownLLMSecrets(sources)

	return llmResolvedConfig{
		config: config, identity: identity, keySource: keySource, secrets: secrets, warnings: warnings,
		baseline: llmConfigBaseline{initialized: true, state: xdg},
	}, nil
}

func knownLLMSecrets(sources []llmEnvSource) []string {
	secrets := make([]string, 0, len(sources)*llmSecretNamesPerSource)
	seen := make(map[string]struct{}, len(sources)*llmSecretNamesPerSource)
	for _, source := range sources {
		for _, name := range []string{openAIKeyEnvironment, deepSeekKeyEnvironment} {
			value := source.values[name]
			if value == "" {
				continue
			}
			if _, duplicate := seen[value]; duplicate {
				continue
			}
			seen[value] = struct{}{}
			secrets = append(secrets, value)
		}
	}

	return secrets
}

func resolveLLMValue(sources []llmEnvSource, name string, secret bool) (string, string) {
	for _, source := range sources {
		value, found := source.values[name]
		if !found {
			continue
		}
		if value != "" || secret {
			if value == "" {
				return "", "explicitly unset in " + source.name
			}

			return value, source.name
		}
	}

	return "", "not configured"
}

func llmConfigIdentity(config llm.Config, sources []string) string {
	payload := strings.Join([]string{
		config.Identity(sources[len(sources)-1]), strings.Join(sources, "\x00"),
	}, "\x00")

	return fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
}

func publicLLMConfiguration(resolved llmResolvedConfig) tui.LLMConfiguration {
	configuration := tui.LLMConfiguration{
		Provider: string(resolved.config.Provider), Model: resolved.config.Model,
		Endpoint: resolved.config.Endpoint, Origin: resolved.config.Origin(),
		KeySource: resolved.keySource, KeyConfigured: resolved.config.APIKey != "",
		Complete: resolved.identity != "", Identity: resolved.identity,
		Warnings: append([]string(nil), resolved.warnings...),
	}
	if resolved.config.Timeout != 0 {
		configuration.Timeout = strconv.FormatInt(int64(resolved.config.Timeout.Seconds()), 10)
	}

	return configuration
}

func parseLLMEnv(raw []byte) (map[string]string, error) {
	if len(raw) > maximumLLMEnvBytes {
		return nil, errLLMConfigInvalid
	}
	values, err := dotenv.Parse(bytes.NewReader(raw))
	if err != nil {
		return nil, errLLMConfigInvalid
	}

	return values, nil
}

func llmConfigRootPath(environment map[string]string) (string, error) {
	root := environment[xdgConfigHomeEnvironment]
	if root == "" {
		home := environment[homeEnvironment]
		if home == "" {
			return "", errLLMConfigPathInvalid
		}
		root = filepath.Join(home, defaultConfigDirectory)
	}
	if strings.IndexByte(root, 0) >= 0 || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", errLLMConfigPathInvalid
	}

	return filepath.Join(root, llmConfigDirectory), nil
}

//nolint:cyclop // This closed map is the persistence policy for the six supported environment keys.
func llmSettingsUpdates(settings tui.LLMSettings) (map[string]*string, error) {
	provider := llm.Provider(settings.Provider)
	timeout, timeoutErr := llm.ParseTimeout(settings.Timeout)
	validation := llm.Config{
		Provider: provider, Model: settings.Model, Endpoint: settings.Endpoint,
		Timeout: timeout, APIKey: "validation-key",
	}
	if provider != llm.ProviderOpenAICompatible {
		validation.Endpoint = ""
	}
	if timeoutErr != nil || validation.Validate() != nil || settings.APIKey != "" && !llm.ValidAPIKey(settings.APIKey) ||
		settings.ClearAPIKey && settings.APIKey != "" {
		return nil, errLLMConfigInvalid
	}
	providerValue := string(provider)
	modelValue := settings.Model
	timeoutValue := settings.Timeout
	updates := map[string]*string{
		llmProviderEnvironment:    &providerValue,
		llmModelEnvironment:       &modelValue,
		llmTimeoutEnvironment:     &timeoutValue,
		openAIEndpointEnvironment: nil,
	}
	if provider == llm.ProviderOpenAICompatible {
		endpointValue := settings.Endpoint
		updates[openAIEndpointEnvironment] = &endpointValue
	}
	if settings.APIKey != "" || settings.ClearAPIKey {
		keyName := openAIKeyEnvironment
		if provider == llm.ProviderDeepSeek {
			keyName = deepSeekKeyEnvironment
		}
		updates[keyName] = nil
		if settings.APIKey != "" {
			keyValue := settings.APIKey
			updates[keyName] = &keyValue
		}
	}

	return updates, nil
}

//nolint:cyclop // Parsing, targeted replacement, and full reparse form one fail-closed dotenv rewrite boundary.
func rewriteLLMEnv(raw []byte, updates map[string]*string) ([]byte, error) {
	lines := strings.SplitAfter(string(raw), "\n")
	output := make([]string, 0, len(lines)+len(updates))
	for _, line := range lines {
		name, value, assignment := dotenvAssignment(line)
		if !assignment {
			output = append(output, line)

			continue
		}
		if _, targeted := updates[name]; !targeted {
			output = append(output, line)

			continue
		}
		if !singleLineDotenvValue(value) {
			return nil, errLLMConfigInvalid
		}
	}
	var candidate strings.Builder
	for _, line := range output {
		candidate.WriteString(line)
	}
	if candidate.Len() != 0 && !strings.HasSuffix(candidate.String(), "\n") {
		candidate.WriteByte('\n')
	}
	for _, name := range []string{
		llmProviderEnvironment, llmModelEnvironment, llmTimeoutEnvironment,
		openAIEndpointEnvironment, openAIKeyEnvironment, deepSeekKeyEnvironment,
	} {
		value, targeted := updates[name]
		if !targeted || value == nil {
			continue
		}
		candidate.WriteString(name)
		candidate.WriteByte('=')
		candidate.WriteString(quoteDotenvValue(*value))
		candidate.WriteByte('\n')
	}
	if candidate.Len() > maximumLLMEnvBytes {
		return nil, errLLMConfigInvalid
	}
	candidateBytes := []byte(candidate.String())
	parsed, err := parseLLMEnv(candidateBytes)
	if err != nil {
		return nil, err
	}
	for name, value := range updates {
		actual, found := parsed[name]
		if value == nil && found || value != nil && (!found || actual != *value) {
			return nil, errLLMConfigInvalid
		}
	}

	return candidateBytes, nil
}

func dotenvAssignment(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\n"))
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	if after, found := strings.CutPrefix(trimmed, "export "); found {
		trimmed = strings.TrimSpace(after)
	}
	name, value, found := strings.Cut(trimmed, "=")
	name = strings.TrimSpace(name)
	if !found || !validDotenvName(name) {
		return "", "", false
	}

	return name, strings.TrimSpace(value), true
}

func validDotenvName(name string) bool {
	for index, character := range name {
		if character == '_' || unicode.IsLetter(character) || index > 0 && unicode.IsDigit(character) {
			continue
		}

		return false
	}

	return name != ""
}

func singleLineDotenvValue(value string) bool {
	for _, quote := range []byte{'\'', '"'} {
		quoted := false
		escaped := false
		for index := range len(value) {
			character := value[index]
			if escaped {
				escaped = false

				continue
			}
			if character == '\\' && quote == '"' {
				escaped = true

				continue
			}
			if character == quote {
				quoted = !quoted
			}
		}
		if quoted {
			return false
		}
	}

	return true
}

func quoteDotenvValue(value string) string {
	if value != "" && strings.IndexFunc(value, func(character rune) bool {
		return !unicode.IsLetter(character) && !unicode.IsDigit(character) &&
			!strings.ContainsRune("-._/:@", character)
	}) < 0 {
		return value
	}
	escaped := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "$", "\\$").Replace(value)

	return "\"" + escaped + "\""
}

func sameLLMEnvState(left, right llmEnvState) bool {
	if left.exists != right.exists || left.valid != right.valid {
		return false
	}
	if !left.exists {
		return true
	}

	return left.mode == right.mode && left.identity == right.identity && left.digest == right.digest
}

func llmSourceWarning(source, reason string) string {
	return source + " skipped: " + reason
}
