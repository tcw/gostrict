//go:build nocontracts

package contract

// Enabled reports whether contracts are checked in this build.
const Enabled = false

// Require is a no-op in builds with the nocontracts tag.
func Require(bool, string) {}

// Ensure is a no-op in builds with the nocontracts tag.
func Ensure(bool, string) {}

// Invariant is a no-op in builds with the nocontracts tag.
func Invariant(bool, string) {}
