// Command haetae-agent is the Haetae host agent.
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
		fmt.Println(version.String("haetae-agent"))
		return
	}

	fmt.Fprintln(os.Stderr, "haetae-agent: not implemented yet")
	os.Exit(1)
}
