// Command haetae-server is the Haetae control plane.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/alexseymer/haetae/internal/version"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.String("haetae-server"))
		return
	}

	fmt.Fprintln(os.Stderr, "haetae-server: not implemented yet")
	os.Exit(1)
}
