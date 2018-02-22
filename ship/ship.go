package ship

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/WedgeNix/bananas/query"
	"github.com/WedgeNix/bananas/skc"
	"github.com/WedgeNix/bananas/whs"
)

type (
	SortBy      string
	SortDir     string
	Page        int
	PageSize    int
	OrderStatus string
)

const (
	Host    = "https://ssapi.shipstation.com"
	DateFmt = "2006-01-02"
	TimeFmt = DateFmt + " 15:04:05"

	OrderDate        SortBy      = "OrderDate"
	ModifyDate       SortBy      = "ModifyDate"
	CreateDate       SortBy      = "CreateDate"
	Asc              SortDir     = "ASC"
	Desc             SortDir     = "DESC"
	AwaitingPayment  OrderStatus = "awaiting_payment"
	AwaitingShipment OrderStatus = "awaiting_shipment"
	Shipped          OrderStatus = "shipped"
	OnHold           OrderStatus = "on_hold"
	Cancelled        OrderStatus = "cancelled"
)

func (p Page) String() (string, error) {
	if p < 0 {
		return "", errors.New("page size less than 0")
	} else if p == 0 {
		return "1", nil
	}
	return itoa(int(p)), nil
}

func (p PageSize) String() (string, error) {
	if p < 0 {
		return "", errors.New("page size less than 0")
	} else if p > 500 {
		return "", errors.New("page size greater than 500")
	} else if p == 0 {
		return "100", nil
	}
	return itoa(int(p)), nil
}

var (
	newerr  = errors.New
	itoa    = strconv.Itoa
	atoi    = strconv.Atoi
	println = fmt.Println
)

type ShipStation struct {
	inited bool

	key,
	secret string
}

type OrdersQ struct {
	CustomerName,
	ItemKeyword string

	CreateDateStart,
	CreateDateEnd,
	ModifyDateStart,
	ModifyDateEnd,
	OrderDateStart,
	OrderDateEnd time.Time

	OrderNumber string

	OrderStatus OrderStatus

	PaymentDateStart,
	PaymentDateEnd time.Time

	StoreID  int
	SortBy   SortBy
	SortDir  SortDir
	Page     Page
	PageSize PageSize
}

func (ship *ShipStation) init() error {
	if ship.inited {
		return nil
	}

	key, found := os.LookupEnv("SHIPSTATION_KEY")
	if !found {
		return errors.New("missing SHIPSTATION_KEY")
	}
	secret, found := os.LookupEnv("SHIPSTATION_SECRET")
	if !found {
		return errors.New("missing SHIPSTATION_SECRET")
	}

	ship.key = key
	ship.secret = secret
	ship.inited = true
	return nil
}

func (ship *ShipStation) GetOrders(q OrdersQ) ([]Order, error) {
	if err := ship.init(); err != nil {
		return nil, err
	}

	var orders []Order
	pay := Payload{
		Page:  q.Page,
		Pages: q.Page + 1,
	}

	qry := make(query.Q)
	qry.Set("customerName", q.CustomerName)
	qry.Set("itemKeyword", q.ItemKeyword)
	qry.SetTime("createDateStart", q.CreateDateStart, TimeFmt)
	qry.SetTime("createDateEnd", q.CreateDateEnd, TimeFmt)
	qry.SetTime("modifyDateStart", q.ModifyDateStart, TimeFmt)
	qry.SetTime("modifyDateEnd", q.ModifyDateEnd, TimeFmt)
	qry.SetTime("orderDateStart", q.OrderDateStart, TimeFmt)
	qry.SetTime("orderDateEnd", q.OrderDateEnd, TimeFmt)
	qry.Set("orderNumber", q.OrderNumber)
	qry.Set("orderStatus", string(q.OrderStatus))
	qry.SetTime("paymentDateStart", q.PaymentDateStart, DateFmt)
	qry.SetTime("paymentDateEnd", q.PaymentDateEnd, DateFmt)
	qry.SetInt("storeId", q.StoreID)
	qry.Set("sortBy", string(q.SortBy))
	qry.Set("sortDir", string(q.SortDir))

	pageSize, err := q.PageSize.String()
	if err != nil {
		return nil, err
	}
	qry.Set("pageSize", pageSize)

	for ; pay.Page <= pay.Pages; pay.Page++ {
		page, err := pay.Page.String()
		if err != nil {
			return nil, err
		}
		qry.Set("page", page)

		u, err := url.Parse(Host + "/orders")
		if err != nil {
			return nil, err
		}
		u.RawQuery = qry.Encode()
		urlStr := u.String()

		req, err := http.NewRequest(http.MethodGet, urlStr, nil)
		if err != nil {
			println(urlStr)
			return nil, err
		}
		req.SetBasicAuth(ship.key, ship.secret)

		cl := http.Client{}
		resp, err := cl.Do(req)
		if err != nil {
			println(urlStr)
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			println(urlStr)
			return nil, errors.New(resp.Status)
		}

		if err := json.NewDecoder(resp.Body).Decode(&pay); err != nil {
			return nil, err
		}
		orders = append(orders, pay.Orders...)

		rem, err := atoi(resp.Header.Get("X-Rate-Limit-Remaining"))
		if err != nil {
			return nil, err
		} else if rem > 0 {
			continue
		}

		wait, err := atoi(resp.Header.Get("X-Rate-Limit-Reset"))
		if err != nil {
			return nil, err
		}
		time.Sleep(time.Duration(wait) * time.Second)
	}

	return orders, nil
}

type Payload struct {
	Orders []Order
	Total  int
	Page,
	Pages Page
}

type Order struct {
	OrderID                  int
	OrderNumber              string
	OrderKey                 string
	OrderDate                string
	CreateDate               string
	ModifyDate               string
	PaymentDate              string
	ShipByDate               string
	OrderStatus              OrderStatus
	CustomerID               int
	CustomerUsername         string
	CustomerEmail            string
	BillTo                   interface{}
	ShipTo                   interface{}
	Items                    []Item
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
	AdvancedOptions          AdvancedOptions
	TagIDs                   []int
	UserID                   string
	ExternallyFulfilled      bool
	ExternallyFulfilledBy    string
}

func (o Order) Copy() Order {
	do := o
	tagIDs := make([]int, len(o.TagIDs))
	copy(tagIDs, o.TagIDs)
	do.TagIDs = tagIDs
	items := make([]Item, len(o.Items))
	copy(items, o.Items)
	do.Items = items
	return do
}

func CopyOrders(ords []Order) []Order {
	dords := make([]Order, len(ords))
	for i, ord := range ords {
		dords[i] = ord.Copy()
	}
	return dords
}

type Item struct {
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
	WarehouseLocation string
	Options           interface{}
	ProductID         int
	FulfillmentSKU    string
	Adjustment        bool
	UPC               string
	CreateDate        string
	ModifyDate        string
}

type AdvancedOptions struct {
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

func (o Order) IsTagged() bool {
	return regexp.MustCompile(`^[Vv][0-9]+-[0-9]{4,8}$`).Match([]byte(o.AdvancedOptions.CustomField1))
}

func AllW1(ords []Order) (whs.Warehouse, error) {
	w1 := whs.Warehouse{}
	for _, ord := range ords {
		for _, itm := range ord.Items {
			skupc, err := itm.SKUPC()
			if err != nil {
				return nil, err
			}

			w1[skupc] = itm.OnHand()
		}
	}
	return w1, nil
}

func (i Item) SKUPC() (skc.SKUPC, error) {
	return skc.Parse(i.SKU, i.UPC)
}

func (i Item) HasW2() bool {
	return whs.Location(i.WarehouseLocation).ContainsW2()
}

func (i Item) OnHand() int {
	return whs.Location(i.WarehouseLocation).OnHand()
}

func FiltDropShip(ords []Order) []Order {
	var dords []Order

	for _, ord := range ords {
		if ord.IsTagged() {
			continue
		}
		if ord.OrderStatus != AwaitingShipment {
			continue
		}

		for _, itm := range ord.Items {
			if itm.HasW2() {
				dords = append(dords, ord.Copy())
			}
		}
	}

	return dords
}

func AllBought(ords []Order) (whs.Warehouse, error) {
	bought := make(whs.Warehouse)

	for _, ord := range ords {
		for _, itm := range ord.Items {
			skupc, err := itm.SKUPC()
			if err != nil {
				return nil, err
			}

			bought[skupc] += itm.Quantity
		}
	}

	return bought, nil
}

func AllToBuy(w1 whs.Warehouse, ords []Order) (whs.Warehouse, error) {
	allBought, err := AllBought(ords)
	if err != nil {
		return nil, err
	}

	toBuy := make(whs.Warehouse)

	for skupc, bought := range allBought {
		if buy := bought - w1[skupc]; buy > 0 {
			toBuy[skupc] = buy
		}
	}

	return toBuy, nil
}
