// Command strictvet runs the gostrict analyzers (argswap, units, purity,
// contracts) on Go packages.
//
//	strictvet ./...
//	go vet -vettool=$(command -v strictvet) ./...
//
// Run strictvet help for the analyzer flags.
package main

import (
	"golang.org/x/tools/go/analysis/multichecker"

	"github.com/tcw/gostrict/analyzer/argswap"
	"github.com/tcw/gostrict/analyzer/contracts"
	"github.com/tcw/gostrict/analyzer/purity"
	"github.com/tcw/gostrict/analyzer/units"
)

func main() {
	multichecker.Main(argswap.Analyzer, units.Analyzer, purity.Analyzer, contracts.Analyzer)
}
