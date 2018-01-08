package bananas

import "github.com/WedgeNix/util"

func (v *Vars) statefulConversion(a arrangedPayload) (bananas, error) {
	bans := bananas{}
	innerBroke := []order{}

	util.Log("run over all orders/items")

	for _, o := range a.Orders {
		var savedO *order
		for _, i := range o.Items {
			skupc, err := v.skupc(i)
			if err != nil {
				println(util.Err(err).Error())
				continue
				// return nil, util.Err(err)
			}
			if v.broken[o.OrderID] {
				err = v.add(bans, &i)
				if err != nil {
					println(util.Err(err).Error())
					continue
					// return nil, util.Err(err)
				}
				savedO = &o
			} else if v.inWarehouse[skupc]-i.Quantity < 0 {
				innerBroke = append(innerBroke, o)
			} else {
				v.inWarehouse[skupc] -= i.Quantity
			}
		}
		if savedO != nil {
			v.taggables = append(v.taggables, *savedO)
		}
	}

	util.Log("hybrid orders that broke during stateful conversion")

	for _, o := range innerBroke {
		for _, i := range o.Items {
			err := v.add(bans, &i)
			if err != nil {
				println(util.Err(err).Error())
				continue
				// return nil, util.Err(err)
			}
		}
		v.taggables = append(v.taggables, o)
	}

	return bans, nil
}
