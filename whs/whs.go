package whs

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/WedgeNix/bananas/skc"
)

var (
	mustCompile = regexp.MustCompile
	itoa        = strconv.Itoa
	sprintf     = fmt.Sprintf
)

type Warehouse map[skc.SKUPC]int

func (whs Warehouse) Copy() Warehouse {
	dwhs := make(Warehouse)
	for skupc, n := range whs {
		dwhs[skupc] = n
	}
	return dwhs
}

// func (whs *Warehouse) Decode(r io.Reader) error {
// 	var jwhs map[string]int
// 	if err := json.NewDecoder(r).Decode(&jwhs); err != nil {
// 		return err
// 	}
// 	dwhs := make(Warehouse)
// 	for raw, n := range jwhs {
// 		skupc, err := skc.ParseRaw(raw)
// 		if err != nil {
// 			return err
// 		}
// 		dwhs[skupc] = n
// 	}
// 	*whs = dwhs
// 	return nil
// }

// func (whs Warehouse) Encode(w io.Writer) error {
// 	jwhs := make(map[string]int, len(whs))
// 	for skupc, n := range whs {
// 		jwhs[skupc.String()] = n
// 	}
// 	return json.NewEncoder(w).Encode(jwhs)
// }

func (whs Warehouse) String() string {
	type skcn struct {
		skc.SKUPC
		int
	}
	var skcns []skcn
	var sL, uL int
	for skupc, n := range whs {
		if L := len(skupc.SKU); L > sL {
			sL = L
		}
		if L := len(skupc.UPC); L > uL {
			uL = L
		}
		skcns = append(skcns, skcn{skupc, n})
	}
	sort.Slice(skcns, func(i, j int) bool {
		return skcns[i].SKU < skcns[j].SKU
	})
	lns := make([]string, len(skcns))
	for i, skcn := range skcns {
		lns[i] = sprintf("%-"+itoa(sL+uL+2)+"s %d", sprintf("%"+itoa(sL)+"s%s%s:", skcn.SKU, skc.Sep, skcn.UPC), skcn.int)
	}
	return string(strings.Join(lns, "\n"))
}

type Location string

func (loc Location) ContainsW2() bool {
	return mustCompile(`W[0-9](-[A-Z0-9]+)+`).Match([]byte(loc))
}
func (loc Location) OnHand() int {
	sum := 0
	for _, whouse := range mustCompile(`(, *|\b)[0-9A-Z]+ *\(([0-9]+)\)`).FindAllSubmatch([]byte(loc), -1) {
		n, _ := strconv.Atoi(string(whouse[2]))
		sum += n
	}
	return sum
}
