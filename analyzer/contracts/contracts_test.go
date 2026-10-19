package contracts_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/tcw/gostrict/analyzer/contracts"
)

func TestContracts(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), contracts.Analyzer, "example.com/route")
}
