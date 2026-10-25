// Package marked has directive mistakes.
//
// strict:pure // want `// strict:pure is an ordinary comment`
//
//strict:units
//strict:puer // want `unknown directive //strict:puer`
//strict:contracts core // want `//strict:contracts has text after it`
package marked

// strict: this is prose, not a directive.

//strict:units // want `//strict:units has no effect here`

// Ratio is dimensionless.
//
//strict:allow units a ratio has no unit
func Ratio(a, b Meters) float64 { return float64(a / b) }

// Meters is a length.
type Meters float64

// Max returns the larger value.
//
//strict:allow argswap commutative
func Max(a, b int) int { return max(a, b) }

// Stale has nothing to suppress.
//
//strict:allow argswap stale // want `//strict:allow argswap suppresses nothing: there is no argswap finding here`
func Stale(a Meters, b string) {}

// Typo misspells the rule.
//
//strict:allow argswp typo // want `//strict:allow names unknown rule "argswp"`
func Typo(a, b int) {}

// Both uses one rule of two.
//
//strict:allow argswap,units both apply // want `//strict:allow units suppresses nothing`
func Both(a, b int) {}

// Pure is not checked here.
//
//strict:allow purity not a pure package // want `the package is not marked //strict:pure`
func Pure() {}

//strict:allowed // want `unknown directive //strict:allowed`
func Allowed() {}
