package cli

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/IceCodeNew/maniud/internal/compose"
)

const (
	maximumGitOutputBytes = maximumComposeSourceBytes
	gitCommandTimeout     = 30 * time.Second
)

type boundedGitOutput struct {
	buffer   bytes.Buffer
	exceeded bool
}

func (output *boundedGitOutput) Write(value []byte) (int, error) {
	if len(value) > maximumGitOutputBytes-output.buffer.Len() {
		output.exceeded = true

		return 0, compose.ErrInvalidSource
	}

	written, _ := output.buffer.Write(value)

	return written, nil
}

func (output *boundedGitOutput) bytes() []byte {
	return output.buffer.Bytes()
}

func runGit(ctx context.Context, directory string, arguments ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()

	base := []string{
		"--no-pager",
		"--no-replace-objects",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "credential.helper=",
		"-c", "credential.interactive=false",
		"-c", "core.askPass=",
		"-c", "protocol.allow=never",
		"-c", "protocol.file.allow=always",
		"-c", "protocol.https.allow=always",
		"-C", directory,
	}
	commandArguments := make([]string, 0, len(base)+len(arguments))
	commandArguments = append(commandArguments, base...)
	commandArguments = append(commandArguments, arguments...)
	command := exec.CommandContext( //nolint:gosec // Git and every production subcommand are fixed by callers.
		ctx,
		"git",
		commandArguments...,
	)
	command.Env = []string{
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"LANG=C",
		"LC_ALL=C",
		"PATH=" + os.Getenv("PATH"),
	}
	output := new(boundedGitOutput)
	command.Stdout = output
	command.Stderr = nil
	if err := command.Run(); err != nil || output.exceeded {
		return nil, compose.ErrInvalidSource
	}

	return output.bytes(), nil
}

func validateGitProcessConfiguration(ctx context.Context, root string) error {
	output, err := runGit(
		ctx,
		root,
		"config", "--no-includes", "--show-scope", "--name-only", "--null", "--list",
	)
	if err != nil {
		return compose.ErrInvalidSource
	}
	fields, valid := splitNullTerminated(output)
	if !valid || len(fields)%2 != 0 {
		return compose.ErrInvalidSource
	}
	for index := 0; index < len(fields); index += 2 {
		scope := string(fields[index])
		if (scope == "local" || scope == "worktree") && hostileGitConfigKey(string(fields[index+1])) {
			return compose.ErrInvalidSource
		}
	}

	return nil
}

func splitNullTerminated(value []byte) ([][]byte, bool) {
	if len(value) == 0 {
		return nil, true
	}
	if value[len(value)-1] != 0 {
		return nil, false
	}

	return bytes.Split(value[:len(value)-1], []byte{0}), true
}

func hostileGitConfigKey(value string) bool {
	key := strings.ToLower(value)
	if key == "include.path" || strings.HasPrefix(key, "includeif.") || strings.HasPrefix(key, "filter.") {
		return true
	}

	return strings.HasPrefix(key, "url.") &&
		(strings.HasSuffix(key, ".insteadof") || strings.HasSuffix(key, ".pushinsteadof"))
}

func validGitRemoteURL(value string) bool {
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value) == value
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}

	return validParsedGitRemoteURL(parsed)
}

func validParsedGitRemoteURL(parsed *url.URL) bool {
	if parsed.Opaque != "" || parsed.Fragment != "" {
		return false
	}
	switch parsed.Scheme {
	case "https":
		return parsed.Host != ""
	case "file":
		return (parsed.Host == "" || parsed.Host == "localhost") &&
			parsed.RawQuery == "" && filepath.IsAbs(filepath.FromSlash(parsed.Path))
	default:
		return false
	}
}
