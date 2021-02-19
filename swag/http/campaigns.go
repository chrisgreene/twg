package http

import (
	"context"
	"database/sql"
	"github.com/joncalhoun/twg/swag/db"
	"github.com/joncalhoun/twg/swag/urlpath"
	"html/template"
	"net/http"
	"strconv"
	"time"
)

type CampaignHandler struct {
	DB interface {
		ActiveCampaign() (*db.Campaign, error)
		GetCampaign(int) (*db.Campaign, error)
	}
	Logger Logger
	Templates struct {
		Show  *template.Template
		Ended *template.Template
	}
	TimeNow func() time.Time
}

func (ch *CampaignHandler) ShowActive(w http.ResponseWriter, r *http.Request) {
	campaign, err := ch.DB.ActiveCampaign()
	switch {
	case err == sql.ErrNoRows:
		err = ch.Templates.Ended.Execute(w, nil)
		if err != nil {
			ch.Logger.Printf("Error executing campaign ended template. err = %v", err)
		}
		// ch.ShowCampaignEnded(w, r)
		return
	case err != nil:
		ch.Logger.Printf("Error retrieving the active campaign. err = %v", err)
		http.Error(w, "Something went wrong...", http.StatusInternalServerError)
		return
	}

	var leftValue int
	var leftUnit string
	left := campaign.EndsAt.Sub(ch.TimeNow())
	switch {
	case left >= 24*time.Hour:
		leftValue = int(left / (24 * time.Hour))
		leftUnit = "day(s)"
	case left >= time.Hour:
		leftValue = int(left / time.Hour)
		leftUnit = "hour(s)"
	case left >= time.Minute:
		leftValue = int(left / time.Minute)
		leftUnit = "minute(s)"
	default:
		leftValue = int(left / time.Second)
		leftUnit = "second(s)"
	}
	data := struct {
		ID       int
		Price    int
		TimeLeft struct {
			Value int
			Unit  string
		}
	}{}
	data.ID = campaign.ID
	data.Price = campaign.Price / 100
	data.TimeLeft.Value = leftValue
	data.TimeLeft.Unit = leftUnit
	err = ch.Templates.Show.Execute(w, data)
	if err != nil {
		ch.Logger.Printf("Error executing campaign show template. err = %v", err)
	}
}

func (ch *CampaignHandler) CampaignMw(next http.HandlerFunc) http.HandlerFunc {
	// Trim the ID from the path, set the campaign in the ctx, and call
	// the cmpMux.
	return func(w http.ResponseWriter, r *http.Request) {
		idStr, path := urlpath.Split(r.URL.Path)
		id, err := strconv.Atoi(idStr)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		campaign, err := ch.DB.GetCampaign(id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), "campaign", campaign)
		r = r.WithContext(ctx)
		r.URL.Path = path
		next.ServeHTTP(w, r)
	}
}
