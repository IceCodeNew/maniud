package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const testFileName = "file"

var errArchiveReadTest = errors.New("archive read test failure")

type tarMember struct {
	header tar.Header
	body   string
}

func makeTar(t *testing.T, members ...tarMember) []byte {
	t.Helper()
	var output bytes.Buffer
	w := tar.NewWriter(&output)
	for _, member := range members {
		header := member.header
		if header.Mode == 0 {
			header.Mode = 0o640
		}
		if header.Typeflag == tar.TypeDir && member.header.Mode == 0 {
			header.Mode = 0o750
		}
		header.Size = int64(len(member.body))
		if err := w.WriteHeader(&header); err != nil {
			t.Fatalf("WriteHeader(%q): %v", header.Name, err)
		}
		if _, err := io.WriteString(w, member.body); err != nil {
			t.Fatalf("Write(%q): %v", header.Name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close tar: %v", err)
	}

	return output.Bytes()
}

func regular(name, body string) tarMember {
	return tarMember{header: tar.Header{Name: name, Typeflag: tar.TypeReg, ModTime: time.Unix(123, 456)}, body: body}
}

func analyzeBytes(t *testing.T, value []byte) (Inventory, error) {
	t.Helper()

	return Analyze(context.Background(), bytes.NewReader(value), int64(len(value)))
}

func TestAnalyzeInventoryAndSemanticIdentity(t *testing.T) {
	t.Parallel()

	members := []tarMember{
		{header: tar.Header{Name: "dir/", Typeflag: tar.TypeDir}},
		regular("dir/data", "payload"),
		{header: tar.Header{Name: "alias", Typeflag: tar.TypeSymlink, Linkname: "dir/data"}},
		{header: tar.Header{Name: "hard", Typeflag: tar.TypeLink, Linkname: "dir/data"}},
	}
	firstBytes := makeTar(t, members...)
	first, err := analyzeBytes(t, firstBytes)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if first.EntryCount != 4 || first.PayloadBytes != 7 || first.ArchiveBytes != int64(len(firstBytes)) {
		t.Fatalf("unexpected inventory: %+v", first)
	}
	wantRaw := sha256.Sum256(firstBytes)
	if first.ArchiveDigest != domain.Digest(wantRaw) {
		t.Errorf("raw digest = %x, want %x", first.ArchiveDigest, wantRaw)
	}

	// Reordering independent members and changing tar padding changes the stream,
	// but not the restorable inventory.
	secondBytes := append(makeTar(t, members[0], members[1], members[3], members[2]), make([]byte, 512)...)
	second, err := analyzeBytes(t, secondBytes)
	if err != nil {
		t.Fatalf("Analyze reordered archive: %v", err)
	}
	if !SameContent(first, second) {
		t.Errorf("semantic inventories differ:\n%+v\n%+v", first, second)
	}
	if first.ArchiveDigest == second.ArchiveDigest {
		t.Error("different tar streams have equal raw digest")
	}
}

func TestAnalyzeAcceptsTypeRegA(t *testing.T) {
	t.Parallel()

	kind, link, valid := new(archiveScan).entryIdentity(testFileName, &tar.Header{Typeflag: tarTypeRegularLegacy})
	if !valid || kind != entryRegular || link != "" {
		t.Fatalf("entryIdentity(TypeRegA) = %d, %q, %t", kind, link, valid)
	}
	archive := makeTar(t, tarMember{
		header: tar.Header{Name: testFileName, Typeflag: tarTypeRegularLegacy}, body: "x",
	})
	got, err := analyzeBytes(t, archive)
	if err != nil {
		t.Fatalf("TypeRegA is a regular file: %v", err)
	}
	if got.EntryCount != 1 || got.PayloadBytes != 1 {
		t.Fatalf("unexpected inventory: %+v", got)
	}
}

func TestSameContentRejectsInvalidInventories(t *testing.T) {
	t.Parallel()

	archive := makeTar(t, regular(testFileName, "x"))
	valid, err := analyzeBytes(t, archive)
	if err != nil {
		t.Fatal(err)
	}
	if !SameContent(valid, valid) {
		t.Error("inventory must match itself")
	}
	negativeEntries := valid
	negativeEntries.EntryCount = -1
	excessEntries := valid
	excessEntries.EntryCount = maximumArchiveEntries + 1
	negativePayload := valid
	negativePayload.PayloadBytes = -1
	shortArchive := valid
	shortArchive.ArchiveBytes = tarTerminatorBytes - 1
	missingArchiveDigest := valid
	missingArchiveDigest.ArchiveDigest = domain.Digest{}
	missingSemanticDigest := valid
	missingSemanticDigest.SemanticDigest = domain.Digest{}
	differentPayload := valid
	differentPayload.PayloadBytes++
	differentSemanticDigest := valid
	differentSemanticDigest.SemanticDigest[0] ^= 1
	mutations := []Inventory{
		{}, negativeEntries, excessEntries, negativePayload, shortArchive,
		missingArchiveDigest, missingSemanticDigest, differentPayload, differentSemanticDigest,
	}
	for index, mutation := range mutations {
		if SameContent(valid, mutation) {
			t.Errorf("mutation %d unexpectedly matches", index)
		}
	}
}

func TestAnalyzeInputAndResourceLimits(t *testing.T) {
	t.Parallel()

	archive := makeTar(t, regular(testFileName, "body"))
	tests := []struct {
		name string
		r    io.Reader
		max  int64
		want error
	}{
		{"nil source", nil, 1, ErrInvalidArchive},
		{"zero maximum", bytes.NewReader(archive), 0, ErrInvalidArchive},
		{"negative maximum", bytes.NewReader(archive), -1, ErrInvalidArchive},
		{"byte limit", bytes.NewReader(archive), int64(len(archive) - 1), ErrArchiveLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := Analyze(context.Background(), test.r, test.max)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Analyze(cancelled, bytes.NewReader(archive), int64(len(archive)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v", err)
	}

	scan := newArchiveScan()
	scan.entries = make([]semanticEntry, maximumArchiveEntries)
	err = scan.record(
		&tar.Header{Name: "extra", Typeflag: tar.TypeReg},
		strings.NewReader(""),
	)
	if !errors.Is(err, ErrArchiveLimit) {
		t.Errorf("entry limit error = %v", err)
	}
	scan = newArchiveScan()
	scan.payloadBytes = 1<<63 - 1
	if _, err := scan.recordPayload(entryRegular, 1, strings.NewReader("x")); !errors.Is(err, ErrArchiveLimit) {
		t.Errorf("payload overflow error = %v", err)
	}
}

func TestAnalyzeRequiresStrictTerminatorAndPadding(t *testing.T) {
	t.Parallel()

	valid := makeTar(t, regular(testFileName, "x"))
	tests := []struct {
		name  string
		value []byte
	}{
		{"empty", nil},
		{"one terminator block", make([]byte, 512)},
		{"truncated body", valid[:600]},
		{"non-block length", valid[:len(valid)-1]},
		{"trailing garbage", append(append([]byte(nil), valid...), 1)},
		{"garbage block", append(append([]byte(nil), valid...), append([]byte{1}, make([]byte, 511)...)...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := Analyze(context.Background(), bytes.NewReader(test.value), int64(len(test.value))+1)
			if !errors.Is(err, ErrInvalidArchive) {
				t.Fatalf("error = %v, want ErrInvalidArchive", err)
			}
		})
	}
}

func TestAnalyzeRejectsUnsafePathsAndLinks(t *testing.T) {
	t.Parallel()

	tooLong := strings.Repeat("x", maximumPathBytes+1)
	invalidUTF8 := string([]byte{0xff})
	tests := []struct {
		name    string
		members []tarMember
	}{
		{"empty", []tarMember{{header: tar.Header{Typeflag: tar.TypeReg}}}},
		{"absolute", []tarMember{regular("/x", "")}},
		{"dot", []tarMember{regular("a/../x", "")}},
		{"parent", []tarMember{regular("../x", "")}},
		{"backslash", []tarMember{regular(`a\b`, "")}},
		{"nul", []tarMember{regular("a\x00b", "")}},
		{"long", []tarMember{regular(tooLong, "")}},
		{"invalid utf8", []tarMember{regular(invalidUTF8, "")}},
		{"duplicate", []tarMember{regular("x", ""), regular("x", "")}},
		{"nfc nfd", []tarMember{regular("x\u00e9", ""), regular("xe\u0301", "")}},
		{"full case fold", []tarMember{regular("Straße", ""), regular("STRASSE", "")}},
		{"file ancestor", []tarMember{regular("a", ""), regular("a/b", "")}},
		{"descendant first", []tarMember{regular("a/b", ""), regular("a", "")}},
		{"symlink escape", []tarMember{{header: tar.Header{Name: "a/link", Typeflag: tar.TypeSymlink, Linkname: "../../x"}}}},
		{"hardlink escape", []tarMember{{header: tar.Header{Name: "link", Typeflag: tar.TypeLink, Linkname: "../x"}}}},
		{
			"forward hardlink",
			[]tarMember{
				{header: tar.Header{Name: "link", Typeflag: tar.TypeLink, Linkname: "target"}},
				regular("target", ""),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			scan := newArchiveScan()
			var err error
			for _, member := range test.members {
				header := member.header
				header.Size = int64(len(member.body))
				err = scan.record(&header, strings.NewReader(member.body))
				if err != nil {
					break
				}
			}
			if !errors.Is(err, ErrInvalidArchive) {
				t.Fatalf("error = %v, want ErrInvalidArchive", err)
			}
		})
	}
}

func TestRecordRejectsUnsupportedTypesAndMetadata(t *testing.T) {
	t.Parallel()

	invalidUTF8 := string([]byte{0xff})
	tests := []struct {
		name   string
		header tar.Header
	}{
		{"nil header", tar.Header{}},
		{"special", tar.Header{Name: "x", Typeflag: tar.TypeChar}},
		{"sparse", tar.Header{Name: "x", Typeflag: tar.TypeGNUSparse}},
		{"regular linkname", tar.Header{Name: "x", Typeflag: tar.TypeReg, Linkname: "y"}},
		{"directory linkname", tar.Header{Name: "x/", Typeflag: tar.TypeDir, Linkname: "y"}},
		{"nonregular size", tar.Header{Name: "x", Typeflag: tar.TypeDir, Size: 1}},
		{"negative mode", tar.Header{Name: "x", Typeflag: tar.TypeReg, Mode: -1}},
		{"large mode", tar.Header{Name: "x", Typeflag: tar.TypeReg, Mode: 0o10000}},
		{"negative uid", tar.Header{Name: "x", Typeflag: tar.TypeReg, Uid: -1}},
		{"negative gid", tar.Header{Name: "x", Typeflag: tar.TypeReg, Gid: -1}},
		{"device", tar.Header{Name: "x", Typeflag: tar.TypeReg, Devmajor: 1}},
		{"invalid uname", tar.Header{Name: "x", Typeflag: tar.TypeReg, Uname: invalidUTF8}},
		{"long gname", tar.Header{Name: "x", Typeflag: tar.TypeReg, Gname: strings.Repeat("g", maximumPathBytes+1)}},
		{"empty pax key", tar.Header{Name: "x", Typeflag: tar.TypeReg, PAXRecords: map[string]string{"": "v"}}},
		{"empty pax value", tar.Header{Name: "x", Typeflag: tar.TypeReg, PAXRecords: map[string]string{"k": ""}}},
		{"sparse pax", tar.Header{Name: "x", Typeflag: tar.TypeReg, PAXRecords: map[string]string{"GNU.sparse.map": "1"}}},
		{
			"long pax",
			tar.Header{
				Name: "x", Typeflag: tar.TypeReg,
				PAXRecords: map[string]string{"k": strings.Repeat("v", maximumPathBytes+1)},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			scan := newArchiveScan()
			var header *tar.Header
			if test.name != "nil header" {
				header = &test.header
			}
			if err := scan.record(header, strings.NewReader("x")); !errors.Is(err, ErrInvalidArchive) {
				t.Fatalf("error = %v, want ErrInvalidArchive", err)
			}
		})
	}
}

type failingReader struct {
	data []byte
	err  error
	both bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	if r.both {
		return n, r.err
	}

	return n, nil
}

func TestReaderErrorsAndReadWithDataBoundary(t *testing.T) {
	t.Parallel()

	archive := makeTar(t, regular(testFileName, "payload"))
	for _, both := range []bool{false, true} {
		t.Run(map[bool]string{false: "separate error", true: "data and error"}[both], func(t *testing.T) {
			t.Parallel()

			source := &failingReader{
				data: append([]byte(nil), archive[:600]...), err: errArchiveReadTest, both: both,
			}
			_, err := Analyze(context.Background(), source, int64(len(archive)))
			if !errors.Is(err, errArchiveReadTest) {
				t.Fatalf("error = %v, want wrapped sentinel", err)
			}
		})
	}

	h := sha256.New()
	r := &boundedArchiveReader{
		cancelled: func() error { return nil },
		source:    &failingReader{data: []byte("abc"), err: errArchiveReadTest, both: true},
		hasher:    h,
		limit:     10,
	}
	buffer := make([]byte, 8)
	n, err := r.Read(buffer)
	if n != 3 || !errors.Is(err, errArchiveReadTest) || r.count != 3 || string(buffer[:n]) != "abc" {
		t.Fatalf("Read = (%d, %v), count=%d, data=%q", n, err, r.count, buffer[:n])
	}
	want := sha256.Sum256([]byte("abc"))
	if got := h.Sum(nil); !bytes.Equal(got, want[:]) {
		t.Errorf("hasher omitted bytes returned with error")
	}
}

func TestArchiveErrorPropagation(t *testing.T) {
	t.Parallel()

	duplicate := makeTar(t, regular(testFileName, "one"), regular(testFileName, "two"))
	if _, err := analyzeBytes(t, duplicate); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("duplicate archive error = %v", err)
	}

	scan := newArchiveScan()
	header := &tar.Header{Name: testFileName, Typeflag: tar.TypeReg, Size: 2}
	if err := scan.record(header, strings.NewReader("x")); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("short record body error = %v", err)
	}
	if _, err := scan.recordPayload(entryRegular, 2, strings.NewReader("x")); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("short payload error = %v", err)
	}
	if _, valid := normalizedSymlink("link", ""); valid {
		t.Fatal("empty symlink target accepted")
	}
}

func TestBoundedArchiveReaderCancellationAndMaximumLimit(t *testing.T) {
	t.Parallel()

	cancelled := &boundedArchiveReader{
		cancelled: func() error { return context.Canceled },
		source:    strings.NewReader("unused"),
		hasher:    sha256.New(),
		limit:     8,
	}
	if _, err := cancelled.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled reader error = %v", err)
	}

	maximum := &boundedArchiveReader{
		cancelled: func() error { return nil },
		source:    strings.NewReader("x"),
		hasher:    sha256.New(),
		limit:     int64(^uint64(0) >> 1),
	}
	if read, err := maximum.Read(make([]byte, 1)); read != 1 || err != nil || maximum.count != 1 {
		t.Fatalf("maximum-limit Read = (%d, %v), count=%d", read, err, maximum.count)
	}
}

func TestWhiteBoxPathAndPaddingHelpers(t *testing.T) {
	t.Parallel()

	if validArchivePathText(string([]byte{0xff})) || !utf8.ValidString("ok") {
		t.Error("UTF-8 validation mismatch")
	}
	if _, ok := normalizedMemberPath("dir/", true); !ok {
		t.Error("canonical directory with slash rejected")
	}
	if _, ok := normalizedSymlink("link", "../escape"); ok {
		t.Error("root symlink escape accepted")
	}
	if link, ok := normalizedSymlink("dir/link", "../target"); !ok || link != "../target" {
		t.Errorf("safe relative symlink = (%q, %v)", link, ok)
	}
	writer := zeroPaddingWriter{}
	if n, err := writer.Write([]byte{0, 0}); n != 2 || err != nil {
		t.Fatalf("zero padding Write = (%d, %v)", n, err)
	}
	if _, err := writer.Write([]byte{0, 1}); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("nonzero padding error = %v", err)
	}
}

func TestWhiteBoxArchiveErrorClassification(t *testing.T) {
	t.Parallel()

	for _, err := range []error{nil, io.EOF, io.ErrUnexpectedEOF} {
		if !errors.Is(classifyArchiveRead(err), ErrInvalidArchive) {
			t.Errorf("classifyArchiveRead(%v) did not report an invalid archive", err)
		}
	}
	if !errors.Is(classifyArchiveRead(ErrArchiveLimit), ErrArchiveLimit) {
		t.Error("archive limit classification changed")
	}
}

func TestWhiteBoxInventoryMetadataAndReaderStates(t *testing.T) {
	t.Parallel()

	scan := newArchiveScan()
	header := &tar.Header{
		Name: "file", Typeflag: tar.TypeReg, Mode: 0o640, Uid: 12, Gid: 34,
		Uname: "user", Gname: "group", ModTime: time.Unix(99, 123),
		PAXRecords: map[string]string{"example.beta": "two", "example.alpha": "one"}, Size: 1,
	}
	if err := scan.record(header, strings.NewReader("x")); err != nil {
		t.Fatalf("record valid metadata: %v", err)
	}
	raw := sha256.New()
	_, _ = raw.Write(make([]byte, tarTerminatorBytes))
	reader := &boundedArchiveReader{hasher: raw, count: tarTerminatorBytes}
	inventory := scan.inventory(reader)
	if !SameContent(inventory, inventory) {
		t.Fatalf("valid direct inventory does not match itself: %+v", inventory)
	}
	changed := inventory
	changed.EntryCount++
	if SameContent(inventory, changed) {
		t.Error("different entry counts matched")
	}

	complete := &boundedArchiveReader{count: tarTerminatorBytes, tailSize: tarTerminatorBytes}
	if !complete.completeTar() {
		t.Error("two zero terminator blocks not recognized")
	}
	complete.tail[0] = 1
	if complete.completeTar() {
		t.Error("nonzero terminator recognized")
	}

	overLimit := &boundedArchiveReader{
		cancelled: func() error { return nil }, source: strings.NewReader(""),
		hasher: sha256.New(), limit: 0, count: 1,
	}
	if _, err := overLimit.Read(make([]byte, 1)); !errors.Is(err, ErrArchiveLimit) {
		t.Errorf("already-exceeded reader error = %v", err)
	}
}
