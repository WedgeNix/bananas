package bananas

import (
	"errors"
	"regexp"
	"strconv"
)

type Warehouse map[string]int

func ParseWarehouse(pay payload) Warehouse {
	whouse := make(Warehouse)
	for _, ord := range pay.Orders {
		for _, itm := range ord.Items {
			n, err := itm.WarehouseLocation.Local()
			if err != nil {
				continue
			}
			whouse[itm.SKU] = n
		}
	}
	return whouse
}

// WarehouseLocation is the SSItem's locations in various warehouses.
type WarehouseLocation string

// W2 checks if the warehouse is not local.
func (loc WarehouseLocation) W2() bool {
	return regexp.MustCompile(`W[0-9](-[A-Z0-9]+)+`).Match([]byte(loc))
}

// Local parses local warehouse quantities and sums them up.
func (loc WarehouseLocation) Local() (int, error) {
	sum := 0
	whouses := regexp.MustCompile(`(, *|\b)[0-9A-Z]+ *\(([0-9]+)\)`).FindAllSubmatch([]byte(loc), -1)
	if len(whouses) == 0 {
		return 0, errors.New("no warehouse locations")
	}
	for _, whouse := range whouses {
		n, _ := strconv.Atoi(string(whouse[2]))
		sum += n
	}
	return sum, nil
}
