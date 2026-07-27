// Package buildinfo names the product and its version. It exists so the two
// places that have to agree — the outbound User-Agent and the tray's
// accessible name — cannot drift apart, since traffic claiming a version the
// executable does not declare is exactly what an audit cannot explain away.
//
// It imports nothing, which is what lets every layer import it.
package buildinfo

// Name is the product name. It is not translated: it is the same word in every
// language and Windows announces it as the accessible name of the tray icon.
const Name = "meterAI"

// Version is the release, as major.minor.patch.
const Version = "0.1.0"
