package main

import (
	"os"

	"github.com/effetmonstre/forge/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args))
}
