// Package route plans routes.
//
//strict:contracts
package route

import "github.com/tcw/gostrict/contract"

type Point struct{ X, Y int }

const debug = true

func Plan(from, to Point, budget int) []Point {
	contract.Require(from != to, "from and to must differ")
	contract.Require(budget > 0, "budget must be positive")
	out := []Point{from, to}
	contract.Ensure(len(out) >= 2, "a route has at least two points")
	return out
}

func Length(ps []Point) int { // want `exported Length takes parameters but states no precondition`
	return len(ps)
}

func Empty(ps []Point) bool { // want `exported Empty takes parameters`
	n := len(ps)
	contract.Require(n >= 0, "not first statement, so it does not count")
	return n == 0
}

func Origin() Point { return Point{} } // no parameters: fine

func helper(p Point) int { return p.X } // unexported: fine

func (p Point) Add(q Point) Point { // want `exported Add takes parameters`
	return Point{p.X + q.X, p.Y + q.Y}
}

func (p Point) Norm() int { return p.X*p.X + p.Y*p.Y } // only the receiver: fine

type grid struct{}

func (grid) Cell(p Point) {} // unexported receiver: fine

func Trivial(p Point) {
	contract.Require(true, "always fine") // want `condition that is always true states nothing`
}

func AlsoTrivial(p Point) {
	contract.Require(debug, "constant true") // want `always true`
}

func NoMessage(p Point, msg string) {
	contract.Require(p.X > 0, msg)   // want `message must be a constant string`
	contract.Invariant(p.Y > 0, " ") // want `message is empty`
}

// Wrap adapts an interface whose signature we do not control.
//
//strict:allow contracts thin adapter, preconditions live in Plan
func Wrap(p Point) Point { return p }
