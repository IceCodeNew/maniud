package docker

import "testing"

func TestParseAPIVersion(t *testing.T) {
	t.Parallel()

	emptyVersion := apiVersion{major: 0, minor: 0}
	tests := []struct {
		value string
		want  apiVersion
		valid bool
	}{
		{value: minimumAPIVersion, want: apiVersion{major: 1, minor: 54}, valid: true},
		{value: "2.0", want: apiVersion{major: 2, minor: 0}, valid: true},
		{value: "", want: emptyVersion, valid: false},
		{value: "1", want: emptyVersion, valid: false},
		{value: ".54", want: emptyVersion, valid: false},
		{value: "1.", want: emptyVersion, valid: false},
		{value: "1.54.0", want: emptyVersion, valid: false},
		{value: "01.54", want: emptyVersion, valid: false},
		{value: "1.054", want: emptyVersion, valid: false},
		{value: "x.54", want: emptyVersion, valid: false},
		{value: "1.x", want: emptyVersion, valid: false},
		{value: "18446744073709551616.1", want: emptyVersion, valid: false},
		{value: "1.18446744073709551616", want: emptyVersion, valid: false},
	}

	for _, test := range tests {
		got, valid := parseAPIVersion(test.value)
		if valid != test.valid || got != test.want {
			t.Fatalf("parseAPIVersion(%q) = %#v, %t; want %#v, %t", test.value, got, valid, test.want, test.valid)
		}
	}
}

func TestCompatibleAPIVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		server string
		want   string
		valid  bool
	}{
		{server: testUnsupportedAPIVersion, want: "", valid: false},
		{server: minimumAPIVersion, want: minimumAPIVersion, valid: true},
		{server: maximumAPIVersion, want: maximumAPIVersion, valid: true},
		{server: "1.56", want: maximumAPIVersion, valid: true},
		{server: "2.0", want: maximumAPIVersion, valid: true},
	}

	for _, test := range tests {
		server, valid := parseAPIVersion(test.server)
		if !valid {
			t.Fatalf("parseAPIVersion(%q) failed", test.server)
		}

		got, compatible := compatibleAPIVersion(server)
		if compatible != test.valid || compatible && got.String() != test.want {
			t.Fatalf("compatibleAPIVersion(%q) = %q, %t; want %q, %t", test.server, got, compatible, test.want, test.valid)
		}
	}

	older := apiVersion{major: 1, minor: 54}

	newer := apiVersion{major: 2, minor: 0}
	if !older.Less(newer) || newer.Less(older) || older.Less(older) {
		t.Fatal("apiVersion.Less() ordering is invalid")
	}
}
