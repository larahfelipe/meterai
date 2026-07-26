// Package winres encodes the Windows resources compiled into the executable: a
// version resource and an application manifest. Nothing at runtime imports it —
// it produces the .syso object the Go linker consumes at build time.
//
// It exists because an executable with no version resource, no company name and
// no declared execution level shows up in Task Manager and in the file's property
// sheet as nothing but a file name, and because the execution level is worth
// stating rather than leaving to a default: this program runs as the invoking
// user and never asks for elevation.
//
// The container is written directly, for the same reason internal/trayicon
// writes the ICO container directly: the alternative is a build-time dependency
// on a resource compiler to emit a few hundred bytes of well-specified structure.
package winres

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Version is the content of a VS_VERSIONINFO resource. Every field is display
// text except FileVersion, which Windows also compares numerically.
type Version struct {
	// FileVersion is the four-part numeric version. Windows compares these, so
	// they are the authoritative ordering between two builds.
	FileVersion     [4]uint16
	CompanyName     string
	FileDescription string
	ProductName     string
	// SemVer is the human-readable version shown for both FileVersion and
	// ProductVersion, which may carry more than the four numbers.
	SemVer           string
	InternalName     string
	OriginalFilename string
	LegalCopyright   string
}

// ParseVersion converts a "major.minor.patch" string into the four-part form,
// with a zero build field: a build counter would make two compilations of the
// same source produce different binaries.
func ParseVersion(semver string) ([4]uint16, error) {
	var out [4]uint16
	parts := strings.Split(semver, ".")
	if len(parts) != 3 {
		return out, fmt.Errorf("version %q is not major.minor.patch", semver)
	}
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 16)
		if err != nil {
			return out, fmt.Errorf("version %q: field %d: %w", semver, i+1, err)
		}
		out[i] = uint16(n)
	}
	return out, nil
}

const (
	// fixedFileInfoSignature and fixedFileInfoVersion are the constants
	// VS_FIXEDFILEINFO is recognized by.
	fixedFileInfoSignature = 0xFEEF04BD
	fixedFileInfoVersion   = 0x00010000
	// fixedFileInfoSize is the 13 DWORDs of VS_FIXEDFILEINFO.
	fixedFileInfoSize = 52

	// fileFlagsMask covers the flag bits that are meaningful; none is set,
	// because this is a plain release build: not a debug, patched or
	// pre-release image.
	fileFlagsMask = 0x3F
	// vosWindows32 and vftApp declare a plain Win32 application, as opposed to a
	// driver, a font or a virtual device — the categories Windows treats
	// differently.
	vosWindows32 = 0x00000004
	vftApp       = 0x00000001

	// langUSEnglish and codepageUnicode identify the one string table. The pair
	// also forms the "Translation" value, in the opposite word order.
	langUSEnglish    = 0x0409
	codepageUnicode  = 0x04B0
	translationTable = "040904B0"
)

// EncodeVersionInfo lays out a VS_VERSIONINFO tree. Every node is
// length-prefixed and padded to a 32-bit boundary, and a node's length covers
// its children, so the structure is built leaf-first.
func EncodeVersionInfo(v Version) []byte {
	fixed := new(bytes.Buffer)
	write(fixed,
		uint32(fixedFileInfoSignature), uint32(fixedFileInfoVersion),
		versionHigh(v.FileVersion), versionLow(v.FileVersion),
		versionHigh(v.FileVersion), versionLow(v.FileVersion),
		uint32(fileFlagsMask), uint32(0),
		uint32(vosWindows32), uint32(vftApp), uint32(0),
		uint32(0), uint32(0))

	// The order is the order Windows lists them in the property sheet.
	stringTable := &node{key: translationTable, text: true}
	for _, field := range []struct{ key, value string }{
		{"CompanyName", v.CompanyName},
		{"FileDescription", v.FileDescription},
		{"FileVersion", v.SemVer},
		{"InternalName", v.InternalName},
		{"LegalCopyright", v.LegalCopyright},
		{"OriginalFilename", v.OriginalFilename},
		{"ProductName", v.ProductName},
		{"ProductVersion", v.SemVer},
	} {
		if field.value == "" {
			continue
		}
		stringTable.children = append(stringTable.children, &node{
			key: field.key, text: true, value: utf16Bytes(field.value),
			// A text value's length is counted in characters, not bytes.
			valueLen: uint16(len(utf16.Encode([]rune(field.value))) + 1),
		})
	}

	translation := new(bytes.Buffer)
	write(translation, uint16(langUSEnglish), uint16(codepageUnicode))

	root := &node{
		key: "VS_VERSION_INFO", value: fixed.Bytes(), valueLen: fixedFileInfoSize,
		children: []*node{
			{key: "StringFileInfo", text: true, children: []*node{stringTable}},
			{key: "VarFileInfo", text: true, children: []*node{
				{key: "Translation", value: translation.Bytes(), valueLen: uint16(translation.Len())},
			}},
		},
	}
	return root.encode()
}

func versionHigh(v [4]uint16) uint32 { return uint32(v[0])<<16 | uint32(v[1]) }
func versionLow(v [4]uint16) uint32  { return uint32(v[2])<<16 | uint32(v[3]) }

// node is one length-prefixed element of a version resource.
type node struct {
	key      string
	value    []byte
	valueLen uint16
	// text marks a node whose value is a string; Windows uses it to decide how
	// to read valueLen.
	text     bool
	children []*node
}

// nodeHeaderSize is the wLength, wValueLength and wType every node opens with.
// Alignment is measured from the start of the node, so every pad below has to
// account for it — padding the body alone puts VS_FIXEDFILEINFO two bytes early
// and Windows reads the structure from the wrong offset.
const nodeHeaderSize = 6

func (n *node) encode() []byte {
	body := new(bytes.Buffer)
	body.Write(utf16Bytes(n.key))
	padAfterHeader(body)
	body.Write(n.value)
	for _, child := range n.children {
		padAfterHeader(body)
		body.Write(child.encode())
	}

	out := new(bytes.Buffer)
	kind := uint16(0)
	if n.text {
		kind = 1
	}
	write(out, uint16(body.Len()+nodeHeaderSize), n.valueLen, kind)
	out.Write(body.Bytes())
	return out.Bytes()
}

// padAfterHeader aligns the next member to a 32-bit boundary counted from the
// start of the node, not from the start of the buffer holding its body.
func padAfterHeader(buf *bytes.Buffer) {
	for (nodeHeaderSize+buf.Len())%4 != 0 {
		buf.WriteByte(0)
	}
}

// Manifest is the application manifest. It states the two things a reader of an
// unknown executable most wants to know: that it never asks for elevation, and
// which Windows versions it was built against.
const Manifest = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <assemblyIdentity type="win32" name="` + manifestIdentity + `" version="` + manifestVersion + `"/>
  <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3">
    <security>
      <requestedPrivileges>
        <requestedExecutionLevel level="asInvoker" uiAccess="false"/>
      </requestedPrivileges>
    </security>
  </trustInfo>
  <compatibility xmlns="urn:schemas-microsoft-com:compatibility.v1">
    <application>
      <supportedOS Id="{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a}"/>
      <supportedOS Id="{1f676c76-80e1-4239-95bb-83d0f6d0da78}"/>
      <supportedOS Id="{4a2f28e3-53b9-4441-ba9c-d69d4a4a6e38}"/>
    </application>
  </compatibility>
  <application xmlns="urn:schemas-microsoft-com:asm.v3">
    <windowsSettings>
      <dpiAwareness xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">permonitorv2</dpiAwareness>
    </windowsSettings>
  </application>
</assembly>
`

const (
	// manifestIdentity is not a namespace this app registers anywhere; it names
	// the assembly for the loader only.
	manifestIdentity = "meterAI.meterAI"
	// manifestVersion is fixed at the identity level: it participates in
	// side-by-side assembly resolution, which this app does not use.
	manifestVersion = "1.0.0.0"
)

func utf16Bytes(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 0, len(units)*2+2)
	for _, u := range units {
		out = binary.LittleEndian.AppendUint16(out, u)
	}
	return append(out, 0, 0)
}

func pad(buf *bytes.Buffer, boundary int) {
	for buf.Len()%boundary != 0 {
		buf.WriteByte(0)
	}
}

func write(buf *bytes.Buffer, values ...any) {
	for _, v := range values {
		// A bytes.Buffer cannot fail: it grows or the allocation panics, which is
		// an unrecoverable condition rather than an error to report.
		_ = binary.Write(buf, binary.LittleEndian, v)
	}
}
