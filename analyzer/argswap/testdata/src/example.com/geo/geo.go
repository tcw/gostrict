package geo

type Meters float64

type Degrees float64

func Move(x, y, dist float64) {} // want `parameters x, y, dist of Move share type float64`

func Offset(lat, lon Degrees, d Meters) {} // defined types: fine

func Label(name string, id int, ok bool) {} // all different: fine

func Split(a int, b int, s string, t string) {} // want `parameters a, b of Split share type int` `parameters s, t of Split share type string`

// Max returns the larger value.
//
//strict:allow argswap commutative, order does not matter
func Max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

/* want `needs a reason` */ //strict:allow argswap
func Min(a, b int) int      { return a }

type byLen []string

func (s byLen) Len() int           { return len(s) }
func (s byLen) Less(i, j int) bool { return len(s[i]) < len(s[j]) } // ignored method name
func (s byLen) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func (m Meters) Between(a, b Meters) bool { return a <= m && m <= b }

func Clamp[T ~float64](v, lo, hi T) T { return v } // type parameters: fine

func Flags(verbose, dryRun bool) {} // want `parameters verbose, dryRun of Flags share type bool`

func Pair(a byte, b uint8) {} // want `parameters a, b of Pair share type byte`
