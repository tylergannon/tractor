package main

import (
	"fmt"
	"os"
)

func main() {
	if err := newRootCommand().Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "tractor:", err)
		os.Exit(1)
	}
}
