package bananas

import (
	"strings"

	"github.com/WedgeNix/bananas/ship"
	"github.com/WedgeNix/util"
)

func (v *Vars) filterDropShipment(pay *payload, rdc <-chan read) (filteredPayload, <-chan updated, <-chan error) {

	util.Log("copy payload and overwrite copy's orders")

	ords := pay.Orders

	if lO := len(pay.Orders); lO > 0 {
		util.Log("Pre-filter order count: ", lO)
	}
	pay.Orders = []ship.Order{}

	util.Log("go through all orders")

OrderLoop:
	for _, ord := range ords {
		if strings.ContainsAny(ord.AdvancedOptions.CustomField1, "Vv") && !ignoreCF1 {
			continue OrderLoop
		}

		items := ord.Items
		ord.Items = []ship.Item{}
		for _, itm := range items {
			if itm.HasW2() {
				ord.Items = append(ord.Items, itm)
			}
		}
		if len(ord.Items) == 0 {
			continue
		}

		pay.Orders = append(pay.Orders, ord)
	}
	util.Log(`len(ords)=`, len(ords))

	errcc := make(chan error, 1)

	var upc <-chan updated
	var errca, errcb <-chan error
	if !dontEmailButCreateOrders {
		v.rdOrdWg.Add(2)

		skuc, errca := v.j.UpdateAWS(rdc, v, ords)
		upc, errcb = v.j.UpdateNewSKUs(skuc, v, ords)
		if err := v.j.PrepareMonMail(upc, v); err != nil {
			util.Log(err)
			errcc <- err
			return filteredPayload{}, nil, util.MergeErr(errca, errcb, errcc)
		}
		go v.j.EmailOrders(v)

		v.rdOrdWg.Wait()
	}

	util.Log(`len(pay.Orders)=`, len(pay.Orders))
	dsOrds := []ship.Order{}
	for _, ord := range pay.Orders {
		switch ord.OrderStatus {
		case ship.AwaitingShipment, ship.OnHold:
			dsOrds = append(dsOrds, ord)
		}
	}

	if len(pay.Orders) < 1 {
		util.Log("No orders found after 'filtering'")
	}
	newFiltPay := filteredPayload(payload{Orders: dsOrds})

	if !dontEmailButCreateOrders {
		return newFiltPay, upc, util.MergeErr(errca, errcb, errcc)
	}
	return newFiltPay, nil, nil
}
