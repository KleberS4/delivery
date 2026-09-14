// Comando delivery: carregador sob demanda de skills para agentes de IA.
package main

import (
	"os"

	"github.com/kleberS4/delivery/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:], os.Stdout, os.Stderr))
}
