package maniud

import (
	"errors"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/IceCodeNew/maniud/containerconfig"
)

const (
	testArchiveDigest  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testManifestDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testConfigDigest   = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testReference      = "example.com/team/archive:1"
	testServiceName    = "api"
	testInvalidValue   = "invalid"
	testOtherField     = "other"
)

func TestCodecRoundTripsRuntimeAndArchiveProof(t *testing.T) {
	t.Parallel()

	proof := validProof()
	extension := Extension{Services: map[string]Service{
		"archive": {Runtime: RuntimeDocker, ArchiveProof: &proof},
		"worker":  {Runtime: RuntimeContainerd},
	}}
	raw, err := Encode(extension)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	services := requireMapping(t, requireMapping(t, raw)[servicesField])
	archive := requireMapping(t, services["archive"])
	if _, found := archive[runtimeField]; found {
		t.Fatal("Encode() emitted redundant Docker runtime metadata")
	}
	decoded, err := Decode(raw)
	if err != nil || !reflect.DeepEqual(decoded, extension) {
		t.Fatalf("Decode(Encode()) = %#v, %v; want %#v", decoded, err, extension)
	}

	docker, err := Encode(Extension{Services: map[string]Service{testServiceName: {Runtime: RuntimeDocker}}})
	if err != nil {
		t.Fatalf("Encode(Docker runtime) error = %v", err)
	}
	dockerServices := requireMapping(t, requireMapping(t, docker)[servicesField])
	dockerService := requireMapping(t, dockerServices[testServiceName])
	if dockerService[runtimeField] != string(RuntimeDocker) {
		t.Fatalf("Encode(Docker runtime) = %#v", dockerService)
	}
}

func TestDecodeRejectsMalformedExtensionShapes(t *testing.T) {
	t.Parallel()

	proof := validRawProof()
	for _, value := range []any{
		testInvalidValue,
		map[string]any{servicesField: map[string]any{}, testOtherField: true},
		map[string]any{servicesField: testInvalidValue},
		map[string]any{servicesField: map[string]any{}},
		map[string]any{servicesField: map[string]any{"": map[string]any{runtimeField: "docker"}}},
		map[string]any{servicesField: map[string]any{testServiceName: testInvalidValue}},
		map[string]any{servicesField: map[string]any{testServiceName: map[string]any{}}},
		map[string]any{servicesField: map[string]any{testServiceName: map[string]any{testOtherField: true}}},
		map[string]any{servicesField: map[string]any{testServiceName: map[string]any{
			runtimeField: "docker", imageSourceField: proof, testOtherField: true,
		}}},
		map[string]any{servicesField: map[string]any{testServiceName: map[string]any{runtimeField: 1}}},
		map[string]any{servicesField: map[string]any{testServiceName: map[string]any{runtimeField: testInvalidValue}}},
		map[string]any{servicesField: map[string]any{testServiceName: map[string]any{imageSourceField: testInvalidValue}}},
		map[string]any{servicesField: map[string]any{testServiceName: map[string]any{
			imageSourceField: proof, testOtherField: true,
		}}},
	} {
		if _, err := Decode(value); !errors.Is(err, ErrInvalid) {
			t.Errorf("Decode(%#v) error = %v", value, err)
		}
	}
}

func TestEncodeRejectsInvalidTypedExtension(t *testing.T) {
	t.Parallel()

	invalidProof := validProof()
	invalidProof.ArchiveSize = 0
	for _, extension := range []Extension{
		{},
		{Services: map[string]Service{"": {Runtime: RuntimeDocker}}},
		{Services: map[string]Service{testServiceName: {Runtime: Runtime(testInvalidValue)}}},
		{Services: map[string]Service{testServiceName: {Runtime: RuntimeDocker, ArchiveProof: &invalidProof}}},
	} {
		if _, err := Encode(extension); !errors.Is(err, ErrInvalid) {
			t.Errorf("Encode(%#v) error = %v", extension, err)
		}
	}
}

func TestArchiveProofDecoderRejectsMalformedFields(t *testing.T) {
	t.Parallel()

	invalid := make([]any, 0, 16)
	invalid = append(invalid, testInvalidValue, map[string]any{})
	unknown := validRawProof()
	delete(unknown, archiveKindField)
	unknown["unknown"] = archiveKind
	invalid = append(invalid, unknown)

	wrongKind := validRawProof()
	wrongKind[archiveKindField] = "registry"
	invalid = append(invalid, wrongKind)
	for _, field := range []string{
		archiveDigestField, archiveManifestField, archiveReferenceField,
		archivePlatformDigestField, archiveImageConfigField,
	} {
		value := validRawProof()
		value[field] = 1
		invalid = append(invalid, value)
	}
	for field, value := range map[string]any{
		archiveSizeField:        0,
		archiveMemberIndexField: -1,
		archivePlatformField:    "linux/386",
		archiveSelectorField:    1,
		archiveSourceRefField:   1,
	} {
		proof := validRawProof()
		proof[field] = value
		invalid = append(invalid, proof)
	}
	mismatch := validRawProof()
	mismatch[archiveReferenceField] = testArchiveDigest
	invalid = append(invalid, mismatch)
	badSelector := validRawProof()
	badSelector[archiveSelectorField] = "@1"
	invalid = append(invalid, badSelector)

	for _, value := range invalid {
		if _, err := decodeArchiveProof(value); !errors.Is(err, ErrInvalid) {
			t.Errorf("decodeArchiveProof(%#v) error = %v", value, err)
		}
	}
}

func TestArchiveProofValidationBoundaries(t *testing.T) {
	t.Parallel()

	proof := validProof()
	proof.SourceReference = ""
	proof.Selector = "@0"
	encoded, err := encodeArchiveProof(proof)
	if err != nil {
		t.Fatalf("encodeArchiveProof(index) error = %v", err)
	}
	if _, found := encoded[archiveSourceRefField]; found {
		t.Fatal("encodeArchiveProof(index) emitted an empty source reference")
	}

	for _, selector := range []string{"@00", "@1", "busybox"} {
		if validArchiveSelector(selector, 0, "") {
			t.Errorf("validArchiveSelector(%q) accepted", selector)
		}
	}
	if validOptionalSourceReference("busybox") || validOptionalSourceReference(testReference+"@"+testManifestDigest) {
		t.Fatal("validOptionalSourceReference accepted a non-canonical source")
	}
}

func TestArchivePlatformBoundaries(t *testing.T) {
	t.Parallel()

	if _, valid := parsePlatform("linux/386"); valid {
		t.Fatal("parsePlatform accepted an unsupported platform")
	}
	arm64, valid := parsePlatform(linuxARM64Platform)
	if !valid || formatPlatform(arm64) != linuxARM64Platform {
		t.Fatalf("ARM64 platform round trip = %#v, %t", arm64, valid)
	}
	if got := formatPlatform(containerconfig.Platform{OS: "linux", Architecture: "amd64"}); got != "linux/amd64" {
		t.Fatalf("formatPlatform() = %q", got)
	}
	if supportedPlatform(containerconfig.Platform{}) {
		t.Fatal("supportedPlatform accepted the zero platform")
	}
}

func TestArchiveWireScalarHelperBoundaries(t *testing.T) {
	t.Parallel()

	if value, valid := positiveInt64(1, 1); !valid || value != 1 {
		t.Fatalf("positiveInt64() = %d, %t", value, valid)
	}
	if _, valid := positiveInt64(int64(1), 1); valid {
		t.Fatal("positiveInt64 accepted int64 wire data")
	}
	if _, valid := nonnegativeInt("0", 1); valid {
		t.Fatal("nonnegativeInt accepted a string")
	}
	if optionalString(map[string]any{}, "value") == nil ||
		optionalString(map[string]any{"value": ""}, "value") != nil ||
		optionalString(map[string]any{"value": 1}, "value") != nil {
		t.Fatal("optionalString mishandled an optional value")
	}
	if stringValue(1) != "" {
		t.Fatal("stringValue accepted a non-string")
	}
}

func TestArchiveWireMappingAndDigestBoundaries(t *testing.T) {
	t.Parallel()

	if _, valid := exactMapping(testInvalidValue, "key"); valid {
		t.Fatal("exactMapping accepted a scalar")
	}
	if _, valid := digestValue(1); valid {
		t.Fatal("digestValue accepted a non-string")
	}
	if _, valid := digestValue("sha512:" + strings.Repeat("a", 128)); valid {
		t.Fatal("digestValue accepted SHA-512")
	}
}

func TestArchiveMetadataFieldBoundaries(t *testing.T) {
	t.Parallel()

	tooMany := validRawProof()
	tooMany["unknown"] = true
	tooFew := maps.Clone(validRawProof())
	delete(tooFew, archiveKindField)
	delete(tooFew, archiveSelectorField)
	if archiveMetadataFieldsValid(tooMany) || archiveMetadataFieldsValid(tooFew) {
		t.Fatal("archiveMetadataFieldsValid accepted an invalid field count")
	}
}

func requireMapping(t *testing.T, value any) map[string]any {
	t.Helper()

	mapping, valid := value.(map[string]any)
	if !valid {
		t.Fatalf("value is not a string mapping: %#v", value)
	}

	return mapping
}

func validProof() ArchiveProof {
	return ArchiveProof{
		ArchiveDigest: digest.Digest(testArchiveDigest), ArchiveSize: 10240,
		ManifestDigest: digest.Digest(testManifestDigest), MemberIndex: 0,
		Platform: containerconfig.Platform{OS: "linux", Architecture: "amd64"},
		Selector: testReference, SourceReference: testReference,
		ReferenceDigest:        digest.Digest(testManifestDigest),
		PlatformManifestDigest: digest.Digest(testManifestDigest),
		ImageConfigDigest:      digest.Digest(testConfigDigest),
	}
}

func validRawProof() map[string]any {
	return map[string]any{
		archiveKindField: archiveKind, archiveSelectorField: testReference,
		archiveDigestField: testArchiveDigest, archiveSizeField: 10240,
		archiveManifestField: testManifestDigest, archiveMemberIndexField: 0,
		archivePlatformField: "linux/amd64", archiveSourceRefField: testReference,
		archiveReferenceField: testManifestDigest, archivePlatformDigestField: testManifestDigest,
		archiveImageConfigField: testConfigDigest,
	}
}
