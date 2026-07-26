package winres

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func sampleVersion() Version {
	return Version{
		FileVersion:      [4]uint16{1, 2, 3, 0},
		SemVer:           "1.2.3",
		CompanyName:      "Sample Publisher",
		FileDescription:  "Sample description",
		ProductName:      "Sample",
		InternalName:     "sample",
		OriginalFilename: "sample.exe",
		LegalCopyright:   "Copyright (c) 2026",
	}
}

// The regression this file exists for: alignment is measured from the start of a
// node, so padding the body alone puts VS_FIXEDFILEINFO two bytes early and every
// field Windows reads from it is garbage. The bug produced a valid-looking
// resource that decoded to a signature of 0xfeef.
func TestFixedFileInfoSitsWhereWindowsLooksForIt(t *testing.T) {
	blob := EncodeVersionInfo(sampleVersion())

	// Exactly the walk the loader performs: skip the header and the key, round up
	// to the next 32-bit boundary, and the structure starts there.
	const headerAndKey = nodeHeaderSize + len("VS_VERSION_INFO\x00")*2
	offset := (headerAndKey + 3) &^ 3
	if offset == headerAndKey {
		t.Fatal("the test's own arithmetic no longer exercises the padding")
	}

	if got := binary.LittleEndian.Uint32(blob[offset:]); got != fixedFileInfoSignature {
		t.Fatalf("signature at offset %d = %#x, want %#x", offset, got, uint32(fixedFileInfoSignature))
	}
	if got := binary.LittleEndian.Uint32(blob[offset+4:]); got != fixedFileInfoVersion {
		t.Errorf("struct version = %#x", got)
	}
	fileMS := binary.LittleEndian.Uint32(blob[offset+8:])
	fileLS := binary.LittleEndian.Uint32(blob[offset+12:])
	if fileMS != 1<<16|2 || fileLS != 3<<16 {
		t.Errorf("file version = %d.%d.%d.%d, want 1.2.3.0",
			fileMS>>16, fileMS&0xFFFF, fileLS>>16, fileLS&0xFFFF)
	}
}

// Every node declares its own length and every child starts on a 32-bit
// boundary. A single node that lies about either makes the loader walk into the
// middle of the next one and abandon the whole resource.
func TestEveryVersionNodeIsWellFormed(t *testing.T) {
	blob := EncodeVersionInfo(sampleVersion())
	if len(blob)%4 != 0 {
		t.Errorf("resource is %d bytes, not a multiple of 4", len(blob))
	}
	walkNodes(t, blob, 0, 0)
}

// walkNodes checks one node and recurses into whatever follows its value, which
// is how the loader distinguishes children from padding.
func walkNodes(t *testing.T, blob []byte, start, depth int) {
	t.Helper()
	if depth > 4 {
		t.Fatal("version tree is deeper than the format allows")
	}
	if start%4 != 0 {
		t.Errorf("node at %d does not start on a 32-bit boundary", start)
	}
	length := int(binary.LittleEndian.Uint16(blob[start:]))
	valueLen := int(binary.LittleEndian.Uint16(blob[start+2:]))
	kind := binary.LittleEndian.Uint16(blob[start+4:])
	if length < nodeHeaderSize || start+length > len(blob) {
		t.Fatalf("node at %d declares length %d, past the %d byte resource", start, length, len(blob))
	}
	if kind > 1 {
		t.Errorf("node at %d has type %d, want 0 (binary) or 1 (text)", start, kind)
	}

	key, after := readUTF16(blob, start+nodeHeaderSize)
	valueStart := align4(after)
	valueBytes := valueLen
	if kind == 1 {
		// A text node counts its value in characters.
		valueBytes = valueLen * 2
	}
	if valueStart+valueBytes > start+length {
		t.Fatalf("node %q claims a %d byte value that overruns its own length", key, valueBytes)
	}

	for child := align4(valueStart + valueBytes); child < start+length; child = align4(child + int(binary.LittleEndian.Uint16(blob[child:]))) {
		if binary.LittleEndian.Uint16(blob[child:]) == 0 {
			t.Fatalf("zero-length child under %q at %d", key, child)
		}
		walkNodes(t, blob, child, depth+1)
	}
}

func align4(n int) int { return (n + 3) &^ 3 }

func readUTF16(blob []byte, at int) (string, int) {
	var units []uint16
	for i := at; i+1 < len(blob); i += 2 {
		u := binary.LittleEndian.Uint16(blob[i:])
		if u == 0 {
			return string(utf16.Decode(units)), i + 2
		}
		units = append(units, u)
	}
	return string(utf16.Decode(units)), len(blob)
}

func TestVersionStringsAreCarriedVerbatim(t *testing.T) {
	blob := EncodeVersionInfo(sampleVersion())
	text := decodeAllUTF16(blob)
	for _, want := range []string{
		"VS_VERSION_INFO", "StringFileInfo", "VarFileInfo", "Translation", translationTable,
		"Sample Publisher", "Sample description", "sample.exe", "1.2.3",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("resource does not carry %q", want)
		}
	}
}

// A field nobody set must not become an empty row in the property sheet.
func TestVersionOmitsFieldsThatWereNotSet(t *testing.T) {
	blob := EncodeVersionInfo(Version{FileVersion: [4]uint16{1, 0, 0, 0}, SemVer: "1.0.0"})
	if text := decodeAllUTF16(blob); strings.Contains(text, "CompanyName") {
		t.Errorf("an unset CompanyName still produced a string entry")
	}
	walkNodes(t, blob, 0, 0)
}

func decodeAllUTF16(blob []byte) string {
	units := make([]uint16, 0, len(blob)/2)
	for i := 0; i+1 < len(blob); i += 2 {
		units = append(units, binary.LittleEndian.Uint16(blob[i:]))
	}
	return string(utf16.Decode(units))
}

func TestParseVersion(t *testing.T) {
	got, err := ParseVersion("12.34.56")
	if err != nil {
		t.Fatalf("ParseVersion: %v", err)
	}
	// The fourth field stays zero: a build counter would make two compilations of
	// the same source produce different binaries.
	if got != [4]uint16{12, 34, 56, 0} {
		t.Errorf("ParseVersion = %v", got)
	}

	for _, bad := range []string{"", "1", "1.2", "1.2.3.4", "1.2.x", "1.2.-3", "1.2.70000", "v1.2.3"} {
		if _, err := ParseVersion(bad); err == nil {
			t.Errorf("ParseVersion(%q) was accepted", bad)
		}
	}
}

// The manifest is the executable's own statement about privilege. Asking for
// elevation is the single loudest thing a tray utility could do.
func TestManifestNeverRequestsElevation(t *testing.T) {
	if !strings.Contains(Manifest, `level="asInvoker"`) {
		t.Error("the manifest does not declare asInvoker")
	}
	for _, forbidden := range []string{"requireAdministrator", "highestAvailable", `uiAccess="true"`} {
		if strings.Contains(Manifest, forbidden) {
			t.Errorf("the manifest requests %q", forbidden)
		}
	}
}

func TestObjectIsDeterministic(t *testing.T) {
	first, err := MeterAIObject()
	if err != nil {
		t.Fatalf("MeterAIObject: %v", err)
	}
	second, err := MeterAIObject()
	if err != nil {
		t.Fatalf("MeterAIObject: %v", err)
	}
	// A timestamp or any other varying field here would mean two builds of one
	// commit produce different executables, and a published hash would prove
	// nothing.
	if !bytes.Equal(first, second) {
		t.Error("two encodings of the same input differ")
	}
}

// The object is committed rather than generated during the build, so nothing but
// this test stops it from drifting away from the source it claims to come from.
func TestCommittedObjectMatchesTheGenerator(t *testing.T) {
	want, err := MeterAIObject()
	if err != nil {
		t.Fatalf("MeterAIObject: %v", err)
	}
	path := filepath.Join("..", "..", "cmd", "meterai", "meterai_windows_amd64.syso")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the committed resource object is unreadable: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s is stale; regenerate it with `go generate ./cmd/meterai`", path)
	}
}

// The object file's own shape: the linker rejects a malformed one, but a
// well-formed one that maps a writable or executable section would put resource
// data somewhere it has no business being.
func TestObjectDeclaresOnlyReadOnlyData(t *testing.T) {
	object, err := MeterAIObject()
	if err != nil {
		t.Fatalf("MeterAIObject: %v", err)
	}
	if got := binary.LittleEndian.Uint16(object[0:]); got != machineAMD64 {
		t.Errorf("machine = %#x, want %#x", got, machineAMD64)
	}
	sections := int(binary.LittleEndian.Uint16(object[2:]))
	if sections != 2 {
		t.Fatalf("sections = %d, want the directory and the data", sections)
	}
	if stamp := binary.LittleEndian.Uint32(object[4:]); stamp != 0 {
		t.Errorf("timestamp = %d, want 0 so the object is reproducible", stamp)
	}

	const (
		memWrite   = 0x80000000
		memExecute = 0x20000000
	)
	for i := range sections {
		header := coffHeaderSize + i*sectionHeaderSize
		name := string(bytes.TrimRight(object[header:header+8], "\x00"))
		chars := binary.LittleEndian.Uint32(object[header+36:])
		if chars&memWrite != 0 || chars&memExecute != 0 {
			t.Errorf("section %s is writable or executable (%#x)", name, chars)
		}
		if chars != sectionInitializedDataRead {
			t.Errorf("section %s characteristics = %#x, want %#x", name, chars, uint32(sectionInitializedDataRead))
		}
	}
}

// Each resource's address is a relocation the linker resolves. One pointing at
// the wrong offset yields an executable whose resources cannot be read at all —
// which is invisible until Windows tries.
func TestObjectRelocatesEveryResourceAddress(t *testing.T) {
	object, err := MeterAIObject()
	if err != nil {
		t.Fatalf("MeterAIObject: %v", err)
	}
	header := coffHeaderSize // the directory section is first
	dirSize := int(binary.LittleEndian.Uint32(object[header+16:]))
	dirOffset := int(binary.LittleEndian.Uint32(object[header+20:]))
	relocOffset := int(binary.LittleEndian.Uint32(object[header+24:]))
	relocCount := int(binary.LittleEndian.Uint16(object[header+32:]))

	if relocCount != 2 {
		t.Fatalf("relocations = %d, want one per resource", relocCount)
	}
	for i := range relocCount {
		at := relocOffset + i*relocationSize
		target := int(binary.LittleEndian.Uint32(object[at:]))
		symbol := binary.LittleEndian.Uint32(object[at+4:])
		kind := binary.LittleEndian.Uint16(object[at+8:])

		if target < 0 || target+4 > dirSize {
			t.Errorf("relocation %d points at %d, outside the %d byte directory", i, target, dirSize)
		}
		// It must land on the first field of an IMAGE_RESOURCE_DATA_ENTRY, which
		// is 16-byte aligned relative to the entries that precede it.
		if target%4 != 0 {
			t.Errorf("relocation %d is not aligned: %d", i, target)
		}
		if symbol != 1 {
			t.Errorf("relocation %d resolves against symbol %d, want the data section", i, symbol)
		}
		if kind != relocAddr32NB {
			t.Errorf("relocation %d is type %#x, want an RVA", i, kind)
		}
		// The addend is the blob's offset inside the data section.
		if addend := binary.LittleEndian.Uint32(object[dirOffset+target:]); addend > uint32(len(object)) {
			t.Errorf("relocation %d has an implausible addend %d", i, addend)
		}
	}
}
