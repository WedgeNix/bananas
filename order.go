package bananas

import (
	"bytes"
	"errors"
	"html/template"
	"strings"
	"sync"
	"time"

	"github.com/WedgeNix/util"
	wedgemail "github.com/WedgeNix/wedgeMail"
)

func (v *Vars) emailOrders(bans bananas) (taggableBananas, error) {
	hyBans := bananas{}
	util.Log("removing monitors and hybrids from drop ship emails")

	for vend, set := range v.settings {
		switch {
		case set.Hybrid:
			hyBans[vend] = bans[vend]
			fallthrough
		case set.Monitor || set.Hybrid:
			delete(bans, vend)
		}
	}

	util.Log("parse HTML template")

	tmpl, err := template.ParseFiles("vendor-email-tmpl.html")
	if err != nil {
		return nil, err
	}

	login, err := wedgemail.StartMail()
	if err != nil {
		return nil, err
	}

	var emailing sync.WaitGroup
	start := time.Now()

	mailErrc := make(chan error)

	for V, set := range v.settings {
		if set.Monitor || set.Hybrid {
			continue
		}

		bunch, exists := bans[V]
		if !exists {
			util.Log("send empty email (drop ship)")
		}

		vendor := V // new "variables" for closure

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
				mailErrc <- err
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
					mailErrc <- err
				}
			}

			if sandbox {
				return
			}

			if !paperless && !dontEmailButCreateOrders {
				email := buf.String()
				err := login.Email(to, "WedgeNix PO#: "+po, email, att)
				if err != nil {
					mailErrc <- errors.New("error in emailing " + vendor)
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

	close(mailErrc)

	var errs []string
	for err := range mailErrc { // drop ship email errors
		if err != nil {
			errs = append(errs, err.Error())
		}
	}
	for err := range v.j.mailErr { // wait for monitor emails to finish
		if err != nil {
			errs = append(errs, err.Error())
		}
	}

	if len(errs) > 0 {
		err = errors.New(strings.Join(errs, ", "))
	}
	return taggableBananas(bans), err
}
