// Package shell is impure and unmarked: nothing is reported.
package shell

import (
	"fmt"
	"os"
	"time"

	"example.com/core"
)

func Main() {
	s, _ := core.Plan(os.Args[0])
	fmt.Println(s, time.Now())
}
