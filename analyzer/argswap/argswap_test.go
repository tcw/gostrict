package argswap_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/tcw/gostrict/analyzer/argswap"
)

func TestArgswap(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), argswap.Analyzer, "example.com/geo")
}
