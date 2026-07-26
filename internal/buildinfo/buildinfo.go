// Package buildinfo names the product and its version. It exists so the three
// places that have to agree — the outbound User-Agent, the tray's accessible
// name, and the version resource compiled into the executable — cannot drift
// apart, since a binary whose declared version contradicts the traffic it sends
// is exactly what an audit cannot explain away.
//
// It imports nothing and is imported by every layer, including the build-time
// resource generator.
package buildinfo

// Name is the product name. It is not translated: it is the same word in every
// language and Windows announces it as the accessible name of the tray icon.
const Name = "meterAI"

// Version is the release, as major.minor.patch. Windows version resources carry
// four 16-bit fields, so the fourth is always zero here rather than encoding a
// build counter, which would make two builds of the same source differ.
const Version = "0.1.0"

// Publisher is the name recorded as the executable's company. It has to match
// whoever signs the binary, or the signature contradicts the metadata.
const Publisher = "Felipe Lara"

// Description is the one-line summary Windows shows in Task Manager and in the
// file's property sheet. Task Manager shows this rather than the file name, so
// a process nobody can identify is a process a user is right to distrust.
const Description = "AI subscription usage monitor for the notification area"

// Copyright is the notice in the version resource, matching LICENSE.
const Copyright = "Copyright (c) 2026 Felipe Lara. MIT licensed."
