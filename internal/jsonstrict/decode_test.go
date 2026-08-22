package jsonstrict

import (
	"strings"
	"testing"
)

func TestReadPreservesValidJSON(t *testing.T) {
	t.Parallel()

	for _, want := range [][]byte{
		[]byte(`{"known":1,"extra":{"value":true}}`),
		[]byte(`[{"known":1},2]`),
		[]byte(`true`),
	} {
		got, valid := Read(strings.NewReader(string(want)), int64(len(want)))
		if !valid || string(got) != string(want) {
			t.Fatalf("Read() = %q, %t", got, valid)
		}
	}
}

func TestReadRejectsInvalidJSON(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		`{"duplicate":1,"duplicate":2}`,
		`{"nested":{"duplicate":1,"duplicate":2}}`,
		`{"trailing":true}{}`,
		`{"incomplete":`,
		`{"wrong closing"]`,
		`["wrong closing"}`,
		`[{"duplicate":1,"duplicate":2}]`,
		`{"key"}`,
		`{"`,
	} {
		if got, valid := Read(strings.NewReader(value), int64(len(value))); valid || got != nil {
			t.Fatalf("Read(%q) = %q, %t", value, got, valid)
		}
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	type document struct {
		Known bool `json:"known"`
	}

	var got document
	if !Decode(strings.NewReader(`{"known":true}`), 14, &got) || !got.Known {
		t.Fatalf("Decode(valid) = %#v", got)
	}

	if Decode(strings.NewReader(`{"unknown":true}`), 16, &got) {
		t.Fatal("Decode(unknown) succeeded")
	}

	if Decode(strings.NewReader(`{`), 1, &got) {
		t.Fatal("Decode(invalid) succeeded")
	}
}

func TestReadRejectsOversizedOrUnreadableInput(t *testing.T) {
	t.Parallel()

	if got, valid := Read(strings.NewReader(`{}`), 1); valid || got != nil {
		t.Fatalf("Read(oversized) = %q, %t", got, valid)
	}

	if got, valid := Read(failingReader{}, 1); valid || got != nil {
		t.Fatalf("Read(failing reader) = %q, %t", got, valid)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errReadFixture
}

type fixtureError string

func (err fixtureError) Error() string {
	return string(err)
}

const errReadFixture fixtureError = "read fixture failed"
