package units_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/tcw/gostrict/analyzer/units"
)

func TestUnits(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), units.Analyzer, "example.com/terrain", "example.com/plain")
}

func TestUnitsInts(t *testing.T) {
	if err := units.Analyzer.Flags.Set("ints", "true"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := units.Analyzer.Flags.Set("ints", "false"); err != nil {
			t.Error(err)
		}
	})
	analysistest.Run(t, analysistest.TestData(), units.Analyzer, "example.com/counts")
}
