package winres

import "github.com/larahfelipe/meterai/internal/buildinfo"

// ExecutableName is the file the version resource claims to be. Windows shows it
// as "original filename", and a mismatch with the file on disk is a signal that
// something was renamed after it was built.
const ExecutableName = buildinfo.Name + ".exe"

// MeterAIObject is this project's resource object: what the executable declares
// about itself. It lives here rather than in the generator command so the bytes
// a test compares against and the bytes the generator writes come from one
// expression.
func MeterAIObject() ([]byte, error) {
	numeric, err := ParseVersion(buildinfo.Version)
	if err != nil {
		return nil, err
	}
	version := EncodeVersionInfo(Version{
		FileVersion:      numeric,
		SemVer:           buildinfo.Version,
		CompanyName:      buildinfo.Publisher,
		FileDescription:  buildinfo.Description,
		ProductName:      buildinfo.Name,
		InternalName:     buildinfo.Name,
		OriginalFilename: ExecutableName,
		LegalCopyright:   buildinfo.Copyright,
	})
	return EncodeObject([]Resource{
		{Type: TypeVersion, ID: VersionID, Data: version},
		{Type: TypeManifest, ID: ManifestID, Data: []byte(Manifest)},
	}), nil
}
