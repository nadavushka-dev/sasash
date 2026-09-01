package main

import (
	"fmt"
	"io"
	"runtime/debug"
)

func init() {
	register("version", "print the sasash version", cmdVersion)
}

func cmdVersion(_ []string, stdout, _ io.Writer) int {
	fmt.Fprintln(stdout, version())
	return 0
}

func version() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}
