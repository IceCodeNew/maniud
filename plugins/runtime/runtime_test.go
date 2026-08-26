package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
)

var errRuntimePluginTest = errors.New("runtime plugin test failure")

func TestNewSetRejectsInvalidPlugins(t *testing.T) {
	t.Parallel()

	valid := testPlugin(domain.RuntimeDocker)
	tests := [][]Plugin{
		{{}},
		{{Kind: domain.RuntimeDocker, Unavailable: func(error) bool { return false }}},
		{{Kind: domain.RuntimeDocker, Open: valid.Open}},
		{valid, valid},
	}
	for _, plugins := range tests {
		if _, err := NewSet(plugins...); !errors.Is(err, ErrInvalidPlugin) {
			t.Fatalf("NewSet(%#v) error = %v", plugins, err)
		}
	}
}

func TestSetSelectsCompiledRuntimeWithoutOpeningIt(t *testing.T) {
	t.Parallel()

	opened := false
	plugin := testPlugin(domain.RuntimeDocker)
	plugin.Open = func(
		context.Context,
		Environment,
		WarningSink,
	) (application.OperationRuntime, error) {
		opened = true

		return nil, errRuntimePluginTest
	}
	set, err := NewSet(plugin)
	if err != nil {
		t.Fatal(err)
	}

	factory, err := set.Select(domain.RuntimeDocker, nil, nil)
	if err != nil || factory == nil || opened {
		t.Fatalf("Select() = %#v, %v; opened = %t", factory, err, opened)
	}
	if _, err = factory(t.Context()); !errors.Is(err, errRuntimePluginTest) || !opened {
		t.Fatalf("factory() error = %v; opened = %t", err, opened)
	}
}

func TestSetRejectsUnknownAndOmittedRuntimes(t *testing.T) {
	t.Parallel()

	set, err := NewSet(testPlugin(domain.RuntimeDocker))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = set.Select(domain.RuntimePodman, nil, nil); !errors.Is(err, ErrNotBuilt) {
		t.Fatalf("Select(omitted) error = %v", err)
	}
	if _, err = set.Select(domain.RuntimeKind("invalid"), nil, nil); !errors.Is(err, application.ErrInvalidRequest) {
		t.Fatalf("Select(invalid) error = %v", err)
	}
}

func TestSetResolvesLocalImagesAndClassifiesAvailability(t *testing.T) {
	t.Parallel()

	plugin := testPlugin(domain.RuntimeContainerd)
	plugin.ResolveLocalImage = func(
		context.Context,
		Environment,
		imageref.Source,
		domain.Platform,
	) (domain.ImageIdentity, error) {
		return domain.ImageIdentity{}, errRuntimePluginTest
	}
	plugin.Unavailable = func(err error) bool { return errors.Is(err, errRuntimePluginTest) }
	set, err := NewSet(plugin)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = set.ResolveLocalImage(
		t.Context(), domain.RuntimeContainerd, nil, imageref.Source{}, domain.Platform{},
	); !errors.Is(err, errRuntimePluginTest) {
		t.Fatalf("ResolveLocalImage() error = %v", err)
	}
	classified := set.Classify(errRuntimePluginTest)
	if !errors.Is(classified, errRuntimePluginTest) || !errors.Is(classified, ErrUnavailable) {
		t.Fatalf("Classify() error = %v", classified)
	}
	if got := set.Classify(nil); got != nil {
		t.Fatalf("Classify(nil) = %v", got)
	}
}

func TestSetRejectsMissingLocalImageCapability(t *testing.T) {
	t.Parallel()

	set, err := NewSet(testPlugin(domain.RuntimeContainerd))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = set.ResolveLocalImage(
		t.Context(), domain.RuntimeContainerd, nil, imageref.Source{}, domain.Platform{},
	); !errors.Is(err, ErrInvalidPlugin) {
		t.Fatalf("ResolveLocalImage() error = %v", err)
	}
}

func testPlugin(kind domain.RuntimeKind) Plugin {
	return Plugin{
		Kind: kind,
		Open: func(
			context.Context,
			Environment,
			WarningSink,
		) (application.OperationRuntime, error) {
			return nil, errRuntimePluginTest
		},
		Unavailable: func(error) bool { return false },
	}
}
