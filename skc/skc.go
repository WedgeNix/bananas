package skc

import (
	"errors"
	"regexp"
	"strings"
)

const (
	Sep = "\t"
)

// type (
// 	SKU string
// 	UPC string
// )

// func ParseSKU(sku string) (SKU, error) {
// 	if len(sku) < 3 {
// 		return "", errors.New("invalid sku '" + sku + "'")
// 	}
// 	return SKU(sku), nil
// }
// func ParseUPC(upc string) (UPC, error) {
// 	if regexp.MustCompile(`^[0-9]+$`).Find([]byte(upc)) == nil {
// 		return "", errors.New("invalid upc '" + upc + "'")
// 	}
// 	return UPC(upc), nil
// }

type SKUPC struct {
	SKU string
	UPC string
}

func ParseRaw(skupc string) (SKUPC, error) {
	i := strings.LastIndex(skupc, Sep)
	if i == -1 {
		return SKUPC{}, errors.New("no '" + Sep + "' separator in '" + skupc + "'")
	}
	return Parse(skupc[:i], skupc[i+1:])
}
func Parse(sku, upc string) (SKUPC, error) {
	if len(sku) < 3 {
		return SKUPC{}, errors.New("invalid sku '" + sku + "'")
	}
	if regexp.MustCompile(`^[0-9]+$`).Find([]byte(upc)) == nil {
		return SKUPC{}, errors.New("invalid upc '" + upc + "'")
	}
	return SKUPC{sku, upc}, nil
}

func (skupc SKUPC) String() string {
	return skupc.SKU + Sep + skupc.UPC
}
