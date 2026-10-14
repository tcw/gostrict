package units_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/tcw/gostrict/analyzer/units"
)

func TestUnits(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), units.Analyzer, "example.com/terrain", "example.com/plain")
}
