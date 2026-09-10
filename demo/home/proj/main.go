//go:build ignore

// A demo fixture, not part of the build: the SASASH.4.33 tape uploads this
// directory whole. The build tag keeps it out of ./... (and the recorded
// copies under demo/out too).
package main

import "fmt"

func main() {
	fmt.Println("hello from proj")
}
