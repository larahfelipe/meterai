package winres

import (
	"bytes"
	"sort"
)

// Resource is one entry of the resource tree: a type, an identifier and the
// bytes themselves.
type Resource struct {
	Type uint32
	ID   uint32
	Data []byte
}

// Resource types used here. The full set is large; these are the two that make
// an executable identifiable.
const (
	// TypeVersion is RT_VERSION.
	TypeVersion = 16
	// TypeManifest is RT_MANIFEST.
	TypeManifest = 24

	// ManifestID is CREATEPROCESS_MANIFEST_RESOURCE_ID, the identifier the loader
	// looks for when it starts a process.
	ManifestID = 1
	// VersionID is the conventional identifier of the sole version resource.
	VersionID = 1
)

const (
	// machineAMD64 matches the only architecture this project ships.
	machineAMD64 = 0x8664

	coffHeaderSize    = 20
	sectionHeaderSize = 40
	relocationSize    = 10
	symbolSize        = 18

	// sectionInitializedDataRead marks a section the loader maps read-only:
	// IMAGE_SCN_CNT_INITIALIZED_DATA | IMAGE_SCN_MEM_READ. No section this
	// package emits is writable or executable.
	sectionInitializedDataRead = 0x40000040

	// relocAddr32NB is IMAGE_REL_AMD64_ADDR32NB, a 32-bit RVA. Every pointer in
	// a resource directory is an RVA, and an object file has no addresses yet, so
	// each is emitted as an offset plus a relocation the linker resolves.
	relocAddr32NB = 0x0003

	// symbolClassStatic is IMAGE_SYM_CLASS_STATIC, the class of a section symbol.
	symbolClassStatic = 3

	// directoryHeaderSize and directoryEntrySize are IMAGE_RESOURCE_DIRECTORY and
	// IMAGE_RESOURCE_DIRECTORY_ENTRY.
	directoryHeaderSize = 16
	directoryEntrySize  = 8
	// dataEntrySize is IMAGE_RESOURCE_DATA_ENTRY.
	dataEntrySize = 16
	// subdirectoryFlag marks a directory entry as pointing at another directory
	// rather than at data.
	subdirectoryFlag = 0x80000000

	// dataAlignment keeps each resource blob at a 4-byte boundary, which is what
	// the structures inside them assume.
	dataAlignment = 4

	// sectionDirectory and sectionData are merged by the linker in name order
	// into one .rsrc section, which is why the suffixes matter.
	sectionDirectory = ".rsrc$01"
	sectionData      = ".rsrc$02"

	// languageNeutral is the sole language of every resource here. The version
	// resource carries its own translation table, and a manifest has no language.
	languageNeutral = 0
)

// EncodeObject produces the .syso object file that carries resources into the
// linked executable.
//
// The layout is the one a resource compiler emits: a directory section holding
// the three-level tree (type, identifier, language) and a data section holding
// the blobs, with one relocation per blob because the directory refers to the
// data by address.
func EncodeObject(resources []Resource) []byte {
	// Sorted because a resource directory is required to be, and because two runs
	// over the same input must produce identical bytes.
	sorted := append([]Resource(nil), resources...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Type != sorted[j].Type {
			return sorted[i].Type < sorted[j].Type
		}
		return sorted[i].ID < sorted[j].ID
	})

	directory, relocations, data := encodeTree(sorted)

	dirOffset := coffHeaderSize + 2*sectionHeaderSize
	relocOffset := dirOffset + len(directory)
	dataOffset := relocOffset + len(relocations)*relocationSize
	symbolOffset := dataOffset + len(data)

	out := new(bytes.Buffer)
	// COFF header. The timestamp is zero so that the same source produces the
	// same object, which is what lets a third party reproduce the build.
	write(out, uint16(machineAMD64), uint16(2), uint32(0),
		uint32(symbolOffset), uint32(2), uint16(0), uint16(0))

	writeSectionHeader(out, sectionDirectory, len(directory), dirOffset, relocOffset, len(relocations))
	writeSectionHeader(out, sectionData, len(data), dataOffset, 0, 0)

	out.Write(directory)
	for _, at := range relocations {
		// Symbol 1 is the data section: every RVA in the directory is relative to
		// where that section lands.
		write(out, uint32(at), uint32(1), uint16(relocAddr32NB))
	}
	out.Write(data)

	writeSectionSymbol(out, sectionDirectory, 1)
	writeSectionSymbol(out, sectionData, 2)
	// The string table is empty; its four-byte length is still required.
	write(out, uint32(4))
	return out.Bytes()
}

// encodeTree lays out the directory section and the data section, returning the
// offsets inside the directory that need a relocation.
func encodeTree(resources []Resource) (directory []byte, relocations []int, data []byte) {
	byType := map[uint32][]Resource{}
	var types []uint32
	for _, r := range resources {
		if _, seen := byType[r.Type]; !seen {
			types = append(types, r.Type)
		}
		byType[r.Type] = append(byType[r.Type], r)
	}

	// Three levels of directory, then one data entry per resource. Every offset
	// below is measured from the start of the directory section, which is what
	// the format requires.
	typeDirSize := directoryHeaderSize + len(types)*directoryEntrySize
	idDirsAt := typeDirSize
	idDirsSize := 0
	for _, t := range types {
		idDirsSize += directoryHeaderSize + len(byType[t])*directoryEntrySize
	}
	langDirsAt := idDirsAt + idDirsSize
	langDirsSize := len(resources) * (directoryHeaderSize + directoryEntrySize)
	dataEntriesAt := langDirsAt + langDirsSize

	typeDir := new(bytes.Buffer)
	idDirs := new(bytes.Buffer)
	langDirs := new(bytes.Buffer)
	dataEntries := new(bytes.Buffer)
	blobs := new(bytes.Buffer)

	writeDirectoryHeader(typeDir, len(types))
	idCursor, langCursor, dataCursor := idDirsAt, langDirsAt, dataEntriesAt
	for _, t := range types {
		write(typeDir, t, uint32(idCursor)|subdirectoryFlag)

		writeDirectoryHeader(idDirs, len(byType[t]))
		idCursor += directoryHeaderSize + len(byType[t])*directoryEntrySize
		for _, r := range byType[t] {
			write(idDirs, r.ID, uint32(langCursor)|subdirectoryFlag)

			writeDirectoryHeader(langDirs, 1)
			write(langDirs, uint32(languageNeutral), uint32(dataCursor))
			langCursor += directoryHeaderSize + directoryEntrySize

			// OffsetToData holds the blob's offset inside the data section; the
			// relocation turns it into the section's own address plus that offset.
			relocations = append(relocations, dataCursor)
			write(dataEntries, uint32(blobs.Len()), uint32(len(r.Data)), uint32(0), uint32(0))
			dataCursor += dataEntrySize

			blobs.Write(r.Data)
			pad(blobs, dataAlignment)
		}
	}

	directory = append(directory, typeDir.Bytes()...)
	directory = append(directory, idDirs.Bytes()...)
	directory = append(directory, langDirs.Bytes()...)
	directory = append(directory, dataEntries.Bytes()...)
	return directory, relocations, blobs.Bytes()
}

func writeDirectoryHeader(buf *bytes.Buffer, entries int) {
	// Characteristics, timestamp and version are zero; every entry is identified
	// by number rather than by name.
	write(buf, uint32(0), uint32(0), uint16(0), uint16(0), uint16(0), uint16(entries))
}

func writeSectionHeader(buf *bytes.Buffer, name string, size, offset, relocOffset, relocCount int) {
	var padded [8]byte
	copy(padded[:], name)
	buf.Write(padded[:])
	write(buf,
		uint32(0), uint32(0), uint32(size), uint32(offset),
		uint32(relocOffset), uint32(0),
		uint16(relocCount), uint16(0), uint32(sectionInitializedDataRead))
}

func writeSectionSymbol(buf *bytes.Buffer, name string, section int16) {
	var padded [8]byte
	copy(padded[:], name)
	buf.Write(padded[:])
	write(buf, uint32(0), section, uint16(0), uint8(symbolClassStatic), uint8(0))
}
