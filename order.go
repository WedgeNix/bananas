package bananas

import (
	"bytes"
	"errors"
	"html/template"
	"sync"
	"time"

	"github.com/WedgeNix/util"
	wedgemail "github.com/WedgeNix/wedgeMail"
)

func (v *Vars) order(b bananas, whouse Warehouse) (taggableBananas, []error) {

	util.Log("removing monitors from drop ship emails")
	for vend, set := range v.settings {
		if set.Monitor && !set.Hybrid {
			delete(b, vend)
		}
	}

	util.Log("parse HTML template")
	tmpl, err := template.ParseFiles("vendor-email-tmpl.html")
	if err != nil {
		return nil, []error{util.Err(err)}
	}

	// login := util.EmailLogin{
	// 	User: comEmailUser,
	// 	Pass: comEmailPass,
	// 	SMTP: comEmailSMTP,
	// }

	login, err := wedgemail.StartMail()
	if err != nil {
		return nil, []error{util.Err(err)}
	}

	// if sandbox {
	// 	login.User = appUser
	// 	login.Pass = appPass
	// }

	var emailing sync.WaitGroup
	start := time.Now()

	mailerrc := make(chan error)

	hyBans := bananas{}

	for V, set := range v.settings {
		if set.Monitor && !set.Hybrid {
			continue
		}

		vend := V // new "variables" for closure
		bun, exists := b[vend]
		if !exists {
			util.Log("send empty email (drop ship)")
		}

		util.Log("<<< Removing SKUs from order we have enough of locally >>>")
		var newbun bunch
		for _, ban := range bun {
			if buyDiff := ban.Quantity - whouse[ban.SKUPC]; buyDiff > 0 {
				ban.Quantity = buyDiff
				newbun = append(newbun, ban)
			} else {
				delete(whouse, ban.SKUPC)
			}
		}
		bun = newbun

		if set.Hybrid {
			// if B, exists := b[V]; exists {
			// 	hyBans[V] = B
			// }
			hyBans[V] = bun // toss empties in as well
			continue
		}

		// util.Log(vend + " ORDER:\n" + bun.String())

		emailing.Add(1)
		go func() {
			defer util.Log("goroutine is finished emailing an email")
			defer emailing.Done()

			util.Log("goroutine is starting to email a drop ship vendor")

			t := util.LANow()
			po := v.settings[vend].PONum + "-" + t.Format("20060102")

			inj := injection{
				Vendor: vend,
				Date:   t.Format("01/02/2006"),
				PO:     po,
				Bunch:  bun,
			}

			buf := &bytes.Buffer{}
			err := tmpl.Execute(buf, inj)
			if err != nil {
				util.Log(vend, " ==> ", bun)
				mailerrc <- err
				return
			}

			to := []string{appUser}
			if !sandbox {
				to = append(v.settings[vend].Email, to...)
			}

			var att wedgemail.Attachment
			if v.settings[vend].FileDownload && len(bun) > 0 {
				att, err = bun.csv(po + ".csv")
				if err != nil {
					mailerrc <- err
				}
			}

			if !paperless && !dontEmailButCreateOrders {
				email := buf.String()
				err := login.Email(to, "WedgeNix PO#: "+po, email, att)
				if err != nil {
					mailerrc <- errors.New("error in emailing " + vend)
				}
			}
		}()
	}

	if !dontEmailButCreateOrders {
		util.Log("Piping over hybrid bananas for Monitor to email")
		v.j.hybrids <- hyBans
	}

	util.Log("Drop ship: wait for goroutines to finish emailing")
	emailing.Wait()
	util.Log("Drop ship: Emailing round-trip: ", time.Since(start))

	close(mailerrc)

	mailerrs := []error{}
	for mailerr := range mailerrc {
		if mailerr == nil {
			continue
		}
		mailerrs = append(mailerrs, mailerr)
	}

	if len(mailerrs) == 0 {
		mailerrs = nil
	}
	return taggableBananas(b), mailerrs
}
