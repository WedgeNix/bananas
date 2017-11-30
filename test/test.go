package main

import "fmt"

func main() {
	type banana struct {
		SKUPC    string
		Quantity int
	}

	bananas := map[string][]banana{
		"alex": []banana{
			{"dog", 1},
			{"cat", 2},
			{"mat", 4},
			{"hat", 8},
		},
		"larry": []banana{
			{"dog", 8},
			{"mat", 4},
			{"hat", 1},
		},
	}
	hybrids := map[string][]banana{
		"alex": []banana{
			{"mat", 16},
			{"cat", 32},
		},
	}

	// dog: 1
	// cat: 34
	// mat: 20
	// hat: 8

	for vend, bun := range hybrids {
		sum := map[string]int{}
		for _, ban := range bun {
			sum[ban.SKUPC] += ban.Quantity
		}
		for _, ban := range bananas[vend] {
			sum[ban.SKUPC] += ban.Quantity
		}
		bananas[vend] = nil
		for skupc, qt := range sum {
			bananas[vend] = append(bananas[vend], banana{skupc, qt})
		}
	}

	fmt.Println(bananas)
}
