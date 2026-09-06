package main

import (
	"os"

	"github.com/simon-em/kranq/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args))
}
