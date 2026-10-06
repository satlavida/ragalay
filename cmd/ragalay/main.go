package main

import (
	"os"

	"github.com/satlavida/ragalay/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
