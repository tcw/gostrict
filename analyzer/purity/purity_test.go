package purity_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/tcw/gostrict/analyzer/purity"
)

func TestPurity(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), purity.Analyzer, "example.com/core", "example.com/shell", "example.com/mathx")
}
