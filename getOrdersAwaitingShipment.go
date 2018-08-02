package bananas

import (
	"time"

	"github.com/WedgeNix/util"
)

func (v *Vars) getOrdersAwaitingShipment() (*payload, error) {
	pg := 1

	util.Log(`Bananas hit @`, util.LANow())

	a := v.j.cfgFile.LastLA
	b := util.LANow()
	if Days := b.Sub(a).Hours() / 24; Days > 2 {
		util.Log("[[[WARNING]]]")
		util.Log("[[[WARNING]]] days since bananas last hit =", int(Days))
		util.Log("[[[WARNING]]]")
	}
	// la, _ := time.LoadLocation("America/Los_Angeles")
	// a := time.Date(2017, time.November, 10, 8, 2, 38, 0, la)
	// b := time.Date(2017, time.November, 13, 7, 57, 0, 0, la)
	util.Log(`last=`, a)
	util.Log(`today=`, b)
	// 3/4ths of a day to give wiggle room for Matt's timing
	if !sandbox && b.Sub(a).Hours()/24 < 0.75 {
		return nil, util.NewErr("same day still; reset AWS config LastLA date")
	}

	pay := &payload{}
	reqs, secs, err := v.getPage(pg, pay, a, b)
	if err != nil {
		return pay, util.Err(err)
	}

	for pay.Page < pay.Pages {
		pg++
		ords := pay.Orders

		pay = &payload{}
		reqs, secs, err = v.getPage(pg, pay, a, b)
		if err != nil {
			return pay, util.Err(err)
		}
		if reqs < 1 {
			time.Sleep(time.Duration(secs) * time.Second)
		}

		pay.Orders = append(ords, pay.Orders...)
	}

	return pay, nil
}
