// Package counts is unit-checked with -ints.
//
//strict:units
package counts

type Items int

type Stock struct {
	Count  int   // want `field Stock.Count uses raw int`
	Levels []int // want `field Stock.Levels uses raw int`
	Total  Items
	Hash   [32]byte // byte arrays are data: fine
	Raw    []byte   // byte slices are data: fine
}

func Take(n int) Items { return Items(n) } // want `parameter of exported Take uses raw int`

func Parse(src []byte) ([]byte, error) { return src, nil } // byte slices are data: fine

func Rows(src [][]byte) {} // a slice of byte slices is data too: fine

func Flag(b byte) {} // want `parameter of exported Flag uses raw byte`

func Sum(m map[string][]uint8) {} // []uint8 is []byte: fine
