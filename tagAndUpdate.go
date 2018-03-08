package bananas

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/WedgeNix/util"
)

func (v *Vars) tagAndUpdate(b taggableBananas) error {
	tagCnt := len(v.taggables)

	util.Log("TAGGABLE_COUNT: ", tagCnt)

	for _, origOrd := range v.original {
		for i, tagOrd := range v.taggables {
			if origOrd.OrderNumber != tagOrd.OrderNumber {
				continue
			}

			var vSlice []string
			var upcSlice []string
			for _, itm := range tagOrd.Items {
				poNum, err := v.poNum(&itm, util.LANow())
				if err != nil {
					return err
				}
				vSlice = append(vSlice, poNum)
				upcSlice = append(upcSlice, itm.UPC)
			}
			vList := onlyUnique(vSlice)
			upcList := onlyUnique(upcSlice)

			origOrd.AdvancedOptions.CustomField1 = vList
			origOrd.AdvancedOptions.CustomField2 = upcList
			v.taggables[i] = origOrd
		}
	}

	if dontTag || sandbox || tagCnt == 0 {
		return nil
	}

	start, errs := time.Now().In(la), []error{}
	for i := 0; i < tagCnt; {
		nexti := min(i+100, tagCnt)
		rng := itoa(i+1) + "~" + itoa(nexti) + " / " + itoa(tagCnt)

		util.Log("/createorders... " + rng)
		resp, err := v.login.Post(shipURL+`orders/createorders`, v.taggables[i:nexti])
		util.Log("/createorders!   " + rng)

		i = nexti
		if err != nil || resp.StatusCode >= 300 {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 { // errors happened; double-check ShipStation
		return checkPO(v.login, start, tagCnt)
	}
	return nil
}

// checkPO checks ShipStation for all modified orders after
// the start time, counting how many have custom field-1
// set to the po-date of the start time.
func checkPO(login util.HTTPLogin, start time.Time, tagged int) error {
	pay := payload{Page: 1, Pages: 2}
	matches := 0
	for ; pay.Page < pay.Pages; pay.Page++ {
		query := make(url.Values)
		query.Set("modifyDateStart", start.Format(ssDateFmt))
		query.Set("page", itoa(pay.Page))
		resp, err := login.Get(shipURL + `orders?` + query.Encode())
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err = json.NewDecoder(resp.Body).Decode(&pay); err != nil {
			return err
		}
		for _, ord := range pay.Orders {
			if strings.Contains(ord.AdvancedOptions.CustomField1, start.Format(poFormat)) {
				matches++
			}
		}
	}
	if matches < tagged {
		return errors.New("not all orders tagged")
	}
	return nil
}
