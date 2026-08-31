package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/llm"
	"github.com/IceCodeNew/maniud/internal/tui"
)

type tuiAssistantPending struct {
	result                llm.Result
	request               application.Request
	configurationIdentity string
	contextIdentity       string
}

type tuiAssistant struct {
	mu          sync.Mutex
	environment map[string]string
	workingDir  string
	deployments *tuiDeploymentWorkspace
	operations  tui.Operations
	configOps   llmConfigOperations
	resolved    llmResolvedConfig
	session     *llm.Session
	sessionID   string
	pending     map[string]tuiAssistantPending
}

func defaultTUIAssistant(
	environment map[string]string,
	workingDirectory string,
	deployments *tuiDeploymentWorkspace,
	operations tui.Operations,
) *tuiAssistant {
	return &tuiAssistant{
		environment: environment, workingDir: workingDirectory,
		deployments: deployments, operations: operations,
		configOps: defaultLLMConfigOperations(),
		pending:   make(map[string]tuiAssistantPending),
	}
}

func (assistant *tuiAssistant) Configuration(ctx context.Context) (tui.LLMConfiguration, error) {
	assistant.mu.Lock()
	defer assistant.mu.Unlock()
	resolved, err := assistant.reload(ctx)
	if err != nil {
		return tui.LLMConfiguration{}, err
	}

	return publicLLMConfiguration(resolved), nil
}

func (assistant *tuiAssistant) Save(
	ctx context.Context,
	settings tui.LLMSettings,
) (tui.LLMConfiguration, error) {
	assistant.mu.Lock()
	defer assistant.mu.Unlock()
	if !assistant.resolved.baseline.initialized {
		if _, err := assistant.reload(ctx); err != nil {
			return tui.LLMConfiguration{}, err
		}
	}
	updates, err := llmSettingsUpdates(settings)
	if err != nil {
		return tui.LLMConfiguration{}, &tui.LLMActionError{Code: tui.LLMConfigInvalid}
	}
	if err = publishXDGLLMEnvWithOperations(
		assistant.environment, assistant.resolved.baseline, updates, assistant.configOps,
	); err != nil {
		if errors.Is(err, errLLMConfigSaveUnknown) {
			resolved, reloadErr := assistant.reload(ctx)
			if reloadErr == nil {
				return publicLLMConfiguration(resolved), publicLLMConfigError(err)
			}
		}

		return tui.LLMConfiguration{}, publicLLMConfigError(err)
	}
	resolved, err := assistant.reload(ctx)
	if err != nil {
		return tui.LLMConfiguration{}, err
	}

	return publicLLMConfiguration(resolved), nil
}

func (assistant *tuiAssistant) Recommend(
	ctx context.Context,
	request application.Request,
	configurationIdentity string,
	question string,
) (tui.LLMResult, error) {
	assistant.mu.Lock()
	defer assistant.mu.Unlock()
	resolved, before, canonical, err := assistant.prepareRecommendation(
		ctx, request, configurationIdentity, question,
	)
	if err != nil {
		return tui.LLMResult{}, err
	}
	result, err := assistant.session.Recommend(ctx, before.projection, canonical)
	if err != nil {
		return tui.LLMResult{}, publicLLMActionError(err)
	}
	if !assistant.recommendationContextCurrent(
		ctx, request, resolved.identity, before.identity, assistant.deployments.assistContext,
	) {
		assistant.closeSession()

		return tui.LLMResult{}, &tui.LLMActionError{Code: tui.LLMContextStale}
	}
	token := rand.Text()
	clear(assistant.pending)
	assistant.pending[token] = tuiAssistantPending{
		result: result, request: request,
		configurationIdentity: resolved.identity, contextIdentity: before.identity,
	}

	return publicLLMResult(token, result), nil
}

func (assistant *tuiAssistant) Accept(ctx context.Context, token string, choice int) error {
	assistant.mu.Lock()
	defer assistant.mu.Unlock()
	pending, found := assistant.pending[token]
	if !found || assistant.session == nil {
		return &tui.LLMActionError{Code: tui.LLMContextStale}
	}
	if !assistant.recommendationContextCurrent(
		ctx, pending.request, pending.configurationIdentity, pending.contextIdentity,
		assistant.deployments.pendingAssistContext,
	) {
		assistant.closeSession()

		return &tui.LLMActionError{Code: tui.LLMContextStale}
	}
	if err := assistant.session.Accept(pending.result, choice); err != nil {
		return publicLLMActionError(err)
	}
	delete(assistant.pending, token)

	return nil
}

// Close releases the active provider session and clears resolved secrets.
func (assistant *tuiAssistant) Close() {
	assistant.mu.Lock()
	defer assistant.mu.Unlock()
	assistant.closeSession()
	assistant.resolved.config.APIKey = ""
	clear(assistant.resolved.secrets)
	assistant.resolved.secrets = nil
}

func (assistant *tuiAssistant) prepareRecommendation(
	ctx context.Context,
	request application.Request,
	configurationIdentity string,
	question string,
) (llmResolvedConfig, tuiAssistContext, string, error) {
	resolved, err := assistant.reload(ctx)
	if err != nil || resolved.identity == "" || resolved.identity != configurationIdentity {
		return llmResolvedConfig{}, tuiAssistContext{}, "", &tui.LLMActionError{Code: tui.LLMConfigInvalid}
	}
	canonical, err := llm.CanonicalQuestion(question)
	if err != nil {
		return llmResolvedConfig{}, tuiAssistContext{}, "", publicLLMActionError(err)
	}
	before, err := assistant.deployments.assistContext(ctx, request, assistant.operations)
	if err != nil {
		return llmResolvedConfig{}, tuiAssistContext{}, "", &tui.LLMActionError{Code: tui.LLMContextStale}
	}
	forbidden := assistant.forbiddenRecommendationValues(resolved, before)
	if category := forbiddenQuestionCategory(canonical, forbidden); category != "" {
		return llmResolvedConfig{}, tuiAssistContext{}, "", &tui.LLMActionError{
			Code: tui.LLMForbiddenValue, Category: category,
		}
	}
	if err = assistant.ensureSession(resolved, before); err != nil {
		return llmResolvedConfig{}, tuiAssistContext{}, "", publicLLMActionError(err)
	}

	return resolved, before, canonical, nil
}

func (assistant *tuiAssistant) forbiddenRecommendationValues(
	resolved llmResolvedConfig,
	before tuiAssistContext,
) map[string][]string {
	forbidden := cloneForbiddenValues(before.forbidden)
	forbidden["credential"] = append(forbidden["credential"], resolved.secrets...)
	if assistant.workingDir != "" {
		forbidden["private path"] = append(forbidden["private path"], assistant.workingDir)
	}
	if configPath, err := llmConfigRootPath(assistant.environment); err == nil {
		forbidden["private path"] = append(forbidden["private path"], configPath)
	}

	return forbidden
}

func (assistant *tuiAssistant) recommendationContextCurrent(
	ctx context.Context,
	request application.Request,
	configurationIdentity string,
	contextIdentity string,
	capture func(context.Context, application.Request, tui.Operations) (tuiAssistContext, error),
) bool {
	after, contextErr := capture(ctx, request, assistant.operations)
	refreshed, configErr := loadLLMConfiguration(ctx, assistant.environment, assistant.workingDir)

	return contextErr == nil && configErr == nil && after.identity == contextIdentity &&
		refreshed.identity == configurationIdentity
}

func (assistant *tuiAssistant) reload(ctx context.Context) (llmResolvedConfig, error) {
	resolved, err := loadLLMConfiguration(ctx, assistant.environment, assistant.workingDir)
	if err != nil {
		return llmResolvedConfig{}, publicLLMConfigError(err)
	}
	if assistant.resolved.identity != "" && assistant.resolved.identity != resolved.identity {
		assistant.closeSession()
	}
	assistant.resolved = resolved

	return resolved, nil
}

func (assistant *tuiAssistant) ensureSession(
	resolved llmResolvedConfig,
	assist tuiAssistContext,
) error {
	identity := resolved.identity + "\x00" + assist.identity
	if assistant.session != nil && assistant.sessionID == identity {
		return nil
	}
	assistant.closeSession()
	session, err := llm.NewSession(resolved.config)
	if err != nil {
		return fmt.Errorf("create LLM session: %w", err)
	}
	assistant.session = session
	assistant.sessionID = identity

	return nil
}

func (assistant *tuiAssistant) closeSession() {
	if assistant.session != nil {
		assistant.session.Close()
	}
	assistant.session = nil
	assistant.sessionID = ""
	clear(assistant.pending)
}

func cloneForbiddenValues(source map[string][]string) map[string][]string {
	clone := make(map[string][]string, len(source))
	for category, values := range source {
		clone[category] = append([]string(nil), values...)
	}

	return clone
}

func forbiddenQuestionCategory(question string, forbidden map[string][]string) string {
	for _, category := range [...]string{
		"credential", "private path", "image reference", "command",
		"port", "mount", "device", "runtime ID",
	} {
		values := forbidden[category]
		for _, value := range values {
			value = strings.TrimSpace(value)
			if len(value) >= 2 && strings.Contains(question, value) {
				return category
			}
		}
	}

	return ""
}

func publicLLMResult(token string, result llm.Result) tui.LLMResult {
	choices := make([]tui.LLMRecommendation, len(result.Choices))
	for index, recommendation := range result.Choices {
		changes := make([]tui.LLMChange, len(recommendation.Changes))
		for changeIndex, change := range recommendation.Changes {
			changes[changeIndex] = tui.LLMChange{
				FieldID: change.FieldID, Value: change.Value, Unset: change.Unset,
			}
		}
		choices[index] = tui.LLMRecommendation{Summary: recommendation.Summary, Changes: changes}
	}

	return tui.LLMResult{
		Token: token, RequestedModel: result.RequestedModel, ReportedModel: result.ReportedModel,
		ModelWarning: result.ModelWarning, Choices: choices,
	}
}

func publicLLMConfigError(err error) error {
	switch {
	case errors.Is(err, errLLMConfigSaveUnknown):
		return &tui.LLMActionError{Code: tui.LLMConfigSaveUnknown}
	case errors.Is(err, errLLMConfigSaveStale):
		return &tui.LLMActionError{Code: tui.LLMConfigSaveStale}
	case errors.Is(err, errLLMConfigPathInvalid):
		return &tui.LLMActionError{Code: tui.LLMConfigPathInvalid}
	default:
		return &tui.LLMActionError{Code: tui.LLMConfigInvalid}
	}
}

//nolint:cyclop // This is the exhaustive mapping between the capability and TUI role codes.
func publicLLMActionError(err error) error {
	action, valid := errors.AsType[*llm.ActionError](err)
	if !valid {
		return &tui.LLMActionError{Code: tui.LLMProviderFailed}
	}
	var code tui.LLMActionCode
	switch action.Code {
	case llm.ErrorConfigInvalid:
		code = tui.LLMConfigInvalid
	case llm.ErrorQuestionInvalid:
		code = tui.LLMQuestionInvalid
	case llm.ErrorForbiddenValue:
		code = tui.LLMForbiddenValue
	case llm.ErrorAuthentication:
		code = tui.LLMAuthenticationFailed
	case llm.ErrorRateLimited:
		code = tui.LLMRateLimited
	case llm.ErrorContextLimit:
		code = tui.LLMContextLimit
	case llm.ErrorRefused:
		code = tui.LLMRefused
	case llm.ErrorEmptyResponse:
		code = tui.LLMEmptyResponse
	case llm.ErrorTruncated:
		code = tui.LLMTruncated
	case llm.ErrorInvalidResponse:
		code = tui.LLMInvalidResponse
	case llm.ErrorModelUnavailable:
		code = tui.LLMModelUnavailable
	case llm.ErrorTimeout:
		code = tui.LLMTimeout
	case llm.ErrorCancelled:
		code = tui.LLMCancelled
	case llm.ErrorContextStale:
		code = tui.LLMContextStale
	case llm.ErrorProvider:
		code = tui.LLMProviderFailed
	default:
		code = tui.LLMProviderFailed
	}

	return &tui.LLMActionError{Code: code, Category: action.Category}
}
