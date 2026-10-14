package lintcfg

import "os"

// Clean removes the scratch file.
func Clean() {
	os.Remove("scratch")
}
