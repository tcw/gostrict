// want package:"pure"

// Package core holds the domain rules.
//
//strict:pure
package core

import (
	"errors"
	"fmt"
	"net/url"
	"os" // want `package core is //strict:pure but imports "os" \(file system`
	"strings"
	"sync" // want `imports "sync" \(shared mutable state`
	"time"

	"example.com/dirty" // want `imports "example.com/dirty", which is not marked //strict:pure`
	"example.com/mathx"
)

var ErrEmpty = errors.New("empty") // error sentinel: fine

var cache = map[string]int{} // want `declares package-level var cache, which is mutable global state`

var _ fmt.Stringer = name("") // blank assertion: fine

type name string

func (n name) String() string { return string(n) }

func Plan(s string) (string, error) {
	if s == "" {
		return "", ErrEmpty
	}
	t := time.Now()                        // want `Plan calls time.Now, which reads the clock`
	fmt.Println(t)                         // want `Plan calls fmt.Println, which writes to stdout`
	go func() {}()                         // want `Plan starts a goroutine`
	_ = fmt.Sprintf("%v", mathx.Square(2)) // pure formatting: fine
	_, _ = url.Parse(s)                    // net/url is pure: fine
	_ = os.Args
	var mu sync.Mutex
	_ = &mu
	_ = dirty.Hello()
	return strings.ToUpper(s), nil
}

func Window(d time.Duration) time.Duration { return d * 2 } // time types are fine

// Debug prints for local debugging.
//
//strict:allow purity debugging aid, removed before release
func Debug(v any) { fmt.Println(v) }
