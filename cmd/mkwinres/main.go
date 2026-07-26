// Command mkwinres writes the Windows resource object linked into meterAI.exe.
//
// The object is committed rather than generated during the build, so the
// documented build command stays a single `go build`. A test in internal/winres
// fails if the committed file stops matching what this program produces, which
// is what keeps a binary blob in the repository honest.
package main

import (
	"fmt"
	"os"

	"github.com/larahfelipe/meterai/internal/buildinfo"
	"github.com/larahfelipe/meterai/internal/winres"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <output.syso>\n", os.Args[0])
		os.Exit(2)
	}
	object, err := winres.MeterAIObject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mkwinres: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(os.Args[1], object, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "mkwinres: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d bytes) for %s %s\n", os.Args[1], len(object), buildinfo.Name, buildinfo.Version)
}
