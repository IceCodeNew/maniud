package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/registry"
	dockerruntime "github.com/IceCodeNew/maniud/internal/runtime/docker"
	"github.com/IceCodeNew/maniud/internal/store"
)

const (
	maximumComposeSourceBytes = 1 << 20
	defaultDockerHost         = "unix:///var/run/docker.sock"
	defaultStateDirectory     = ".local/state"
	stateDatabaseName         = "state.db"
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
	loadSource  func(string) (compose.Source, error)
	openRuntime func(context.Context) (applyRuntime, error)
	openReader  func(context.Context) (applyTransactionReader, error)
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

	endpoint, err := dockerEndpoint(environment, stderr)
	if err != nil {
		return applyDependencies{}, err
	}

	return applyDependencies{
		loadSource: func(path string) (compose.Source, error) {
			return loadComposeSource(path, workingDirectory, environment)
		},
		openRuntime: func(ctx context.Context) (applyRuntime, error) {
			client, _, connectErr := dockerruntime.Connect(ctx, endpoint)
			if connectErr != nil {
				return nil, fmt.Errorf("connect Docker runtime: %w", connectErr)
			}

			return client, nil
		},
		openReader: func(ctx context.Context) (applyTransactionReader, error) {
			return store.OpenReader(ctx, statePath)
		},
		images: registry.NewResolver(registry.Options{
			Credentials:      nil,
			DockerConfigPath: dockerConfigPath(environment),
		}),
	}, nil
}

func executeDryRun(
	ctx context.Context,
	arguments applyInvocation,
	output io.Writer,
	dependencies applyDependencies,
) error {
	source, err := dependencies.loadSource(arguments.compose)
	if err != nil {
		return fmt.Errorf("load apply source: %w", err)
	}

	reader, err := dependencies.openReader(ctx)
	if err != nil {
		return fmt.Errorf("open apply state: %w", err)
	}

	runtime, err := dependencies.openRuntime(ctx)
	if err != nil {
		return errors.Join(fmt.Errorf("open apply runtime: %w", err), reader.Close())
	}

	plan, runErr := application.NewService(dependencies.images, runtime, reader).DryRun(
		ctx,
		application.Request{Source: source, Service: arguments.service},
	)
	runtime.CloseIdleConnections()

	closeErr := reader.Close()
	if runErr != nil || closeErr != nil {
		return errors.Join(runErr, closeErr)
	}

	return writeDryRunPlan(output, plan)
}

type dryRunPlan struct {
	DesiredDigest string `json:"desired_digest"`
	Image         string `json:"image"`
	Platform      string `json:"platform"`
	Project       string `json:"project"`
	Runtime       string `json:"runtime"`
	Service       string `json:"service"`
	SourceDigest  string `json:"source_digest"`
	Status        string `json:"status"`
}

func writeDryRunPlan(output io.Writer, plan application.Plan) error {
	encoded := dryRunPlan{
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
		return fmt.Errorf("encode dry-run plan: %w", err)
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

func configuredDockerEndpoint(
	endpoint dockerruntime.Endpoint,
	err error,
) (dockerruntime.Endpoint, error) {
	if err != nil {
		return dockerruntime.Endpoint{}, fmt.Errorf("configure Docker endpoint: %w", err)
	}

	return endpoint, nil
}

func loadComposeSource(
	path string,
	workingDirectory string,
	environment map[string]string,
) (compose.Source, error) {
	return loadComposeSourceWithFilesystem(
		path,
		workingDirectory,
		environment,
		composeSourceFilesystem{
			lstat: os.Lstat,
			open: func(name string) (composeSourceFile, error) {
				return os.Open(name) //nolint:gosec // The bounded user-selected Compose file is validated before and after opening.
			},
		},
	)
}

type composeSourceFile interface {
	io.Reader
	Stat() (os.FileInfo, error)
	Close() error
}

type composeSourceFilesystem struct {
	lstat func(string) (os.FileInfo, error)
	open  func(string) (composeSourceFile, error)
}

type openedComposeSource struct {
	file composeSourceFile
	info os.FileInfo
}

func loadComposeSourceWithFilesystem(
	path string,
	workingDirectory string,
	environment map[string]string,
	filesystem composeSourceFilesystem,
) (compose.Source, error) {
	absolutePath, valid := normalizedComposeSourcePath(path, workingDirectory)
	if !valid {
		return compose.Source{}, compose.ErrInvalidSource
	}

	pathInfo, err := filesystem.lstat(absolutePath)
	if err != nil || !validComposeSourceInfo(pathInfo) {
		return compose.Source{}, compose.ErrInvalidSource
	}

	opened, err := openComposeSourceFile(absolutePath, pathInfo, filesystem)
	if err != nil {
		return compose.Source{}, err
	}

	source, readErr := readComposeSource(absolutePath, opened, environment, filesystem)

	return source, errors.Join(readErr, opened.file.Close())
}

func readComposeSource(
	absolutePath string,
	opened openedComposeSource,
	environment map[string]string,
	filesystem composeSourceFilesystem,
) (compose.Source, error) {
	content, err := io.ReadAll(io.LimitReader(opened.file, maximumComposeSourceBytes+1))
	if err != nil || len(content) == 0 || len(content) > maximumComposeSourceBytes {
		return compose.Source{}, compose.ErrInvalidSource
	}

	currentInfo, err := filesystem.lstat(absolutePath)
	if err != nil || !unchangedComposeSource(opened.info, currentInfo, len(content)) {
		return compose.Source{}, compose.ErrInvalidSource
	}

	return compose.Source{
		Content:     content,
		WorkingDir:  filepath.Dir(absolutePath),
		Environment: maps.Clone(environment),
		Profiles:    nil,
	}, nil
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

func validComposeSourceInfo(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Size() > 0 &&
		info.Size() <= maximumComposeSourceBytes
}

func openComposeSourceFile(
	path string,
	expected os.FileInfo,
	filesystem composeSourceFilesystem,
) (openedComposeSource, error) {
	file, err := filesystem.open(path)
	if err != nil {
		return openedComposeSource{}, compose.ErrInvalidSource
	}

	opened, err := file.Stat()
	if err != nil || opened == nil || !os.SameFile(expected, opened) {
		return openedComposeSource{}, errors.Join(compose.ErrInvalidSource, file.Close())
	}

	return openedComposeSource{file: file, info: opened}, nil
}

func unchangedComposeSource(opened, current os.FileInfo, contentSize int) bool {
	return current != nil && os.SameFile(opened, current) && int64(contentSize) == current.Size() &&
		opened.ModTime().Equal(current.ModTime())
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

	retryable := errors.Is(err, dockerruntime.ErrUnavailable) || errors.Is(err, registry.ErrUnavailable) ||
		errors.Is(err, registry.ErrRateLimited) || errors.Is(err, store.ErrUnavailable)

	return domain.ApplyFailed(retryable)
}
