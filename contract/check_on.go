//go:build !nocontracts

package contract

// Enabled reports whether contracts are checked in this build.
const Enabled = true

// Require states a precondition: something the caller must guarantee.
// It panics with a *Violation if cond is false.
func Require(cond bool, msg string) {
	if !cond {
		panic(violation(KindRequire, msg, 1))
	}
}

// Ensure states a postcondition: something this function guarantees to its
// caller. Call it just before returning. It panics with a *Violation if cond
// is false.
func Ensure(cond bool, msg string) {
	if !cond {
		panic(violation(KindEnsure, msg, 1))
	}
}

// Invariant states something that must hold at this point in the code.
// It panics with a *Violation if cond is false.
func Invariant(cond bool, msg string) {
	if !cond {
		panic(violation(KindInvariant, msg, 1))
	}
}
