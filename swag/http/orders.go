package http

import (
	"context"
	"fmt"
	"github.com/gorilla/schema"
	"github.com/joncalhoun/twg/stripe"
	"github.com/joncalhoun/twg/swag/db"
	"github.com/joncalhoun/twg/swag/urlpath"
	"html/template"
	"net/http"
	"strings"
)

type orderForm struct {
	Customer struct {
		Name  string `form:"placeholder=Jane Doe"`
		Email string `form:"type=email;placeholder=jane@doe.com;label=Email Address"`
	}
	Address struct {
		Street1 string `form:"placeholder=123 Sticker St;label=Street 1"`
		Street2 string `form:"placeholder=Apt 45;label=Street 2"`
		City    string `form:"placeholder=San Francisco"`
		State   string `form:"label=State (or Province);placeholder=CA"`
		Zip     string `form:"label=Postal Code;placeholder=94139"`
		Country string `form:"placeholder=United States"`
	}
}

type OrderHandler struct {
	DB interface {
		CreateOrder(*db.Order) error
		GetOrderViaPayCus(string) (*db.Order, error)
		GetCampaign(id int) (*db.Campaign, error)
		ConfirmOrder(int, string, string) error
	}
	Stripe struct {
		PublicKey string
		Client    interface {
			Customer(token, email string) (*stripe.Customer, error)
			GetCharge(chargeID string) (*stripe.Charge, error)
			Charge(customerID string, amount int) (*stripe.Charge, error)
		}
	}
	Templates struct {
		New    *template.Template
		Review *template.Template
	}
	Logger Logger
}

func (oh *OrderHandler) New(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	campaign, ok := r.Context().Value("campaign").(*db.Campaign)
	if !ok {
		http.Error(w, "Campaign not provided", http.StatusInternalServerError)
		return
	}

	data := struct {
		Campaign struct {
			ID    int
			Price int
		}
		OrderForm       orderForm
		StripePublicKey string
	}{}
	data.Campaign.ID = campaign.ID
	data.Campaign.Price = campaign.Price / 100
	data.StripePublicKey = oh.Stripe.PublicKey
	err := oh.Templates.New.Execute(w, data)
	if err != nil {
		oh.Logger.Printf("Error executing the new_order template. err = %v", err)
	}
}

func (oh *OrderHandler) Create(w http.ResponseWriter, r *http.Request) {
	campaign := r.Context().Value("campaign").(*db.Campaign)
	formData := struct {
		Name    string
		Email   string
		Street1 string
		Street2 string
		City    string
		State   string
		Zip     string
		Country string
	}{}
	r.ParseForm()
	schema.NewDecoder().Decode(&formData, r.PostForm)
	if formData.Email == "" {
		panic("email wasn't parsed!")
	}
	cus, err := oh.Stripe.Client.Customer(r.PostForm.Get("stripe-token"), formData.Email)
	if err != nil {
		oh.Logger.Printf("Error creating stripe customer. email = %s, err = %v", formData.Email, err)
		http.Error(w, "Something went wrong processing your payment information. Try again, or contact me - jon@calhoun.io - if the problem persists.", http.StatusInternalServerError)
		return
	}
	var order db.Order
	order.CampaignID = campaign.ID
	// Customer
	order.Customer.Name = formData.Name
	order.Customer.Email = formData.Email
	// Address
	order.Address.Street1 = formData.Street1
	order.Address.Street2 = formData.Street2
	order.Address.City = formData.City
	order.Address.State = formData.State
	order.Address.Zip = formData.Zip
	order.Address.Country = formData.Country
	order.Address.Raw = fmt.Sprintf(`%s
%s
%s
%s %s  %s
%s`, order.Customer.Name,
		order.Address.Street1,
		order.Address.Street2,
		order.Address.City, order.Address.State, order.Address.Zip,
		order.Address.Country)
	order.Address.Raw = strings.Replace(order.Address.Raw, "\n\n", "\n", 1)
	order.Address.Raw = strings.ToUpper(order.Address.Raw)

	// Payment info
	order.Payment.Source = "stripe"
	order.Payment.CustomerID = cus.ID
	err = oh.DB.CreateOrder(&order)
	if err != nil {
		http.Error(w, "Something went wrong...", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/orders/%s", order.Payment.CustomerID), http.StatusFound)
}

// func GetOrderViaPayCus(payCustomerID string) (*Order, error) {
// 	return DefaultDatabase.GetOrderViaPayCus(payCustomerID)
// }

func (oh *OrderHandler) OrderMw(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payCusID, path := urlpath.Split(r.URL.Path)
		order, err := oh.DB.GetOrderViaPayCus(payCusID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), "order", order)
		r = r.WithContext(ctx)
		r.URL.Path = path
		next(w, r)
	}
}

func (oh *OrderHandler) Show(w http.ResponseWriter, r *http.Request) {
	order := r.Context().Value("order").(*db.Order)
	campaign, err := oh.DB.GetCampaign(order.CampaignID)
	if err != nil {
		oh.Logger.Printf("error retrieving order campaign\n")
		http.Error(w, "Something went wrong...", http.StatusInternalServerError)
		return
	}
	if order.Payment.ChargeID != "" {
		chg, err := oh.Stripe.Client.GetCharge(order.Payment.ChargeID)
		if err != nil {
			oh.Logger.Printf("error looking up a customer's charge where chg.ID = %s; err = %v", order.Payment.ChargeID, err)
			fmt.Fprintln(w, "Failed to lookup the status of your order. Please try again, or contact me if this persists - jon@calhoun.io")
			return
		}
		switch chg.Status {
		case "succeeded":
			fmt.Fprintln(w, "Your order has been completed successfully! You will be contacted when it ships.")
		case "pending":
			fmt.Fprintln(w, "Your payment is still pending.")
		case "failed":
			fmt.Fprintln(w, "Your payment failed. :( Please create a new order with a new card if you want to try again.")
		}
		return
	}
	data := struct {
		Order struct {
			ID      string
			Address string
		}
		Campaign struct {
			Price int
		}
	}{}
	data.Order.ID = order.Payment.CustomerID
	data.Order.Address = order.Address.Raw
	data.Campaign.Price = campaign.Price / 100
	oh.Templates.Review.Execute(w, data)
}

func (oh *OrderHandler) Confirm(w http.ResponseWriter, r *http.Request) {
	order := r.Context().Value("order").(*db.Order)
	campaign, err := oh.DB.GetCampaign(order.CampaignID)
	if err != nil {
		oh.Logger.Printf("error retrieving order campaign\n")
		http.Error(w, "Something went wrong...", http.StatusInternalServerError)
		return
	}
	r.ParseForm()
	order.Address.Raw = r.PostFormValue("address-raw")
	chg, err := oh.Stripe.Client.Charge(order.Payment.CustomerID, campaign.Price)
	if err != nil {
		if se, ok := err.(stripe.Error); ok {
			fmt.Fprint(w, se.Message)
			return
		}
		http.Error(w, "Something went wrong processing your card. Please contact me for support - jon@calhoun.io",
			http.StatusInternalServerError)
		return
	}
	order.Payment.ChargeID = chg.ID
	// statement := `UPDATE orders
	// SET adr_raw = $2, pay_charge_id = $3
	// WHERE id = $1`
	err = oh.DB.ConfirmOrder(order.ID, order.Address.Raw, order.Payment.ChargeID)
	if err != nil {
		http.Error(w, "You were charged, but something went wrong saving your data. Please contact me for support - jon@calhoun.io",
			http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/orders/%s", order.Payment.CustomerID), http.StatusFound)
}
