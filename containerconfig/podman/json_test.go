package podman

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestJSONScannerRejectsMalformedNestedValues(t *testing.T) {
	t.Parallel()

	if consumeValue(json.NewDecoder(bytes.NewReader(nil))) {
		t.Fatal("consumeValue(empty) = true")
	}
	if consumeArray(json.NewDecoder(bytes.NewBufferString(`1,]`))) {
		t.Fatal("consumeArray(malformed child) = true")
	}
	if consumeObject(json.NewDecoder(bytes.NewBufferString(`"a"`))) {
		t.Fatal("consumeObject(missing value) = true")
	}
	if consumeClosing(json.NewDecoder(bytes.NewReader(nil)), ']') {
		t.Fatal("consumeClosing(empty) = true")
	}
	brokenKey := json.NewDecoder(bytes.NewBufferString(`{"`))
	if _, err := brokenKey.Token(); err != nil {
		t.Fatal(err)
	}
	if consumeObject(brokenKey) {
		t.Fatal("consumeObject(broken key) = true")
	}
	array := json.NewDecoder(bytes.NewBufferString(`[1]`))
	if _, err := array.Token(); err != nil {
		t.Fatal(err)
	}
	if consumeObject(array) {
		t.Fatal("consumeObject(non-string key) = true")
	}
	if uniqueKeys([]byte(`{"a":`)) {
		t.Fatal("uniqueKeys(incomplete object) = true")
	}
}
