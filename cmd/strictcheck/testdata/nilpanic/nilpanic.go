package nilpanic

// First returns the value stored under "a".
func First(m map[string]*int) int {
	return *m["a"]
}
