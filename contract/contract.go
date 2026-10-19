// Package contract states preconditions, postconditions and invariants in
// ordinary Go code.
//
// A failed contract is a programmer error, not an expected failure, so it
// panics with a *Violation instead of returning an error. Expected failures
// (bad input from outside the program, I/O errors) should still be returned
// as error values.
//
// Build with -tags nocontracts to turn every check into a no-op. The
// condition argument is still evaluated, so keep conditions cheap and free
// of side effects.
package contract

import (
	"fmt"
	"runtime"
	"strings"
)

// Kind names the type of contract that failed.
type Kind string

// The kinds of contract.
const (
	KindRequire   Kind = "precondition"
	KindEnsure    Kind = "postcondition"
	KindInvariant Kind = "invariant"
)

// Violation is the panic value raised when a contract fails.
type Violation struct {
	Kind    Kind
	Message string
	Func    string // fully qualified name of the function that stated the contract
	File    string
	Line    int
}

// Error formats the violation as "file:line: precondition failed in pkg.Func: message".
func (v *Violation) Error() string {
	return fmt.Sprintf("%s:%d: %s failed in %s: %s", v.File, v.Line, v.Kind, v.Func, v.Message)
}

// violation builds a Violation describing the caller of Require, Ensure or
// Invariant. skip is the number of frames between this function and that
// caller.
func violation(kind Kind, msg string, skip int) *Violation {
	v := &Violation{Kind: kind, Message: msg, Func: "unknown", File: "unknown"}
	pc, file, line, ok := runtime.Caller(skip + 1)
	if !ok {
		return v
	}
	v.File, v.Line = file, line
	if fn := runtime.FuncForPC(pc); fn != nil {
		v.Func = fn.Name()
		if i := strings.LastIndex(v.Func, "/"); i >= 0 {
			v.Func = v.Func[i+1:]
		}
	}
	return v
}
