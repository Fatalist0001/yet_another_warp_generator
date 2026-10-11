// Package main — точка входа warpgen.
//
// Статический бинарник warpgen собирается командой:
//
//	CGO_ENABLED=0 go build -tags netgo -ldflags "-s -w -extldflags '-static' -X main.Version=..." -o warpgen ./cmd/warpgen
//
// Версия, коммит и дата подставляются через ldflags. Если флаги не переданы —
// значения по умолчанию безопасны для dev-сборок.
package main

import (
	"fmt"
	"os"

	"github.com/alexgen/warpgen/cli"
)

var (
	// Version — семантическая версия утилиты. Переопределяется через -ldflags "-X main.Version=...".
	Version = "dev"

	// Commit — короткий SHA коммита. Переопределяется через -ldflags "-X main.Commit=...".
	Commit = "unknown"

	// Date — время сборки в UTC ISO 8601. Переопределяется через -ldflags "-X main.Date=...".
	Date = "unknown"
)

func main() {
	cli.Version = fmt.Sprintf("%s (%s, %s)", Version, Commit, Date)

	if len(os.Args) > 1 && (os.Args[1] == "-v" || os.Args[1] == "--version") {
		fmt.Printf("warpgen %s\n", cli.Version)
		os.Exit(0)
	}

	if err := cli.Run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "warpgen: %s\n", err)
		os.Exit(1)
	}
}
