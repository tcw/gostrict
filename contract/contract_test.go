//go:build !nocontracts

package contract_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/tcw/gostrict/contract"
)

func sqrtFloor(n int) int {
	contract.Require(n >= 0, "n must be non-negative")
	r := 0
	for (r+1)*(r+1) <= n {
		r++
	}
	contract.Ensure(r*r <= n && (r+1)*(r+1) > n, "r is the integer square root of n")
	return r
}

func catch(f func()) (v *contract.Violation) {
	defer func() {
		if r := recover(); r != nil {
			err, ok := r.(error)
			if !ok || !errors.As(err, &v) {
				panic(r)
			}
		}
	}()
	f()
	return nil
}

func TestRequirePasses(t *testing.T) {
	if got := sqrtFloor(17); got != 4 {
		t.Fatalf("sqrtFloor(17) = %d, want 4", got)
	}
}

func TestRequireFails(t *testing.T) {
	v := catch(func() { sqrtFloor(-1) })
	if v == nil {
		t.Fatal("expected a contract violation")
	}
	if v.Kind != contract.KindRequire {
		t.Errorf("Kind = %q, want %q", v.Kind, contract.KindRequire)
	}
	if !strings.HasSuffix(v.Func, "contract_test.sqrtFloor") {
		t.Errorf("Func = %q, want it to name sqrtFloor", v.Func)
	}
	if !strings.HasSuffix(v.File, "contract_test.go") || v.Line == 0 {
		t.Errorf("location = %s:%d, want contract_test.go", v.File, v.Line)
	}
	if !strings.Contains(v.Error(), "precondition failed in") || !strings.Contains(v.Error(), "n must be non-negative") {
		t.Errorf("Error() = %q", v.Error())
	}
}

func TestEnsureAndInvariantKinds(t *testing.T) {
	if v := catch(func() { contract.Ensure(false, "post") }); v == nil || v.Kind != contract.KindEnsure {
		t.Errorf("Ensure: got %+v", v)
	}
	if v := catch(func() { contract.Invariant(false, "inv") }); v == nil || v.Kind != contract.KindInvariant {
		t.Errorf("Invariant: got %+v", v)
	}
}
