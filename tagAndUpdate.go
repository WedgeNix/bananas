package bananas

import (
	"errors"
	"io/ioutil"

	"github.com/WedgeNix/util"
)

func (v *Vars) tagAndUpdate(b taggableBananas) error {
	util.Log("TAGGABLE_COUNT: ", len(v.taggables))

	for _, origOrd := range v.original {
		for i, tagOrd := range v.taggables {
			if origOrd.OrderNumber != tagOrd.OrderNumber {
				continue
			}

			var vSlice []string
			var upcSlice []string
			for _, item := range tagOrd.Items {
				poNum, err := v.poNum(&item, util.LANow())
				if err != nil {
					return err
				}
				vSlice = append(vSlice, poNum)
				upcSlice = append(upcSlice, item.UPC)
			}
			vList := onlyUnique(vSlice)
			upcList := onlyUnique(upcSlice)

			origOrd.AdvancedOptions.CustomField1 = vList
			origOrd.AdvancedOptions.CustomField2 = upcList
			v.taggables[i] = origOrd
		}
	}

	if L, step := len(v.taggables), 100; !sandbox && L > 0 {
		for i := 0; i < L; i += step {
			resp, err := v.login.Post(shipURL+`orders/createorders`, v.taggables[i:min(i+step, L)])
			if err != nil {
				return err
			}
			if resp.StatusCode >= 300 {
				return errors.New(resp.Status)
			}
			b, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			util.Log("CREATEORDERS_RESP: ", string(b))
		}
	}

	if sandbox {
		// util.Log("NEW_TAGGABLES: ", v.taggables)
	}

	return nil
}
