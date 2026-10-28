package directives_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/tcw/gostrict/analyzer/directives"
)

func TestDirectives(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), directives.Analyzer, "example.com/marked", "example.com/unmarked")
}
