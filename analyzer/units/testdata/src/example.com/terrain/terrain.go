// Package terrain is unit-checked.
//
//strict:units
package terrain

type Meters float64

type Point struct {
	Lat, Lon float64 // want `field Point.Lat uses raw float64`
	Elev     Meters
	Samples  []float64 // want `field Point.Samples uses raw float64`
	weight   float64   // unexported: fine
	Count    int       // ints are off by default
}

func Slope(a, b Point) float64 { return 0 } // want `result of exported Slope uses raw float64`

func Climb(p Point, rate float64) Meters { return 0 } // want `parameter of exported Climb uses raw float64`

func Heights(m map[string]*float32) {} // want `parameter of exported Heights uses raw float32`

func helper(x float64) float64 { return x } // unexported: fine

type grid struct{}

func (grid) Cell(x float64) {} // unexported receiver: fine

func (p *Point) Scale(f float64) {} // want `parameter of exported Scale uses raw float64`

// Ratio is dimensionless.
//
//strict:allow units a ratio has no unit
func Ratio(a, b Meters) float64 { return float64(a / b) }
