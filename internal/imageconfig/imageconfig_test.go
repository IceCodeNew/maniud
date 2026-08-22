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
		`"os.version":"","os.features":["f1"],"rootfs":{"type":"layers",` +
		`"diff_ids":["sha256:0000000000000000000000000000000000000000000000000000000000000000"]},` +
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
		OSFeatures: []string{"f1"}, DiffIDs: []domain.Digest{{}}, User: "1000", Environment: []string{"PATH=/bin"},
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

	nilValue, err := imageconfig.Decode([]byte(
		`{"rootfs":{"type":"layers","diff_ids":[]},"config":{}}`,
	), 99)
	if err != nil {
		t.Fatal(err)
	}
	emptyValue, err := imageconfig.Decode(
		[]byte(`{"rootfs":{"type":"layers","diff_ids":[]},`+
			`"config":{"Env":[],"Entrypoint":[],"Cmd":[],"ExposedPorts":{},"Volumes":{},"Labels":{}}}`),
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

	got, err := imageconfig.Decode(
		[]byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`),
		128,
	)
	if err != nil || got.Entrypoint != nil || got.Command != nil {
		t.Fatalf("got %#v, %v", got, err)
	}
}

//nolint:funlen // The table keeps every independent image-config rejection boundary together.
func TestDecodeRejectsInvalidDocuments(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		raw []byte
		max int64
	}{
		"nonpositive limit":    {[]byte(`{}`), 0},
		"over limit":           {[]byte(`{"architecture":"amd64"}`), 2},
		"invalid utf8":         {[]byte{'{', '"', 0xff, '"', '}'}, 99},
		"malformed":            {[]byte(`{`), 99},
		"unknown top field":    {[]byte(`{"architecture":"amd64","surprise":1}`), 99},
		"duplicate top field":  {[]byte(`{"os":"linux","os":"linux"}`), 99},
		"missing rootfs":       {[]byte(`{"architecture":"amd64","os":"linux"}`), 99},
		"invalid rootfs type":  {[]byte(`{"rootfs":{"type":"rootfs","diff_ids":[]}}`), 99},
		"missing diff ids":     {[]byte(`{"rootfs":{"type":"layers"}}`), 99},
		"null diff ids":        {[]byte(`{"rootfs":{"type":"layers","diff_ids":null}}`), 99},
		"invalid diff id":      {[]byte(`{"rootfs":{"type":"layers","diff_ids":["sha256:bad"]}}`), 128},
		"unknown rootfs field": {[]byte(`{"rootfs":{"type":"layers","diff_ids":[],"other":true}}`), 128},
		"unknown process field": {[]byte(
			`{"rootfs":{"type":"layers","diff_ids":[]},"config":{"Surprise":"root"}}`,
		), 128},
		"duplicate process field": {[]byte(
			`{"rootfs":{"type":"layers","diff_ids":[]},"config":{"Cmd":[],"Cmd":[]}}`,
		), 128},
		"invalid process utf8": {append(
			[]byte(`{"rootfs":{"type":"layers","diff_ids":[]},"config":{"Cmd":["`),
			append([]byte{0xff}, []byte(`"]}}`)...)...,
		), 128},
		"nul argument": {[]byte(
			`{"rootfs":{"type":"layers","diff_ids":[]},"config":{"Entrypoint":["a\u0000b"]}}`,
		), 128},
		"nul map key": {[]byte(
			`{"rootfs":{"type":"layers","diff_ids":[]},"config":{"Volumes":{"a\u0000b":{}}}}`,
		), 128},
		"nul label value": {[]byte(
			`{"rootfs":{"type":"layers","diff_ids":[]},"config":{"Labels":{"key":"a\u0000b"}}}`,
		), 128},
		"negative healthcheck": {[]byte(
			`{"rootfs":{"type":"layers","diff_ids":[]},"config":{"Healthcheck":{"Timeout":-1}}}`,
		), 128},
		"short healthcheck": {[]byte(
			`{"rootfs":{"type":"layers","diff_ids":[]},` +
				`"config":{"Healthcheck":{"Test":["CMD","true"],"Timeout":1}}}`,
		), 160},
		"invalid health command": {[]byte(
			`{"rootfs":{"type":"layers","diff_ids":[]},"config":{"Healthcheck":{"Test":["true"]}}}`,
		), 128},
		"invalid disabled health": {[]byte(
			`{"rootfs":{"type":"layers","diff_ids":[]},` +
				`"config":{"Healthcheck":{"Test":["NONE"],"Retries":1}}}`,
		), 160},
		"environment without key": {[]byte(
			`{"rootfs":{"type":"layers","diff_ids":[]},"config":{"Env":["=value"]}}`,
		), 128},
		"environment without eq": {[]byte(
			`{"rootfs":{"type":"layers","diff_ids":[]},"config":{"Env":["KEY"]}}`,
		), 128},
		"duplicate environment": {[]byte(
			`{"rootfs":{"type":"layers","diff_ids":[]},"config":{"Env":["KEY=one","KEY=two"]}}`,
		), 160},
		"relative volume": {[]byte(
			`{"rootfs":{"type":"layers","diff_ids":[]},"config":{"Volumes":{"data":{}}}}`,
		), 128},
		"empty label key": {[]byte(
			`{"rootfs":{"type":"layers","diff_ids":[]},"config":{"Labels":{"":"value"}}}`,
		), 128},
		"ambiguous label key": {[]byte(
			`{"rootfs":{"type":"layers","diff_ids":[]},"config":{"Labels":{"key=part":"value"}}}`,
		), 128},
		"escaped Windows args": {[]byte(
			`{"rootfs":{"type":"layers","diff_ids":[]},"config":{"ArgsEscaped":true}}`,
		), 128},
		"oversized field": {[]byte(
			`{"rootfs":{"type":"layers","diff_ids":[]},"config":{"User":"` + strings.Repeat("x", 4097) + `"}}`,
		), 5000},
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

	raw := []byte(`{"rootfs":{"type":"layers","diff_ids":[]},` +
		`"config":{"Cmd":["` + strings.Repeat("x", 2) + `\u0000"]}}`)
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
	withoutRetries := imageconfig.Evidence{Healthcheck: &domain.Healthcheck{
		Test: []string{testHealthCMD, testTrueCommand},
	}}
	if got := withoutRetries.Identity(domain.ImageIdentity{}); got.Healthcheck == nil || got.Healthcheck.Retries != nil {
		t.Fatalf("healthcheck without retries = %#v", got.Healthcheck)
	}
}

func TestDecodePortAndHealthcheckBoundaries(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"rootfs":{"type":"layers","diff_ids":[]},` +
		`"config":{"ExposedPorts":{"53/udp":{},"53/tcp":{},"65535/sctp":{},"1":{}},` +
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
	raw = []byte(`{"rootfs":{"type":"layers","diff_ids":[]},` +
		`"config":{"Healthcheck":{"Test":["CMD","true"]}}}`)
	got, err = imageconfig.Decode(raw, int64(len(raw)))
	if err != nil || got.Healthcheck == nil || got.Healthcheck.Retries != nil {
		t.Fatalf("Decode(healthcheck without retries) = %#v, %v", got, err)
	}

	invalid := []string{"0", "65536/tcp", "abc/tcp", "80/http", "80/tcp/extra", "a\\u0000b"}
	for _, port := range invalid {
		t.Run(port, func(t *testing.T) {
			t.Parallel()

			raw := []byte(`{"rootfs":{"type":"layers","diff_ids":[]},` +
				`"config":{"ExposedPorts":{"` + port + `":{}}}}`)
			if _, err := imageconfig.Decode(raw, int64(len(raw))); !errors.Is(err, imageconfig.ErrInvalid) {
				t.Fatalf("Decode(%q) error = %v, want ErrInvalid", port, err)
			}
		})
	}
}
