package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/tui"
)

const (
	tuiTestLocale       = "C.UTF-8"
	languageEnvironment = "LANG"
)

type terminalFixture struct {
	io.Reader
	io.Writer

	descriptor uintptr
}

func (fixture terminalFixture) Fd() uintptr {
	return fixture.descriptor
}

func TestRequireInteractiveTerminal(t *testing.T) {
	t.Parallel()

	input := terminalFixture{Reader: bytes.NewReader(nil), descriptor: 1}
	output := terminalFixture{Writer: io.Discard, descriptor: 2}
	terminal := func(descriptor uintptr) bool { return descriptor == 1 || descriptor == 2 }

	tests := []struct {
		name      string
		input     io.Reader
		output    io.Writer
		term      string
		terminal  func(uintptr) bool
		wantError error
	}{
		{name: "available", input: input, output: output, term: testTerminalName, terminal: terminal},
		{name: "input has no descriptor", input: bytes.NewReader(nil), output: output, terminal: terminal,
			wantError: errTUIInputUnavailable},
		{name: "input is not terminal", input: input, output: output, terminal: func(uintptr) bool { return false },
			wantError: errTUIInputUnavailable},
		{name: "probe missing", input: input, output: output, terminal: nil, wantError: errTUIInputUnavailable},
		{name: "output has no descriptor", input: input, output: io.Discard, terminal: terminal,
			wantError: errTUIOutputUnavailable},
		{name: "output is not terminal", input: input, output: output,
			terminal: func(descriptor uintptr) bool { return descriptor == 1 }, wantError: errTUIOutputUnavailable},
		{name: "dumb terminal", input: input, output: output, term: " DUMB ", terminal: terminal,
			wantError: errTUITermUnavailable},
	}
	for _, test := range tests {
		err := requireInteractiveTerminal(test.input, test.output, test.term, test.terminal)
		if !errors.Is(err, test.wantError) {
			t.Fatalf("requireInteractiveTerminal(%s) error = %v, want %v", test.name, err, test.wantError)
		}
	}
}

func TestTUIOptionsHonorTerminalEnvironment(t *testing.T) {
	t.Parallel()
	const utf8Locale = "en_US.UTF-8"

	tests := []struct {
		environment map[string]string
		want        tui.Options
	}{
		{environment: map[string]string{languageEnvironment: utf8Locale}, want: tui.Options{Color: true, Unicode: true}},
		{environment: map[string]string{"LC_CTYPE": "C.UTF8", "NO_COLOR": ""}, want: tui.Options{Unicode: true}},
		{environment: map[string]string{"LC_ALL": "C", languageEnvironment: utf8Locale}, want: tui.Options{Color: true}},
		{environment: map[string]string{languageEnvironment: utf8Locale, "MANIUD_TUI_ASCII": "1"},
			want: tui.Options{Color: true}},
		{environment: nil, want: tui.Options{Color: true}},
	}
	for _, test := range tests {
		if got := tuiOptions(test.environment); got != test.want {
			t.Fatalf("tuiOptions(%v) = %#v, want %#v", test.environment, got, test.want)
		}
	}
	if unicodeTerminal(map[string]string{languageEnvironment: utf8Locale}, func(string) int { return 2 }) {
		t.Fatal("unicodeTerminal(wide canonical symbol) succeeded")
	}
}

func TestClassifyTUIFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err  error
		code domain.ErrorCode
	}{
		{err: errTUIInputUnavailable, code: domain.ErrorTUIUnavailable},
		{err: errTUIOutputUnavailable, code: domain.ErrorTUIUnavailable},
		{err: errTUITermUnavailable, code: domain.ErrorTUIUnavailable},
		{err: errTUIExportFailed, code: domain.ErrorTUIExportFailed},
		{err: errApplyTest, code: domain.ErrorApplyFailed},
	}
	for _, test := range tests {
		if got := classifyTUIFailure(test.err).Code(); got != test.code {
			t.Fatalf("classifyTUIFailure(%v) = %q, want %q", test.err, got, test.code)
		}
	}
}

func TestWriteTUIExportWritesOnceAndContainsFailures(t *testing.T) {
	t.Parallel()

	output := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	if err := writeTUIExport(output, stderr, "session detail\n"); err != nil ||
		output.String() != "session detail\n" || stderr.Len() != 0 {
		t.Fatalf("writeTUIExport(success) = %q, %q, %v", output.String(), stderr.String(), err)
	}
	if err := writeTUIExport(output, stderr, ""); err != nil {
		t.Fatalf("writeTUIExport(empty) error = %v", err)
	}

	for _, writer := range []io.Writer{failingWriter{}, shortGitOpsCycleWriter{}} {
		stderr.Reset()
		err := writeTUIExport(writer, stderr, "BARK_DEVICE_KEY=secret")
		if !errors.Is(err, errTUIExportFailed) ||
			stderr.String() != "maniud tui: session export could not be written\n" ||
			bytes.Contains(stderr.Bytes(), []byte("secret")) {
			t.Fatalf("writeTUIExport(failure) = %q, %v", stderr.String(), err)
		}
	}
}

func TestWriteTUIInstructionsUsesShellReadyLines(t *testing.T) {
	t.Parallel()

	output := new(bytes.Buffer)
	if err := writeTUIInstructions(output, []string{"git push", tuiCommand}); err != nil ||
		output.String() != "Next steps:\n$ git push\n$ "+tuiCommand+"\n" {
		t.Fatalf("writeTUIInstructions() = %q, %v", output.String(), err)
	}
	if err := writeTUIInstructions(output, nil); err != nil {
		t.Fatalf("writeTUIInstructions(nil) error = %v", err)
	}
	if err := writeTUIInstructions(failingWriter{}, []string{tuiCommand}); err == nil {
		t.Fatal("writeTUIInstructions(write failure) succeeded")
	}
	if err := writeTUIInstructions(
		&failAtWriter{failAt: 2, err: io.ErrClosedPipe},
		[]string{tuiCommand},
	); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("writeTUIInstructions(next-step failure) error = %v", err)
	}
}

func TestExecuteProductionTUIContainsSetupFailures(t *testing.T) {
	t.Parallel()

	input := terminalFixture{Reader: bytes.NewReader([]byte("q")), descriptor: 1}
	output := terminalFixture{Writer: io.Discard, descriptor: 2}
	terminal := func(uintptr) bool { return true }
	workingDirectory := t.TempDir()
	environment := map[string]string{
		homeKey: workingDirectory, xdgStateHomeKey: filepath.Join(workingDirectory, "state"),
		testTermEnvironment: testTerminalName, languageEnvironment: tuiTestLocale,
	}

	tests := []struct {
		name        string
		environment map[string]string
		workingDir  func() (string, error)
		terminal    func(uintptr) bool
		wantError   error
	}{
		{
			name: "terminal", environment: environment,
			workingDir: func() (string, error) { return workingDirectory, nil },
			terminal:   func(uintptr) bool { return false }, wantError: errTUIInputUnavailable,
		},
		{
			name: "notification", environment: map[string]string{
				testTermEnvironment: testTerminalName, barkEncryptionKeyEnvironment: "incomplete",
			},
			workingDir: func() (string, error) { return workingDirectory, nil },
			terminal:   terminal, wantError: errIncompleteBarkConfiguration,
		},
		{
			name: "dependencies", environment: environment,
			workingDir: func() (string, error) { return "", io.ErrClosedPipe },
			terminal:   terminal, wantError: io.ErrClosedPipe,
		},
	}
	for _, test := range tests {
		var notifications processNotifications
		err := executeProductionTUIWith(
			t.Context(), input, output, io.Discard, test.environment, test.workingDir,
			testRuntimePlugins(t), &notifications, test.terminal, productionTUIRunner(nil),
		)
		notifications.Close()
		if !errors.Is(err, test.wantError) {
			t.Fatalf("executeProductionTUI(%s) error = %v, want %v", test.name, err, test.wantError)
		}
	}
}

func TestExecuteProductionTUIRunsHomeAndClassifiesCancellation(t *testing.T) {
	t.Parallel()

	workingDirectory := t.TempDir()
	environment := map[string]string{
		homeKey: workingDirectory, xdgStateHomeKey: filepath.Join(workingDirectory, "state"),
		testTermEnvironment: testTerminalName, languageEnvironment: tuiTestLocale,
	}
	terminal := func(uintptr) bool { return true }
	for _, test := range []struct {
		name      string
		runError  error
		wantError bool
	}{
		{name: "quit"},
		{name: testCancelledName, runError: context.Canceled, wantError: true},
	} {
		var notifications processNotifications
		output := new(bytes.Buffer)
		err := executeProductionTUIWith(
			t.Context(),
			terminalFixture{Reader: bytes.NewReader([]byte("q")), descriptor: 1},
			terminalFixture{Writer: output, descriptor: 2},
			io.Discard,
			environment,
			func() (string, error) { return workingDirectory, nil },
			testRuntimePlugins(t),
			&notifications,
			terminal,
			productionTUIRunner(test.runError),
		)
		notifications.Close()
		if (err != nil) != test.wantError {
			t.Fatalf("executeProductionTUI(%s) error = %v", test.name, err)
		}
	}
}

func TestExecuteProductionTUIWritesPostCommitInstructionsAfterFailure(t *testing.T) {
	t.Parallel()

	workingDirectory := t.TempDir()
	environment := map[string]string{
		homeKey: workingDirectory, xdgStateHomeKey: filepath.Join(workingDirectory, "state"),
		testTermEnvironment: testTerminalName, languageEnvironment: tuiTestLocale,
	}
	output := new(bytes.Buffer)
	var notifications processNotifications
	err := executeProductionTUIWith(
		t.Context(),
		terminalFixture{Reader: bytes.NewReader([]byte("q")), descriptor: 1},
		terminalFixture{Writer: output, descriptor: 2},
		io.Discard,
		environment,
		func() (string, error) { return workingDirectory, nil },
		testRuntimePlugins(t),
		&notifications,
		func(uintptr) bool { return true },
		func(
			_ context.Context,
			_ io.Reader,
			_ io.Writer,
			_ tui.Catalog,
			workspace tui.ServiceWorkspace,
			_ tui.DeploymentWorkspace,
			_ tui.Operations,
			_ *tui.EventStream,
			_ tui.Options,
		) (tui.Result, error) {
			serviceWorkspace, valid := workspace.(*tuiServiceWorkspace)
			if !valid {
				return tui.Result{}, errApplyTest
			}
			serviceWorkspace.instructions = []string{tuiCommand}

			return tui.Result{}, errApplyTest
		},
	)
	notifications.Close()
	if !errors.Is(err, errApplyTest) || output.String() != "Next steps:\n$ "+tuiCommand+"\n" {
		t.Fatalf("executeProductionTUI(post-commit failure) = %q, %v", output.String(), err)
	}
}

func TestExecuteProductionTUIWritesExportAfterTerminalAndBeforeInstructions(t *testing.T) {
	t.Parallel()

	workingDirectory := t.TempDir()
	environment := map[string]string{
		homeKey: workingDirectory, xdgStateHomeKey: filepath.Join(workingDirectory, "state"),
		testTermEnvironment: testTerminalName, languageEnvironment: tuiTestLocale,
	}
	output := new(bytes.Buffer)
	var notifications processNotifications
	err := executeProductionTUIWith(
		t.Context(),
		terminalFixture{Reader: bytes.NewReader([]byte("q")), descriptor: 1},
		terminalFixture{Writer: output, descriptor: 2},
		io.Discard,
		environment,
		func() (string, error) { return workingDirectory, nil },
		testRuntimePlugins(t),
		&notifications,
		func(uintptr) bool { return true },
		func(
			_ context.Context,
			_ io.Reader,
			tuiOutput io.Writer,
			_ tui.Catalog,
			workspace tui.ServiceWorkspace,
			_ tui.DeploymentWorkspace,
			_ tui.Operations,
			_ *tui.EventStream,
			_ tui.Options,
		) (tui.Result, error) {
			serviceWorkspace, valid := workspace.(*tuiServiceWorkspace)
			if !valid {
				return tui.Result{}, errApplyTest
			}
			serviceWorkspace.instructions = []string{tuiCommand}
			_, _ = io.WriteString(tuiOutput, "terminal screen\n")

			return tui.Result{Export: "session export\n"}, nil
		},
	)
	notifications.Close()
	want := "terminal screen\nsession export\nNext steps:\n$ " + tuiCommand + "\n"
	if err != nil || output.String() != want {
		t.Fatalf("executeProductionTUI(export ordering) = %q, %v", output.String(), err)
	}
}

func productionTUIRunner(result error) tuiRunner {
	return func(
		context.Context,
		io.Reader,
		io.Writer,
		tui.Catalog,
		tui.ServiceWorkspace,
		tui.DeploymentWorkspace,
		tui.Operations,
		*tui.EventStream,
		tui.Options,
	) (tui.Result, error) {
		return tui.Result{}, result
	}
}

func TestCombinedEventSinkPublishesAndCountsDrops(t *testing.T) {
	t.Parallel()

	first := &eventSinkFixture{accepted: false, dropped: 2}
	second := &eventSinkFixture{accepted: true, dropped: 3}
	sink := combinedEventSink{first: first, second: second}
	event := application.Event{Kind: application.EventPlanPrepared}
	accepted := sink.TryPublish(event)
	if !accepted || first.events != 1 || second.events != 1 {
		t.Fatalf("TryPublish() = %t, calls = %d/%d", accepted, first.events, second.events)
	}
	if got := sink.DroppedEvents(); got != 5 {
		t.Fatalf("DroppedEvents() = %d", got)
	}
	if (combinedEventSink{}).TryPublish(event) || (combinedEventSink{}).DroppedEvents() != 0 {
		t.Fatal("empty combined sink accepted or counted an event")
	}
	if got := droppedEventCount(eventSinkWithoutCounter{}); got != 0 {
		t.Fatalf("droppedEventCount(non-counter) = %d", got)
	}
}

type eventSinkFixture struct {
	accepted bool
	dropped  uint64
	events   int
}

func (sink *eventSinkFixture) TryPublish(application.Event) bool {
	sink.events++

	return sink.accepted
}

func (sink *eventSinkFixture) DroppedEvents() uint64 {
	return sink.dropped
}

type eventSinkWithoutCounter struct{}

func (eventSinkWithoutCounter) TryPublish(application.Event) bool {
	return false
}

type tuiSignalReader struct {
	ready   <-chan struct{}
	content []byte
}

func (reader *tuiSignalReader) Read(destination []byte) (int, error) {
	select {
	case <-reader.ready:
	case <-time.After(5 * time.Second):
		return 0, fmt.Errorf("wait for TUI render: %w", context.DeadlineExceeded)
	}
	if len(reader.content) == 0 {
		return 0, io.EOF
	}

	count := copy(destination, reader.content)
	reader.content = reader.content[count:]

	return count, nil
}

func TestTUISignalReaderDrainsContent(t *testing.T) {
	t.Parallel()

	ready := make(chan struct{})
	close(ready)
	reader := &tuiSignalReader{ready: ready, content: []byte("q")}
	buffer := make([]byte, 1)
	if count, err := reader.Read(buffer); count != 1 || err != nil || !bytes.Equal(buffer, []byte("q")) {
		t.Fatalf("first Read() = %d, %v, %q", count, err, buffer)
	}
	if count, err := reader.Read(buffer); count != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("second Read() = %d, %v", count, err)
	}
}
