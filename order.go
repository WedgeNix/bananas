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

func (v *Vars) order(b bananas) (taggableBananas, []error) {

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
		if set.Hybrid {
			if B, exists := b[V]; exists {
				hyBans[V] = B
			}
			continue
		}

		B, exists := b[V]
		if !exists {

			util.Log("send empty email")

		}
		vendor, bunch := V, B // new "variables" for closure

		emailing.Add(1)
		go func() {
			defer util.Log("goroutine is finished emailing an email")
			defer emailing.Done()

			util.Log("goroutine is starting to email a drop ship vendor")

			t := util.LANow()
			po := v.settings[vendor].PONum + "-" + t.Format("20060102")

			inj := injection{
				Vendor: vendor,
				Date:   t.Format("01/02/2006"),
				PO:     po,
				Bunch:  bunch,
			}

			buf := &bytes.Buffer{}
			err := tmpl.Execute(buf, inj)
			if err != nil {
				util.Log(vendor, " ==> ", bunch)
				mailerrc <- err
				return
			}

			to := []string{appUser}
			if !sandbox {
				to = append(v.settings[vendor].Email, to...)
			}

			var att wedgemail.Attachment
			if v.settings[vendor].FileDownload && len(bunch) > 0 {
				att, err = bunch.csv(po + ".csv")
				if err != nil {
					mailerrc <- err
				}
			}

			if !paperless && !dontEmailButCreateOrders {
				email := buf.String()
				err := login.Email(to, "WedgeNix PO#: "+po, email, att)
				if err != nil {
					mailerrc <- errors.New("error in emailing " + vendor)
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
	// login.Stop()

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
