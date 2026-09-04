package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/llm"
	"github.com/IceCodeNew/maniud/internal/tui"
)

type tuiAssistantPending struct {
	token                 string
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
	pending     *tuiAssistantPending
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
		return tui.LLMConfiguration{}, &llm.ActionError{Code: llm.ErrorConfigInvalid}
	}
	if err = publishXDGLLMEnvWithOperations(
		assistant.environment, assistant.resolved.baseline, updates, assistant.configOps,
	); err != nil {
		return assistant.recoverLLMSaveFailure(ctx, err)
	}
	resolved, err := assistant.reload(ctx)
	if err != nil {
		return tui.LLMConfiguration{}, &llm.ActionError{Code: tui.LLMConfigSavedReloadFailed}
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
		action, _ := errors.AsType[*llm.ActionError](publicLLMActionError(err))
		action.Stage = llm.ActionStageRequestPreparation
		action.RequestOutcome = llm.RequestNotStarted

		return tui.LLMResult{}, action
	}
	result, err := assistant.session.Recommend(ctx, before.projection, canonical)
	if err != nil {
		action, actionError := errors.AsType[*llm.ActionError](err)
		if actionError && action.Code == llm.ErrorConversationLimit {
			assistant.closeSession()
		}

		return tui.LLMResult{}, publicLLMActionError(err)
	}
	if !assistant.recommendationContextCurrent(
		ctx, request, resolved.identity, before.identity, assistant.deployments.assistContext,
	) {
		assistant.closeSession()

		return tui.LLMResult{}, &llm.ActionError{
			Code: llm.ErrorContextStale, Stage: llm.ActionStageContextValidation,
			RequestOutcome: llm.RequestResponseReceived,
		}
	}
	assistant.pending = &tuiAssistantPending{
		token: result.Token, request: request,
		configurationIdentity: resolved.identity, contextIdentity: before.identity,
	}

	return result, nil
}

func (assistant *tuiAssistant) Accept(ctx context.Context, token string, choice int) error {
	assistant.mu.Lock()
	defer assistant.mu.Unlock()
	pending := assistant.pending
	if pending == nil || pending.token != token || assistant.session == nil {
		return &llm.ActionError{Code: llm.ErrorContextStale}
	}
	if !assistant.recommendationContextCurrent(
		ctx, pending.request, pending.configurationIdentity, pending.contextIdentity,
		assistant.deployments.pendingAssistContext,
	) {
		assistant.closeSession()

		return &llm.ActionError{Code: llm.ErrorContextStale}
	}
	if err := assistant.session.Accept(token, choice); err != nil {
		action, actionError := errors.AsType[*llm.ActionError](err)
		if actionError && action.Code == llm.ErrorConversationLimit {
			assistant.closeSession()
		}

		return publicLLMActionError(err)
	}
	assistant.pending = nil

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

func (assistant *tuiAssistant) recoverLLMSaveFailure(
	ctx context.Context,
	saveErr error,
) (tui.LLMConfiguration, error) {
	if !errors.Is(saveErr, errLLMConfigSaveUnknown) && !errors.Is(saveErr, errLLMConfigSaveStale) {
		return tui.LLMConfiguration{}, publicLLMConfigError(saveErr)
	}
	resolved, reloadErr := assistant.reload(ctx)
	if reloadErr == nil {
		return publicLLMConfiguration(resolved), publicLLMConfigError(saveErr)
	}
	if errors.Is(saveErr, errLLMConfigSaveStale) {
		return tui.LLMConfiguration{}, &llm.ActionError{Code: tui.LLMConfigReloadFailed}
	}

	return tui.LLMConfiguration{}, publicLLMConfigError(saveErr)
}

func (assistant *tuiAssistant) prepareRecommendation(
	ctx context.Context,
	request application.Request,
	configurationIdentity string,
	question string,
) (llmResolvedConfig, tuiAssistContext, string, error) {
	resolved, err := assistant.reload(ctx)
	if err != nil || resolved.identity == "" || resolved.identity != configurationIdentity {
		return llmResolvedConfig{}, tuiAssistContext{}, "", &llm.ActionError{Code: llm.ErrorConfigInvalid}
	}
	canonical, err := llm.CanonicalQuestion(question)
	if err != nil {
		return llmResolvedConfig{}, tuiAssistContext{}, "", publicLLMActionError(err)
	}
	before, err := assistant.deployments.assistContext(ctx, request, assistant.operations)
	if err != nil {
		return llmResolvedConfig{}, tuiAssistContext{}, "", &llm.ActionError{Code: llm.ErrorContextStale}
	}
	forbidden := assistant.forbiddenRecommendationValues(resolved, before)
	if category := forbiddenQuestionCategory(canonical, forbidden); category != "" {
		return llmResolvedConfig{}, tuiAssistContext{}, "", &llm.ActionError{
			Code: llm.ErrorForbiddenValue, Category: category,
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
	assistant.pending = nil
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
		"credential", "environment", "private path", "image reference", "command",
		"port", "mount", "device", "runtime ID",
	} {
		values := forbidden[category]
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value != "" && strings.Contains(question, value) {
				return category
			}
		}
	}

	return ""
}

func publicLLMConfigError(err error) error {
	switch {
	case errors.Is(err, errLLMConfigSaveUnknown):
		return &llm.ActionError{Code: tui.LLMConfigSaveUnknown}
	case errors.Is(err, errLLMConfigSaveStale):
		return &llm.ActionError{Code: tui.LLMConfigSaveStale}
	case errors.Is(err, errLLMConfigPathInvalid):
		return &llm.ActionError{Code: tui.LLMConfigPathInvalid}
	default:
		return &llm.ActionError{Code: llm.ErrorConfigInvalid}
	}
}

func publicLLMActionError(err error) error {
	action, valid := errors.AsType[*llm.ActionError](err)
	if !valid {
		return &llm.ActionError{Code: llm.ErrorProvider}
	}

	return &llm.ActionError{
		Code: action.Code, Category: action.Category,
		Stage: action.Stage, RequestOutcome: action.RequestOutcome,
	}
}
