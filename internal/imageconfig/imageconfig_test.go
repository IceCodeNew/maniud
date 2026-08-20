package imageconfig_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageconfig"
)

const (
	testProtocolTCP = "tcp"
	testDataPath    = "/data"
	testHealthCMD   = "CMD"
	testChanged     = "changed"
	testWorkDir     = "/work"
	testStopSignal  = "SIGTERM"
	testTrueCommand = "true"
)

func TestDecode(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"architecture":"arm64","os":"linux","variant":"v8",` +
		`"os.version":"","os.features":["f1"],"rootfs":{},` +
		`"config":{"User":"1000","Env":["PATH=/bin"],"Entrypoint":["/bin/sh",""],` +
		`"Cmd":["-c","echo ok"],"ExposedPorts":{"8080/tcp":{}},"Volumes":{"/data":{}},` +
		`"WorkingDir":"/work","Labels":{"key":"value"},"StopSignal":"SIGTERM",` +
		`"Healthcheck":{"Test":["CMD","true"],"Interval":1000000,"Retries":1},` +
		`"OnBuild":["RUN true"],"Shell":["/bin/sh","-c"]},` +
		`"author":"a","container":"c","container_config":{},"created":null,` +
		`"docker_version":"1","history":[]}`)
	got, err := imageconfig.Decode(raw, int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	retries := 1
	want := imageconfig.Evidence{
		Platform:   domain.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"},
		OSFeatures: []string{"f1"}, User: "1000", Environment: []string{"PATH=/bin"},
		Entrypoint: []string{"/bin/sh", ""}, Command: []string{"-c", "echo ok"},
		ExposedPorts: []domain.ExposedPort{{TargetPort: 8080, Protocol: testProtocolTCP}},
		Volumes:      []string{testDataPath}, WorkingDirectory: testWorkDir, Labels: []string{"key=value"},
		StopSignal: testStopSignal, Healthcheck: &domain.Healthcheck{
			Test: []string{testHealthCMD, testTrueCommand}, Interval: "1ms", Retries: &retries,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Decode() = %#v, want %#v", got, want)
	}
	got.OSFeatures[0] = testChanged
	again, _ := imageconfig.Decode(raw, int64(len(raw)))
	if again.OSFeatures[0] != "f1" {
		t.Fatal("result aliases decoder storage")
	}
}

//nolint:cyclop // Every assertion protects one nil-versus-empty image default.
func TestDecodePreservesNilAndEmptyCollections(t *testing.T) {
	t.Parallel()

	nilValue, err := imageconfig.Decode([]byte(`{"config":{}}`), 99)
	if err != nil {
		t.Fatal(err)
	}
	emptyValue, err := imageconfig.Decode(
		[]byte(`{"config":{"Env":[],"Entrypoint":[],"Cmd":[],"ExposedPorts":{},"Volumes":{},"Labels":{}}}`),
		200,
	)
	if err != nil {
		t.Fatal(err)
	}
	if nilValue.Environment != nil || nilValue.Entrypoint != nil || nilValue.Command != nil ||
		nilValue.ExposedPorts != nil || nilValue.Volumes != nil || nilValue.Labels != nil {
		t.Fatalf("nil collections = %#v", nilValue)
	}
	if emptyValue.Environment == nil || emptyValue.Entrypoint == nil || emptyValue.Command == nil ||
		emptyValue.ExposedPorts == nil || emptyValue.Volumes == nil || emptyValue.Labels == nil {
		t.Fatalf("empty collections = %#v", emptyValue)
	}
}

func TestDecodeAbsentConfig(t *testing.T) {
	t.Parallel()

	got, err := imageconfig.Decode([]byte(`{"architecture":"amd64","os":"linux","rootfs":{}}`), 99)
	if err != nil || got.Entrypoint != nil || got.Command != nil {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func TestDecodeRejectsInvalidDocuments(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		raw []byte
		max int64
	}{
		"nonpositive limit":       {[]byte(`{}`), 0},
		"over limit":              {[]byte(`{"architecture":"amd64"}`), 2},
		"invalid utf8":            {[]byte{'{', '"', 0xff, '"', '}'}, 99},
		"malformed":               {[]byte(`{`), 99},
		"unknown top field":       {[]byte(`{"architecture":"amd64","surprise":1}`), 99},
		"duplicate top field":     {[]byte(`{"os":"linux","os":"linux"}`), 99},
		"unknown process field":   {[]byte(`{"config":{"Surprise":"root"}}`), 99},
		"duplicate process field": {[]byte(`{"config":{"Cmd":[],"Cmd":[]}}`), 99},
		"invalid process utf8":    {append([]byte(`{"config":{"Cmd":["`), append([]byte{0xff}, []byte(`"]}}`)...)...), 99},
		"nul argument":            {[]byte(`{"config":{"Entrypoint":["a\u0000b"]}}`), 99},
		"nul map key":             {[]byte(`{"config":{"Volumes":{"a\u0000b":{}}}}`), 99},
		"nul label value":         {[]byte(`{"config":{"Labels":{"key":"a\u0000b"}}}`), 99},
		"negative healthcheck":    {[]byte(`{"config":{"Healthcheck":{"Timeout":-1}}}`), 99},
		"short healthcheck":       {[]byte(`{"config":{"Healthcheck":{"Test":["CMD","true"],"Timeout":1}}}`), 99},
		"invalid health command":  {[]byte(`{"config":{"Healthcheck":{"Test":["true"]}}}`), 99},
		"invalid disabled health": {[]byte(`{"config":{"Healthcheck":{"Test":["NONE"],"Retries":1}}}`), 99},
		"environment without key": {[]byte(`{"config":{"Env":["=value"]}}`), 99},
		"environment without eq":  {[]byte(`{"config":{"Env":["KEY"]}}`), 99},
		"duplicate environment":   {[]byte(`{"config":{"Env":["KEY=one","KEY=two"]}}`), 99},
		"relative volume":         {[]byte(`{"config":{"Volumes":{"data":{}}}}`), 99},
		"empty label key":         {[]byte(`{"config":{"Labels":{"":"value"}}}`), 99},
		"escaped Windows args":    {[]byte(`{"config":{"ArgsEscaped":true}}`), 99},
		"oversized field":         {[]byte(`{"config":{"User":"` + strings.Repeat("x", 4097) + `"}}`), 5000},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := imageconfig.Decode(tc.raw, tc.max)
			if !errors.Is(err, imageconfig.ErrInvalid) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestDecodeRejectsInvalidCommandArgument(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"config":{"Cmd":["` + strings.Repeat("x", 2) + `\u0000"]}}`)
	if _, err := imageconfig.Decode(raw, int64(len(raw))); !errors.Is(err, imageconfig.ErrInvalid) {
		t.Fatalf("got %v", err)
	}
}

func TestEvidenceIdentityClonesConfiguration(t *testing.T) {
	t.Parallel()

	retries := 2
	evidence := imageconfig.Evidence{
		User: "1000", Environment: []string{"A=1"}, Entrypoint: []string{"entry"}, Command: []string{"cmd"},
		ExposedPorts: []domain.ExposedPort{{TargetPort: 80, Protocol: testProtocolTCP}}, Volumes: []string{testDataPath},
		WorkingDirectory: testWorkDir, Labels: []string{"a=b"}, StopSignal: testStopSignal,
		Healthcheck: &domain.Healthcheck{Test: []string{testHealthCMD, testTrueCommand}, Retries: &retries},
	}
	wantEvidence := imageconfig.Evidence{
		User: "1000", Environment: []string{"A=1"}, Entrypoint: []string{"entry"}, Command: []string{"cmd"},
		ExposedPorts: []domain.ExposedPort{{TargetPort: 80, Protocol: testProtocolTCP}}, Volumes: []string{testDataPath},
		WorkingDirectory: testWorkDir, Labels: []string{"a=b"}, StopSignal: testStopSignal,
		Healthcheck: &domain.Healthcheck{Test: []string{testHealthCMD, testTrueCommand}, Retries: &retries},
	}
	identity := evidence.Identity(domain.ImageIdentity{Reference: "preserved"})
	identity.Environment[0], identity.Entrypoint[0], identity.Command[0] = testChanged, testChanged, testChanged
	identity.ExposedPorts[0].TargetPort, identity.Volumes[0], identity.Labels[0] = 81, "/changed", "changed=yes"
	identity.Healthcheck.Test[0], *identity.Healthcheck.Retries = testChanged, 9
	if !reflect.DeepEqual(evidence, wantEvidence) || identity.Reference != "preserved" {
		t.Fatalf("Identity() did not clone or preserve identity: evidence=%#v identity=%#v", evidence, identity)
	}
	if got := (imageconfig.Evidence{}).Identity(domain.ImageIdentity{}); got.Healthcheck != nil {
		t.Fatalf("nil healthcheck became non-nil: %#v", got)
	}
}

func TestDecodePortAndHealthcheckBoundaries(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"config":{"ExposedPorts":{"53/udp":{},"53/tcp":{},"65535/sctp":{},"1":{}},` +
		`"Healthcheck":{"Test":["NONE"]}}}`)
	got, err := imageconfig.Decode(raw, int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	wantPorts := []domain.ExposedPort{
		{TargetPort: 1, Protocol: testProtocolTCP}, {TargetPort: 53, Protocol: testProtocolTCP},
		{TargetPort: 53, Protocol: "udp"}, {TargetPort: 65535, Protocol: "sctp"}}
	if !reflect.DeepEqual(got.ExposedPorts, wantPorts) || got.Healthcheck == nil || !got.Healthcheck.Disabled {
		t.Fatalf("Decode() = %#v", got)
	}

	invalid := []string{"0", "65536/tcp", "abc/tcp", "80/http", "80/tcp/extra", "a\\u0000b"}
	for _, port := range invalid {
		t.Run(port, func(t *testing.T) {
			t.Parallel()

			raw := []byte(`{"config":{"ExposedPorts":{"` + port + `":{}}}}`)
			if _, err := imageconfig.Decode(raw, int64(len(raw))); !errors.Is(err, imageconfig.ErrInvalid) {
				t.Fatalf("Decode(%q) error = %v, want ErrInvalid", port, err)
			}
		})
	}
}
