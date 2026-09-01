package main

import (
	"fmt"
	"runtime/debug"
)

func init() {
	register("version", "print the sasash version", cmdVersion)
}

func cmdVersion(args []string) int {
	fmt.Println(version())
	return 0
}

func version() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}
