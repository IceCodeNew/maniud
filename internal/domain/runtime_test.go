package domain

import "testing"

func TestParseRuntimeKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value             string
		want              RuntimeKind
		valid             bool
		supportsWorkloads bool
	}{
		{value: "docker", want: RuntimeDocker, valid: true, supportsWorkloads: true},
		{value: "podman", want: RuntimePodman, valid: true, supportsWorkloads: true},
		{value: "containerd", want: RuntimeContainerd, valid: true, supportsWorkloads: true},
		{value: "nerdctl", want: "", valid: false, supportsWorkloads: false},
		{value: "", want: "", valid: false, supportsWorkloads: false},
	}

	for _, test := range tests {
		kind, valid := ParseRuntimeKind(test.value)
		if valid != test.valid {
			t.Fatalf("ParseRuntimeKind(%q) valid = %t, want %t", test.value, valid, test.valid)
		}

		if kind != test.want {
			t.Fatalf("ParseRuntimeKind(%q) = %q, want %q", test.value, kind, test.want)
		}

		if kind.SupportsWorkloads() != test.supportsWorkloads {
			t.Fatalf(
				"ParseRuntimeKind(%q).SupportsWorkloads() = %t, want %t",
				test.value,
				kind.SupportsWorkloads(),
				test.supportsWorkloads,
			)
		}

		if kind.String() != string(test.want) {
			t.Fatalf("ParseRuntimeKind(%q).String() = %q, want %q", test.value, kind.String(), test.want)
		}
	}
}
