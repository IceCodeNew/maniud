package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/registry"
	dockerruntime "github.com/IceCodeNew/maniud/internal/runtime/docker"
	podmanruntime "github.com/IceCodeNew/maniud/internal/runtime/podman"
	"github.com/IceCodeNew/maniud/internal/store"
)

const (
	maximumComposeSourceBytes  = 1 << 20
	containerHostEnvironment   = "CONTAINER_HOST"
	defaultDockerHost          = "unix:///var/run/docker.sock"
	defaultRootfulPodmanSocket = "/run/podman/podman.sock"
	defaultStateDirectory      = ".local/state"
	podmanSocketDirectory      = "podman"
	podmanSocketName           = "podman.sock"
	stateDatabaseName          = "state.db"
)

var (
	errStateHomeUnavailable = errors.New("state home is unavailable")
	errStateHomeInvalid     = errors.New("state home is invalid")
)

type applyRuntime interface {
	application.Runtime
	CloseIdleConnections()
}

type applyTransactionReader interface {
	application.TransactionReader
	Close() error
}

type applyDependencies struct {
	loadSource  func(context.Context, string) (compose.Source, error)
	openRuntime func(context.Context, domain.RuntimeKind) (applyRuntime, error)
	openReader  func(context.Context) (applyTransactionReader, error)
	openState   func(context.Context) (*store.Store, error)
	mutate      func(context.Context, application.Request, *store.Store, applyRuntime) (application.Plan, error)
	images      application.ImageResolver
}

func defaultApplyDependencies(
	environment map[string]string,
	stderr io.Writer,
	getWorkingDirectory func() (string, error),
) (applyDependencies, error) {
	workingDirectory, err := getWorkingDirectory()
	if err != nil {
		return applyDependencies{}, fmt.Errorf("resolve working directory: %w", err)
	}

	statePath, err := defaultStatePath(environment)
	if err != nil {
		return applyDependencies{}, err
	}

	resolver := registry.NewResolver(registry.Options{
		Credentials: nil, DockerConfigPath: dockerConfigPath(environment),
	})

	return applyDependencies{
		loadSource: func(ctx context.Context, path string) (compose.Source, error) {
			return loadTrackedComposeSource(ctx, path, workingDirectory, environment, filepath.Dir(statePath))
		},
		openRuntime: func(ctx context.Context, runtimeKind domain.RuntimeKind) (applyRuntime, error) {
			switch runtimeKind {
			case domain.RuntimeDocker:
				client, openErr := openDockerApplyRuntime(ctx, environment, stderr)
				if openErr != nil {
					return nil, openErr
				}

				return client, nil
			case domain.RuntimePodman:
				client, openErr := openPodmanApplyRuntime(ctx, environment)
				if openErr != nil {
					return nil, openErr
				}

				return client, nil
			case domain.RuntimeContainerd:
				return nil, application.ErrInvalidRequest
			default:
				return nil, application.ErrInvalidRequest
			}
		},
		openReader: func(ctx context.Context) (applyTransactionReader, error) {
			return store.OpenReader(ctx, statePath)
		},
		openState: func(ctx context.Context) (*store.Store, error) {
			return store.Open(ctx, statePath)
		},
		mutate: func(
			ctx context.Context,
			request application.Request,
			state *store.Store,
			runtime applyRuntime,
		) (application.Plan, error) {
			return application.NewService(resolver, runtime, state).Apply(ctx, request, state, resolver)
		},
		images: resolver,
	}, nil
}

func openDockerApplyRuntime(
	ctx context.Context,
	environment map[string]string,
	stderr io.Writer,
) (*dockerruntime.Client, error) {
	endpoint, err := dockerEndpoint(environment, stderr)
	if err != nil {
		return nil, err
	}
	client, _, err := dockerruntime.Connect(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("connect Docker runtime: %w", err)
	}

	return client, nil
}

func openPodmanApplyRuntime(
	ctx context.Context,
	environment map[string]string,
) (*podmanruntime.Client, error) {
	socketPath, err := podmanSocketPath(environment, os.Geteuid())
	if err != nil {
		return nil, err
	}
	client, _, err := podmanruntime.Connect(ctx, socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect Podman runtime: %w", err)
	}

	return client, nil
}

func executeApply(
	ctx context.Context,
	arguments applyInvocation,
	output io.Writer,
	dependencies applyDependencies,
) error {
	if arguments.dryRun {
		return executeDryRun(ctx, arguments, output, dependencies)
	}

	return executeMutation(ctx, arguments, output, dependencies)
}

func executeDryRun(
	ctx context.Context,
	arguments applyInvocation,
	output io.Writer,
	dependencies applyDependencies,
) error {
	plan, err := prepareApplyPlan(ctx, arguments, dependencies)
	if err != nil {
		return err
	}

	return writeApplyPlan(output, plan)
}

func prepareApplyPlan(
	ctx context.Context,
	arguments applyInvocation,
	dependencies applyDependencies,
) (application.Plan, error) {
	source, err := dependencies.loadSource(ctx, arguments.compose)
	if err != nil {
		return application.Plan{}, fmt.Errorf("load apply source: %w", err)
	}
	runtimeKind, err := applyRuntimeKind(ctx, source, arguments.service)
	if err != nil {
		return application.Plan{}, fmt.Errorf("select apply runtime: %w", err)
	}

	reader, err := dependencies.openReader(ctx)
	if err != nil {
		return application.Plan{}, fmt.Errorf("open apply state: %w", err)
	}

	runtime, err := dependencies.openRuntime(ctx, runtimeKind)
	if err != nil {
		return application.Plan{}, errors.Join(fmt.Errorf("open apply runtime: %w", err), reader.Close())
	}

	plan, runErr := application.NewService(dependencies.images, runtime, reader).DryRun(
		ctx,
		application.Request{Source: source, Service: arguments.service},
	)
	runtime.CloseIdleConnections()

	closeErr := reader.Close()
	if runErr != nil || closeErr != nil {
		return application.Plan{}, errors.Join(runErr, closeErr)
	}

	return plan, nil
}

func executeMutation(
	ctx context.Context,
	arguments applyInvocation,
	output io.Writer,
	dependencies applyDependencies,
) error {
	source, err := dependencies.loadSource(ctx, arguments.compose)
	if err != nil {
		return fmt.Errorf("load apply source: %w", err)
	}
	runtimeKind, err := applyRuntimeKind(ctx, source, arguments.service)
	if err != nil {
		return fmt.Errorf("select apply runtime: %w", err)
	}

	runtime, err := dependencies.openRuntime(ctx, runtimeKind)
	if err != nil {
		return fmt.Errorf("open apply runtime: %w", err)
	}

	state, err := dependencies.openState(ctx)
	if err != nil {
		runtime.CloseIdleConnections()

		return fmt.Errorf("open apply state: %w", err)
	}

	plan, runErr := dependencies.mutate(
		ctx,
		application.Request{Source: source, Service: arguments.service},
		state,
		runtime,
	)
	runtime.CloseIdleConnections()

	closeErr := state.Close()
	if runErr != nil || closeErr != nil {
		return errors.Join(runErr, closeErr)
	}

	return writeApplyPlan(output, plan)
}

type applyPlan struct {
	DesiredDigest string `json:"desired_digest"`
	Image         string `json:"image"`
	Platform      string `json:"platform"`
	Project       string `json:"project"`
	Runtime       string `json:"runtime"`
	Service       string `json:"service"`
	SourceDigest  string `json:"source_digest"`
	Status        string `json:"status"`
}

func writeApplyPlan(output io.Writer, plan application.Plan) error {
	encoded := applyPlan{
		DesiredDigest: plan.Desired.String(),
		Image:         plan.Image.Reference,
		Platform:      platformString(plan.Platform),
		Project:       plan.Project,
		Runtime:       plan.Runtime.String(),
		Service:       plan.Service,
		SourceDigest:  plan.Source.String(),
		Status:        string(plan.Kind),
	}

	err := json.NewEncoder(output).Encode(encoded)
	if err != nil {
		return fmt.Errorf("encode apply plan: %w", err)
	}

	return nil
}

func platformString(platform domain.Platform) string {
	value := platform.OS + "/" + platform.Architecture
	if platform.Variant != "" {
		value += "/" + platform.Variant
	}

	return value
}

func applyRuntimeKind(
	ctx context.Context,
	source compose.Source,
	service string,
) (domain.RuntimeKind, error) {
	project, err := compose.Load(ctx, source)
	if err != nil {
		return "", fmt.Errorf("load Compose runtime metadata: %w", err)
	}
	runtimeKind, err := project.Runtime(service)
	if err != nil {
		return "", fmt.Errorf("select Compose service runtime: %w", err)
	}

	return runtimeKind, nil
}

func defaultStatePath(environment map[string]string) (string, error) {
	root := environment["XDG_STATE_HOME"]
	if root == "" {
		home := environment["HOME"]
		if home == "" {
			return "", errStateHomeUnavailable
		}

		root = filepath.Join(home, defaultStateDirectory)
	}

	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", errStateHomeInvalid
	}

	return filepath.Join(root, "maniud", stateDatabaseName), nil
}

func dockerConfigPath(environment map[string]string) string {
	directory := environment["DOCKER_CONFIG"]
	if directory == "" {
		home := environment["HOME"]
		if home == "" {
			return ""
		}

		directory = filepath.Join(home, ".docker")
	}

	return filepath.Join(directory, "config.json")
}

func dockerEndpoint(environment map[string]string, stderr io.Writer) (dockerruntime.Endpoint, error) {
	host := environment["DOCKER_HOST"]
	if host == "" {
		host = defaultDockerHost
	}

	if socketPath, found := strings.CutPrefix(host, "unix://"); found {
		return configuredDockerEndpoint(dockerruntime.UnixEndpoint(socketPath))
	}

	if strings.HasPrefix(host, "ssh://") {
		return configuredDockerEndpoint(dockerruntime.SSHEndpoint(host, dockerruntime.SSHOptions{
			Auth: dockerruntime.SSHAuth{
				AgentSocket:     environment["SSH_AUTH_SOCK"],
				PrivateKeyFiles: nil,
				Passphrase:      nil,
			},
			HostKeys:         dockerruntime.SSHHostKeys{Files: nil},
			RemoteDockerPath: "",
		}))
	}

	if strings.HasPrefix(host, "tcp://") && environment["DOCKER_TLS_VERIFY"] != "1" {
		if stderr == nil {
			return dockerruntime.Endpoint{}, dockerruntime.ErrWarningDelivery
		}

		return configuredDockerEndpoint(dockerruntime.VPNEndpoint(host, func(warning dockerruntime.Warning) error {
			err := json.NewEncoder(stderr).Encode(struct {
				Code    dockerruntime.WarningCode `json:"code"`
				Message string                    `json:"message"`
			}{Code: warning.Code, Message: warning.Message})
			if err != nil {
				return fmt.Errorf("emit Docker endpoint warning: %w", err)
			}

			return nil
		}))
	}

	return dockerruntime.Endpoint{}, dockerruntime.ErrInvalidEndpoint
}

func podmanSocketPath(environment map[string]string, effectiveUserID int) (string, error) {
	if effectiveUserID < 0 {
		return "", podmanruntime.ErrInvalidEndpoint
	}
	host := environment[containerHostEnvironment]
	if host != "" {
		path, found := strings.CutPrefix(host, "unix://")
		if !found || !validAbsolutePath(path) {
			return "", podmanruntime.ErrInvalidEndpoint
		}

		return path, nil
	}

	runtimeDirectory := environment["XDG_RUNTIME_DIR"]
	if runtimeDirectory != "" {
		if !validAbsolutePath(runtimeDirectory) {
			return "", podmanruntime.ErrInvalidEndpoint
		}

		return filepath.Join(runtimeDirectory, podmanSocketDirectory, podmanSocketName), nil
	}
	if effectiveUserID == 0 {
		return defaultRootfulPodmanSocket, nil
	}

	return filepath.Join(
		"/run/user",
		strconv.Itoa(effectiveUserID),
		podmanSocketDirectory,
		podmanSocketName,
	), nil
}

func validAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, 0)
}

func configuredDockerEndpoint(
	endpoint dockerruntime.Endpoint,
	err error,
) (dockerruntime.Endpoint, error) {
	if err != nil {
		return dockerruntime.Endpoint{}, fmt.Errorf("configure Docker endpoint: %w", err)
	}

	return endpoint, nil
}

func normalizedComposeSourcePath(path, workingDirectory string) (string, bool) {
	if path == "" || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(workingDirectory) {
		return "", false
	}

	absolutePath := path
	if !filepath.IsAbs(absolutePath) {
		absolutePath = filepath.Join(workingDirectory, absolutePath)
	}

	return filepath.Clean(absolutePath), true
}

func environmentMap(values []string) map[string]string {
	environment := make(map[string]string, len(values))
	for _, value := range values {
		name, content, found := strings.Cut(value, "=")
		if found {
			environment[name] = content
		}
	}

	return environment
}

func classifyApplyFailure(err error) *domain.FailureError {
	if errors.Is(err, context.Canceled) || errors.Is(err, registry.ErrCancelled) {
		return domain.OperationCancelled()
	}

	retryable := errors.Is(err, dockerruntime.ErrUnavailable) || errors.Is(err, podmanruntime.ErrUnavailable) ||
		errors.Is(err, registry.ErrUnavailable) ||
		errors.Is(err, registry.ErrRateLimited) || errors.Is(err, store.ErrUnavailable)

	return domain.ApplyFailed(retryable)
}
