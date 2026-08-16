package application

import (
	"context"
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/registry/credential"
	"github.com/IceCodeNew/maniud/internal/store"
)

type testImageRuntime struct {
	events      *[]string
	pulled      domain.ImageIdentity
	auth        credential.Provider
	probe       ImageProbe
	pullErr     error
	probeErr    error
	pullInvoked bool
}

func (runtime *testImageRuntime) PullImage(
	_ context.Context,
	expected domain.ImageIdentity,
	authenticator credential.Provider,
) error {
	*runtime.events = append(*runtime.events, eventEffect)
	runtime.pullInvoked = true
	runtime.pulled = expected
	runtime.auth = authenticator

	return runtime.pullErr
}

func (runtime *testImageRuntime) ProbeImage(
	context.Context,
	domain.ImageIdentity,
) (ImageProbe, error) {
	*runtime.events = append(*runtime.events, eventProbe)

	return runtime.probe, runtime.probeErr
}

type testImageCredentialProvider struct{}

func (*testImageCredentialProvider) Credentials(
	context.Context,
	imageref.Reference,
) (credential.Value, error) {
	return credential.Value{
		Username:     "private username",
		Password:     "private password",
		RefreshToken: "private refresh token",
		AccessToken:  "private access token",
	}, nil
}

func TestRunImagePullFencesEffectAndCompletesFromObservedIdentity(t *testing.T) {
	t.Parallel()

	expected := testImageEffectIdentity(t)
	journal := imageEffectJournal(store.ActionStateIntent)
	authenticator := &testImageCredentialProvider{}
	runtime := observedImageRuntime(&journal.events, expected)
	identifier := store.TransactionID{1}

	got, err := runImagePull(
		context.Background(),
		journal,
		identifier,
		1,
		expected,
		runtime,
		authenticator,
	)
	if err != nil || !got.Satisfied || got.Digest == (domain.Digest{}) {
		t.Fatalf("runImagePull() = %#v, %v", got, err)
	}

	if !runtime.pullInvoked || runtime.pulled != expected || runtime.auth != authenticator {
		t.Fatal("runImagePull() did not pass the expected execution capability")
	}

	if journal.action.Kind != imagePullActionKind ||
		journal.action.IntentDigest != imageEffectDigest(imageEffectIntent, expected) {
		t.Fatalf("image pull intent = %#v", journal.action)
	}

	if !equalEvents(journal.events, newEffectEvents()) {
		t.Fatalf("events = %q, want %q", journal.events, newEffectEvents())
	}
}

func TestRunImagePullRecoveryProbesWithoutCredentialsOrReplay(t *testing.T) {
	t.Parallel()

	expected := testImageEffectIdentity(t)
	journal := imageEffectJournal(store.ActionStateEffectOutcomeUnknown)
	runtime := observedImageRuntime(&journal.events, expected)

	got, err := runImagePull(
		context.Background(),
		journal,
		store.TransactionID{1},
		1,
		expected,
		runtime,
		&testImageCredentialProvider{},
	)
	if err != nil || !got.Satisfied || runtime.pullInvoked {
		t.Fatalf("runImagePull(unknown) = %#v, %v, pull %t", got, err, runtime.pullInvoked)
	}

	wantEvents := []string{eventIntent, eventProbe, eventComplete}
	if !equalEvents(journal.events, wantEvents) {
		t.Fatalf("events = %q, want %q", journal.events, wantEvents)
	}
}

func TestRunImagePullCompletesProvenAbsenceBeforeFailing(t *testing.T) {
	t.Parallel()

	expected := testImageEffectIdentity(t)
	journal := imageEffectJournal(store.ActionStateIntent)
	runtime := &testImageRuntime{
		events:      &journal.events,
		pulled:      emptyImageIdentity(),
		auth:        nil,
		probe:       ImageProbe{State: ImageProbeMissing, Image: emptyImageEvidence()},
		pullErr:     errTestBoundary,
		probeErr:    nil,
		pullInvoked: false,
	}

	got, err := runImagePull(
		context.Background(),
		journal,
		store.TransactionID{1},
		1,
		expected,
		runtime,
		&testImageCredentialProvider{},
	)
	if !errors.Is(err, errTestBoundary) || got.Satisfied || got.Digest == (domain.Digest{}) ||
		journal.action.State != store.ActionStateCompleted {
		t.Fatalf("runImagePull(missing) = %#v, %v", got, err)
	}

	if got.Digest == imageEffectDigest(imageEffectObserved, expected) ||
		got.Digest != imageEffectDigest(imageEffectMissing, expected) {
		t.Fatal("missing image postcondition digest does not identify proven absence")
	}
}

func TestRunImagePullRejectsUnprovenPostconditions(t *testing.T) {
	t.Parallel()

	expected := testImageEffectIdentity(t)
	tests := []struct {
		name     string
		probe    ImageProbe
		probeErr error
		want     error
	}{
		{
			name:     "probe failure",
			probe:    ImageProbe{State: ImageProbeUnknown, Image: emptyImageEvidence()},
			probeErr: errTestBoundary,
			want:     errTestBoundary,
		},
		{
			name:     "unknown",
			probe:    ImageProbe{State: ImageProbeUnknown, Image: emptyImageEvidence()},
			probeErr: nil,
			want:     ErrConflictingState,
		},
		{
			name:     "invalid state",
			probe:    ImageProbe{State: ImageProbeState(99), Image: emptyImageEvidence()},
			probeErr: nil,
			want:     ErrConflictingState,
		},
		{
			name: "mismatched identity",
			probe: ImageProbe{
				State: ImageProbeObserved,
				Image: imageEvidence(expected, func(value *ImageEvidence) {
					value.ImageConfig = domain.Hash([]byte("other image config"))
				}),
			},
			probeErr: nil,
			want:     ErrConflictingState,
		},
		{
			name: "missing with evidence",
			probe: ImageProbe{
				State: ImageProbeMissing,
				Image: imageEvidence(expected, nil),
			},
			probeErr: nil,
			want:     ErrConflictingState,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assertUnprovenImagePostcondition(t, expected, test.probe, test.probeErr, test.want)
		})
	}
}

func assertUnprovenImagePostcondition(
	t *testing.T,
	expected domain.ImageIdentity,
	probe ImageProbe,
	probeErr error,
	want error,
) {
	t.Helper()

	journal := imageEffectJournal(store.ActionStateIntent)
	runtime := &testImageRuntime{
		events:      &journal.events,
		pulled:      emptyImageIdentity(),
		auth:        nil,
		probe:       probe,
		pullErr:     nil,
		probeErr:    probeErr,
		pullInvoked: false,
	}

	got, err := runImagePull(
		context.Background(),
		journal,
		store.TransactionID{1},
		1,
		expected,
		runtime,
		&testImageCredentialProvider{},
	)
	if !errors.Is(err, want) || got != emptyEffectPostcondition() ||
		journal.action.State != store.ActionStateEffectOutcomeUnknown {
		t.Fatalf("runImagePull(unproven) = %#v, %v", got, err)
	}
}

func TestRunImagePullRejectsInvalidExecutionInputs(t *testing.T) {
	t.Parallel()

	expected := testImageEffectIdentity(t)
	journal := imageEffectJournal(store.ActionStateIntent)
	runtime := observedImageRuntime(&journal.events, expected)
	authenticator := &testImageCredentialProvider{}

	tests := []struct {
		name          string
		journal       EffectJournal
		sequence      int64
		expected      domain.ImageIdentity
		runtime       ImageRuntime
		authenticator credential.Provider
	}{
		{name: "journal", journal: nil, sequence: 1, expected: expected, runtime: runtime, authenticator: authenticator},
		{name: "sequence", journal: journal, sequence: 0, expected: expected, runtime: runtime, authenticator: authenticator},
		{
			name:          "image",
			journal:       journal,
			sequence:      1,
			expected:      emptyImageIdentity(),
			runtime:       runtime,
			authenticator: authenticator,
		},
		{name: "runtime", journal: journal, sequence: 1, expected: expected, runtime: nil, authenticator: authenticator},
		{name: "authenticator", journal: journal, sequence: 1, expected: expected, runtime: runtime, authenticator: nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := runImagePull(
				context.Background(),
				test.journal,
				store.TransactionID{1},
				test.sequence,
				test.expected,
				test.runtime,
				test.authenticator,
			)
			if !errors.Is(err, ErrInvalidRequest) || got != emptyEffectPostcondition() {
				t.Fatalf("runImagePull(%s) = %#v, %v", test.name, got, err)
			}
		})
	}
}

func TestImageEffectDigestBindsStableNonSecretIdentity(t *testing.T) {
	t.Parallel()

	expected := testImageEffectIdentity(t)
	intent := imageEffectDigest(imageEffectIntent, expected)
	observed := imageEffectDigest(imageEffectObserved, expected)
	changed := expected
	changed.Platform.Variant = "v2"

	if intent == (domain.Digest{}) || intent == observed ||
		intent == imageEffectDigest(imageEffectIntent, changed) ||
		intent != imageEffectDigest(imageEffectIntent, expected) {
		t.Fatal("image effect digest does not bind its format, state, and image identity")
	}
}

func imageEffectJournal(state store.ActionState) *testEffectJournal {
	return &testEffectJournal{
		events: nil,
		action: store.Action{
			TransactionID:       store.TransactionID{},
			Sequence:            0,
			Kind:                "",
			State:               state,
			IntentDigest:        domain.Digest{},
			PostconditionDigest: nil,
		},
		failures:       make(map[string]error),
		mutateRecord:   nil,
		mutateMark:     nil,
		mutateComplete: nil,
	}
}

func observedImageRuntime(events *[]string, expected domain.ImageIdentity) *testImageRuntime {
	return &testImageRuntime{
		events:      events,
		pulled:      emptyImageIdentity(),
		auth:        nil,
		probe:       ImageProbe{State: ImageProbeObserved, Image: imageEvidence(expected, nil)},
		pullErr:     nil,
		probeErr:    nil,
		pullInvoked: false,
	}
}

func imageEvidence(
	expected domain.ImageIdentity,
	mutate func(*ImageEvidence),
) ImageEvidence {
	evidence := ImageEvidence{
		ReferenceDigest:  expected.ReferenceDigest,
		PlatformManifest: expected.PlatformManifest,
		ImageConfig:      expected.ImageConfig,
		Platform:         expected.Platform,
	}
	if mutate != nil {
		mutate(&evidence)
	}

	return evidence
}

func testImageEffectIdentity(t *testing.T) domain.ImageIdentity {
	t.Helper()

	referenceDigest := domain.Hash([]byte("image effect reference"))

	source, err := imageref.Normalize("example.com/team/api:1")
	if err != nil {
		t.Fatalf("normalize image effect reference: %v", err)
	}

	reference, err := source.Pin(referenceDigest)
	if err != nil {
		t.Fatalf("pin image effect reference: %v", err)
	}

	return domain.ImageIdentity{
		Reference:       reference.String(),
		ReferenceDigest: referenceDigest,
		Platform: domain.Platform{
			OS:           testOperatingSystem,
			Architecture: testArchitectureAMD64,
			Variant:      "",
		},
		PlatformManifest: domain.Hash([]byte("image effect platform manifest")),
		ImageConfig:      domain.Hash([]byte("image effect config")),
	}
}
