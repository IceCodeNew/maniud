package cli

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/tui"
)

type tuiCatalog struct {
	registrationPath string
	loadSource       func(context.Context, string) (compose.Source, error)
	setupRepository  func(context.Context, string, tui.RepositorySetupRequest) error
	suggestedPath    string
	githubHost       string
}

func defaultTUICatalog(
	environment map[string]string,
	loadSource func(context.Context, string) (compose.Source, error),
) *tuiCatalog {
	statePath, err := defaultStatePath(environment)
	if err != nil {
		return &tuiCatalog{loadSource: loadSource, githubHost: environment["GH_HOST"]}
	}
	home := environment["HOME"]
	suggestedPath := ""
	if filepath.IsAbs(home) && filepath.Clean(home) == home {
		suggestedPath = filepath.Join(home, "maniud-desired-state")
	}

	return &tuiCatalog{
		registrationPath: gitOpsRegistrationPath(statePath),
		loadSource:       loadSource,
		setupRepository:  setupTUIRepository,
		suggestedPath:    suggestedPath,
		githubHost:       environment["GH_HOST"],
	}
}

func (catalog *tuiCatalog) Snapshot(ctx context.Context) tui.CatalogSnapshot {
	registration, state := catalog.registration()
	if state != tui.CatalogReady {
		return tui.CatalogSnapshot{State: state, SuggestedRepository: catalog.suggestedPath}
	}

	paths, err := listGitOpsServiceFiles(registration.Repository)
	if err != nil {
		return tui.CatalogSnapshot{State: tui.CatalogUnavailable}
	}

	services := make([]tui.Service, 0, len(paths))
	for _, path := range paths {
		if ctx.Err() != nil {
			return tui.CatalogSnapshot{State: tui.CatalogUnavailable}
		}
		// listGitOpsServiceFiles returns direct children of this registered repository.
		id := filepath.ToSlash(filepath.Join(gitOpsServicesDirectory, filepath.Base(path)))

		service := tui.Service{ID: id, Location: id, Blocker: tui.BlockerInvalid}
		result := catalog.openSource(ctx, path)
		if result.Blocker == tui.BlockerNone && len(result.Targets) == 1 {
			target := result.Targets[0]
			service.Project = target.Project
			service.Name = target.Service
			service.Runtime = target.Runtime
			service.Blocker = tui.BlockerNone
		} else {
			service.Diagnostic = result.Diagnostic
		}
		services = append(services, service)
	}

	return tui.CatalogSnapshot{
		State: tui.CatalogReady, Services: services, SuggestedRepository: catalog.suggestedPath,
	}
}

func (catalog *tuiCatalog) OpenRegistered(ctx context.Context, id string) tui.OpenResult {
	registration, state := catalog.registration()
	if state != tui.CatalogReady {
		return tui.OpenResult{Blocker: tui.BlockerUnavailable}
	}
	root, _, err := inspectLocalGitOpsCheckout(ctx, registration.Repository, registration.Branch)
	if err != nil {
		return tui.OpenResult{Blocker: tui.BlockerUnavailable}
	}
	scope, err := gitOpsRepositoryScope(ctx, registration, root)
	if err != nil {
		return tui.OpenResult{Blocker: tui.BlockerUnavailable}
	}

	paths, err := listGitOpsServiceFiles(root)
	if err != nil {
		return tui.OpenResult{Blocker: tui.BlockerUnavailable}
	}
	for _, path := range paths {
		candidate, valid := registeredTUIServiceID(root, path)
		if valid && candidate == id {
			result := catalog.openRepositorySource(ctx, path, root, scope)
			if result.Blocker != tui.BlockerNone {
				return result
			}
			if len(result.Targets) != 1 {
				return tui.OpenResult{Blocker: tui.BlockerInvalid}
			}

			return result
		}
	}

	return tui.OpenResult{Blocker: tui.BlockerNotFound}
}

func (catalog *tuiCatalog) OpenPath(ctx context.Context, path string) tui.OpenResult {
	result := catalog.openSource(ctx, path)
	if result.Blocker != tui.BlockerNone {
		return result
	}
	for _, target := range result.Targets {
		if target.Request.Source.Repository == nil {
			return tui.OpenResult{Blocker: tui.BlockerInvalid}
		}
	}

	return result
}

func (catalog *tuiCatalog) Register(
	ctx context.Context,
	request tui.RepositorySetupRequest,
) tui.RegistrationResult {
	if catalog.setupRepository == nil || catalog.registrationPath == "" {
		return tui.RegistrationResult{Failure: tui.RepositorySetupUnavailable}
	}
	err := catalog.setupRepository(ctx, catalog.registrationPath, request)
	if err != nil {
		failure := repositorySetupFailure(err)
		recoveryRepository := ""
		if errors.Is(err, errTUIRepositoryCreated) &&
			tuiGitHubRecoveryRemote(request.Remote, catalog.githubHost) != "" {
			recoveryRepository = request.Remote
		}

		return tui.RegistrationResult{Failure: failure, RecoveryRepository: recoveryRepository}
	}

	return tui.RegistrationResult{Snapshot: catalog.Snapshot(ctx)}
}

func repositorySetupFailure(err error) tui.RepositorySetupFailure {
	switch {
	case errors.Is(err, errTUIRepositoryCreateFailed):
		return tui.RepositorySetupGitHubFailed
	case errors.Is(err, errTUIRepositoryCloneFailed):
		return tui.RepositorySetupCloneFailed
	case errors.Is(err, errTUIRepositoryRegistration):
		return tui.RepositorySetupRegistrationFailed
	case errors.Is(err, errGitOpsRepositoryInvalid),
		errors.Is(err, errGitOpsRegistrationExists),
		errors.Is(err, compose.ErrInvalidSource):
		return tui.RepositorySetupInvalidInput
	default:
		return tui.RepositorySetupUnavailable
	}
}

func tuiGitHubRecoveryRemote(repository, configuredHost string) string {
	host := configuredHost
	if host == "" {
		host = "github.com"
	}
	rawOrigin := "https://" + host
	parsed, err := url.Parse(rawOrigin)
	expectedOrigin := (&url.URL{Scheme: "https", Host: host}).String()
	if err != nil || !validGitHubRepository(repository) || parsed.Hostname() == "" ||
		parsed.String() != expectedOrigin {
		return ""
	}
	parsed.Path = "/" + repository + ".git"

	return parsed.String()
}

func (catalog *tuiCatalog) registration() (gitOpsRegistration, tui.CatalogState) {
	if catalog.registrationPath == "" {
		return gitOpsRegistration{}, tui.CatalogUnavailable
	}

	registration, err := readGitOpsRegistration(catalog.registrationPath)
	if errors.Is(err, os.ErrNotExist) {
		return gitOpsRegistration{}, tui.CatalogMissing
	}
	if err != nil {
		return gitOpsRegistration{}, tui.CatalogUnavailable
	}

	return registration, tui.CatalogReady
}

func (catalog *tuiCatalog) openSource(ctx context.Context, path string) tui.OpenResult {
	return catalog.openRepositorySource(ctx, path, "", compose.RepositoryScope{})
}

func (catalog *tuiCatalog) openRepositorySource(
	ctx context.Context,
	path string,
	root string,
	scope compose.RepositoryScope,
) tui.OpenResult {
	if catalog.loadSource == nil {
		return tui.OpenResult{Blocker: tui.BlockerUnavailable}
	}

	source, err := catalog.loadSource(ctx, path)
	if err != nil {
		return tui.OpenResult{
			Blocker: tui.BlockerInvalid, Diagnostic: tuiSourceDiagnostic(source, err),
		}
	}
	project, err := compose.Load(ctx, source)
	if err != nil {
		return tui.OpenResult{
			Blocker: tui.BlockerInvalid, Diagnostic: tuiSourceDiagnostic(source, err),
		}
	}
	provenance := compose.RepositoryProvenance{}
	if scope.Valid() {
		provenance, err = bindApplyRepositorySource(root, path, scope, source)
		if err != nil {
			return tui.OpenResult{Blocker: tui.BlockerInvalid}
		}
	}

	names := project.ServiceNames()
	targets := make([]tui.Target, 0, len(names))
	for _, name := range names {
		runtimeKind, runtimeErr := project.Runtime(name)
		if runtimeErr != nil {
			return tui.OpenResult{Blocker: tui.BlockerInvalid}
		}
		targets = append(targets, tui.Target{
			Project: project.Name(),
			Service: name,
			Runtime: runtimeKind.String(),
			Request: application.Request{Source: source, Service: name, Repository: provenance},
		})
	}
	if len(targets) == 0 {
		return tui.OpenResult{Blocker: tui.BlockerInvalid}
	}

	return tui.OpenResult{Targets: targets}
}

func tuiSourceDiagnostic(source compose.Source, err error) tui.SourceDiagnostic {
	diagnostic, ok := errors.AsType[*compose.SourceDiagnosticError](err)
	if !ok {
		return tui.SourceDiagnostic{}
	}
	var reason tui.SourceDiagnosticReason
	switch diagnostic.Reason {
	case compose.DiagnosticYAMLSyntax:
		reason = tui.DiagnosticYAMLSyntax
	case compose.DiagnosticYAMLStructure:
		reason = tui.DiagnosticYAMLStructure
	case compose.DiagnosticYAMLUnsupported:
		reason = tui.DiagnosticYAMLUnsupported
	case compose.DiagnosticComposeValidation:
		reason = tui.DiagnosticComposeValidation
	default:
		return tui.SourceDiagnostic{}
	}
	file := diagnostic.File
	if file == "" && source.Repository != nil {
		file = source.Repository.Entry
	}
	if file == "" {
		return tui.SourceDiagnostic{}
	}

	return tui.SourceDiagnostic{
		File: file, Reason: reason, Line: diagnostic.Line, Column: diagnostic.Column,
	}
}

func registeredTUIServiceID(root, path string) (string, bool) {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || filepath.IsAbs(relative) ||
		relative == ".." || !filepath.IsLocal(relative) {
		return "", false
	}

	return filepath.ToSlash(relative), true
}
