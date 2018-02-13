package bananas

import (
	"regexp"
	"sync"

	"github.com/WedgeNix/util"
	"github.com/WedgeNix/warehouse-settings/app"
)

// Monitor JUST FOR TESTING NEED TO REPLACEW
type Monitor struct {
	Pending bool
}

// Vars is the bananas data structure.
type Vars struct {
	// settings holds all static vendor warehouse information.
	settings app.Bananas

	vendExprs map[string]*regexp.Regexp

	j *jit

	login util.HTTPLogin

	// hasVendor checks a warehouse location string to see if the item exists in quantity on their end.
	hasVendor *regexp.Regexp
	// vendorOnly picks out vendor-only warehouse locations with quantities.
	// vendorOnly *regexp.Regexp
	// localOnly picks out local-only warehouse locations with quantities.
	localOnly *regexp.Regexp
	// quantity grabs the value within a single warehouse location.
	quantity *regexp.Regexp
	// number is just a series of numbers.
	number *regexp.Regexp

	// inWarehouse keeps track of quantities of on-hand goods based on SKU/UPC.
	inWarehouse map[string]int

	// broken tracks orders needing complete order requests.
	broken map[int]bool

	// vendors holds an instant mapping of warehouse locations to vendor names.
	vendors map[string]string

	// taggables
	taggables []order

	original []order

	rdOrdWg sync.WaitGroup

	// holds onto errors that need to be handled later for damage control
	errs []error
}

// Payload is the first level of a ShipStation HTTP response body.
type payload struct {
	Orders []order
	Total  int
	Page   int
	Pages  int
}

// FilteredPayload is a type-safe payload already filtered.
type filteredPayload payload

// ArrangedPayload is a type-safe payload already arranged.
type arrangedPayload payload

// Banana is a single order to be requested for a vendor.
type banana struct {
	SKUPC    string
	Quantity int
}

// Bunch is a slice of bananas.
type bunch []banana

// bananas maps a vendor name to a SKU to a requested quantity.
type bananas map[string]bunch

// Taggablebananas is ready-to-be-tagged bananas.
type taggableBananas bananas

// Item is the third level of a ShipStation HTTP response body.
type item struct {
	OrderItemID       int
	LineItemKey       string
	SKU               string
	Name              string
	ImageURL          string
	Weight            interface{}
	Quantity          int
	UnitPrice         float32
	TaxAmount         float32
	ShippingAmount    float32
	WarehouseLocation WarehouseLocation
	Options           interface{}
	ProductID         int
	FulfillmentSKU    string
	Adjustment        bool
	UPC               string
	CreateDate        string
	ModifyDate        string
}

// Order is the second level of a ShipStation HTTP response body.
type order struct {
	OrderID                  int
	OrderNumber              string
	OrderKey                 string
	OrderDate                string
	CreateDate               string
	ModifyDate               string
	PaymentDate              string
	ShipByDate               string
	OrderStatus              string
	CustomerID               int
	CustomerUsername         string
	CustomerEmail            string
	BillTo                   interface{}
	ShipTo                   interface{}
	Items                    []item
	OrderTotal               float32
	AmountPaid               float32
	TaxAmount                float32
	ShippingAmount           float32
	CustomerNotes            string
	InternalNotes            string
	Gift                     bool
	GiftMessage              string
	PaymentMethod            string
	RequestedShippingService string
	CarrierCode              string
	ServiceCode              string
	PackageCode              string
	Confirmation             string
	ShipDate                 string
	HoldUntilDate            string
	Weight                   interface{}
	Dimensions               interface{}
	InsuranceOptions         interface{}
	InternationalOptions     interface{}
	AdvancedOptions          advancedOptions
	TagIDs                   []int
	UserID                   string
	ExternallyFulfilled      bool
	ExternallyFulfilledBy    string
}

type orderUpdate struct {
	OrderNumber     string
	OrderKey        string
	OrderDate       string
	OrderStatus     string
	BillTo          interface{}
	ShipTo          interface{}
	AdvancedOptions advancedOptionsUpdate
}

type advancedOptionsUpdate struct {
	StoreID      int
	CustomField1 string
	CustomField2 string
	CustomField3 string
}

// AdvancedOptions holds the "needed" custom fields for post-email tagging.
type advancedOptions struct {
	WarehouseID       int
	NonMachinable     bool
	SaturdayDelivery  bool
	ContainsAlcohol   bool
	MergedOrSplit     bool
	MergedIDs         interface{}
	ParentID          interface{}
	StoreID           int
	CustomField1      string
	CustomField2      string
	CustomField3      string
	Source            string
	BillToParty       interface{}
	BillToAccount     interface{}
	BillToPostalCode  interface{}
	BillToCountryCode interface{}
}

// Injection injects structure into email template.
type injection struct {
	Vendor string
	Date   string
	PO     string
	Bunch  bunch
}
