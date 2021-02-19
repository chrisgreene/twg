package http_test

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/joncalhoun/twg/stripe"
	"github.com/joncalhoun/twg/swag/db"
	. "github.com/joncalhoun/twg/swag/http"
	"html/template"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestOrderHandler_New(t *testing.T) {
	type checkFn func(*testing.T, *http.Response)
	checks := func(fns ...checkFn) []checkFn {
		return fns
	}
	hasBody := func(want string) func(*testing.T, *http.Response) {
		return func(t *testing.T, got *http.Response) {
			body, err := ioutil.ReadAll(got.Body)
			defer got.Body.Close()
			if err != nil {
				t.Fatalf("ReadAll() err = %v; want nil", err)
			}
			resBody := strings.TrimSpace(string(body))
			if resBody != want {
				t.Fatalf("body = %s; want %s", resBody, want)
			}
		}
	}
	hasStatus := func(code int) func(*testing.T, *http.Response) {
		return func(t *testing.T, got *http.Response) {
			if got.StatusCode != code {
				t.Fatalf("code = %d; want %d", got.StatusCode, code)
			}
		}
	}
	tests := map[string]func(*testing.T) (*OrderHandler, *db.Campaign, []checkFn){
		"campaign id is set": func(t *testing.T) (*OrderHandler, *db.Campaign, []checkFn) {
			oh := OrderHandler{}
			oh.Templates.New = template.Must(template.New("").Parse("{{.Campaign.ID}}"))
			return &oh, &db.Campaign{
				ID: 123,
			}, checks(hasBody("123"))
		},
		"campaign price is set": func(t *testing.T) (*OrderHandler, *db.Campaign, []checkFn) {
			oh := OrderHandler{}
			oh.Templates.New = template.Must(template.New("").Parse("{{.Campaign.Price}}"))
			return &oh, &db.Campaign{
				Price: 1200,
			}, checks(hasBody("12"))
		},
		"campaign is not set": func(t *testing.T) (*OrderHandler, *db.Campaign, []checkFn) {
			oh := OrderHandler{}
			return &oh, nil, checks(hasBody("Campaign not provided"), hasStatus(http.StatusInternalServerError))
		},
		"stripe public key": func(t *testing.T) (*OrderHandler, *db.Campaign, []checkFn) {
			oh := OrderHandler{}
			oh.Stripe.PublicKey = "sk_pub_123abc"
			oh.Templates.New = template.Must(template.New("").Parse("{{.StripePublicKey}}"))
			return &oh, &db.Campaign{}, checks(hasBody(oh.Stripe.PublicKey))
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			oh, campaign, checks := tc(t)
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if campaign != nil {
				r = r.WithContext(context.WithValue(r.Context(), "campaign", campaign))
			}
			oh.New(w, r)
			res := w.Result()
			// resBody, err := ioutil.ReadAll(res.Body)
			// if err != nil {
			// 	t.Fatalf("ReadAll() err = %v; want nil", err)
			// }
			// defer res.Body.Close()
			// got := strings.TrimSpace(string(resBody))
			for _, check := range checks {
				check(t, res)
			}
		})
	}
}

func TestOrderHandler_Create(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		oh := OrderHandler{}
		oh.DB = &mockDB{
			CreateOrderFunc: func(order *db.Order) error {
				order.ID = 123
				return nil
			},
		}
		formData := url.Values{
			"Name":         []string{"Chris Greene"},
			"Email":        []string{"chris@test.com"},
			"stripe-token": []string{"secret-stripe-token"},
		}
		stripeCustomerID := "cus_abc123"
		oh.Stripe.Client = &mockStripe{
			CustomerFunc: func(token, email string) (*stripe.Customer, error) {
				if token != formData.Get("stripe-token") {
					t.Fatalf("token = %s, want %s", token, formData.Get("stripe-token"))
				}
				if email != formData.Get("Email") {
					t.Fatalf("email = %s, want %s", email, formData.Get("Email"))
				}
				return &stripe.Customer{
					ID: stripeCustomerID,
				}, nil
			},
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(formData.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r = r.WithContext(context.WithValue(r.Context(), "campaign", &db.Campaign{
			ID: 333,
		}))
		oh.Create(w, r)
		res := w.Result()
		if res.StatusCode != http.StatusFound {
			t.Fatalf("StatusCode = %d; want %d", res.StatusCode, http.StatusFound)
		}
		locURL, err := res.Location() // Header.Get("Location")
		if err != nil {
			t.Fatalf("Location() err = %v; want %v", err, nil)
		}
		gotLoc := locURL.Path
		wantLoc := fmt.Sprintf("/orders/%s", stripeCustomerID)
		if gotLoc != wantLoc {
			t.Fatalf("Redirect location = %s; want %s", gotLoc, wantLoc)
		}

	})
}

func TestOrderHandler_OrderMw(t *testing.T) {
	failHandler := func(t *testing.T) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("next handler shouldn't have been called by middleware")
		}
	}
	t.Run("missing order", func(t *testing.T) {
		oh := OrderHandler{}
		mdb := &mockDB{
			GetOrderViaPayCusFunc: func(id string) (*db.Order, error) {
				return nil, sql.ErrNoRows
			},
		}
		oh.DB = mdb
		handler := oh.OrderMw(failHandler(t))
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/cus_abc123/id/here", nil)
		handler(w, r)
		res := w.Result()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("StatusCode = %d, want %d", res.StatusCode, http.StatusNotFound)
		}
	})
	t.Run("order found", func(t *testing.T) {
		order := &db.Order{
			ID: 123,
			Payment: db.Payment{
				CustomerID: "cus_abc123",
				Source:     "stripe",
			},
			// StartsAt: time.Now(),
			// EndsAt: time.Now().Add(1 * time.Hour),
			// Price: 1200,
		}
		oh := OrderHandler{}
		mdb := &mockDB{
			GetOrderViaPayCusFunc: func(id string) (*db.Order, error) {
				if id == order.Payment.CustomerID {
					return order, nil
				}
				return nil, sql.ErrNoRows
			},
		}
		handlerCalled := false
		gotPath := ""
		var gotOrder *db.Order
		oh.DB = mdb
		handler := oh.OrderMw(func(w http.ResponseWriter, r *http.Request) {
			handlerCalled = true
			gotPath = r.URL.Path
			gotOrder = r.Context().Value("order").(*db.Order)
		})
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/%s/id/here", order.Payment.CustomerID), nil)
		handler(w, r)
		res := w.Result()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("StatusCode = %d, want %d", res.StatusCode, http.StatusOK)
		}
		if !handlerCalled {
			t.Fatalf("next handler not called")
		}
		if gotPath != "/id/here/" {
			t.Fatalf("Path in next handler = %v; want %v", gotPath, "/id/here/")
		}
		if gotOrder != order {
			t.Fatalf("Campaign = %v; want %v", gotOrder, order)
		}
	})

}

func testOrderHandler_Show_review(t *testing.T, oh *OrderHandler, campaign *db.Campaign, order *db.Order) {
	tests := map[string]struct {
		tpl  *template.Template
		want func(*db.Order, *db.Campaign) string
	}{
		"order id": {
			tpl:  template.Must(template.New("").Parse("{{.Order.ID}}")),
			want: func(order *db.Order, _ *db.Campaign) string { return order.Payment.CustomerID },
		},
		"order address": {
			tpl:  template.Must(template.New("").Parse("{{.Order.Address}}")),
			want: func(order *db.Order, _ *db.Campaign) string { return order.Address.Raw },
		},
		"campaign price": {
			tpl:  template.Must(template.New("").Parse("{{.Campaign.Price}}")),
			want: func(_ *db.Order, campaign *db.Campaign) string { return fmt.Sprintf("%d", campaign.Price/100) },
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			oh.Templates.Review = tc.tpl
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/orders/cus_abc123", nil)
			r = r.WithContext(context.WithValue(r.Context(), "order", order))
			oh.Show(w, r)
			res := w.Result()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("Statuscode = %d; want %d", res.StatusCode, http.StatusOK)
			}
			defer res.Body.Close()
			body, err := ioutil.ReadAll(res.Body)
			if err != nil {
				t.Fatalf("ReadAll() err = %v; want %v", err, nil)
			}
			gotBody := string(body)
			if gotBody != tc.want(order, campaign) {
				t.Fatalf("Body = %s; want %s", gotBody, tc.want(order, campaign))
			}
		})
	}
}

func TestOrderHandler_Show_tableDemo(t *testing.T) {
	tests := map[string]func(*testing.T, *OrderHandler, *db.Campaign, *db.Order){
		"review - campaign found": testOrderHandler_Show_review,
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			oh := &OrderHandler{}
			campaign := &db.Campaign{
				ID:    999,
				Price: 1000,
			}
			order := &db.Order{
				ID:         123,
				CampaignID: campaign.ID,
				Address: db.Address{
					Raw: `Chris Greene
PO BOX 295
BEDFORD PA 15522
UNITED STATES`,
				},
				Payment: db.Payment{
					CustomerID: "cus_abc123",
					Source:     "stripe",
				},
			}
			mdb := &mockDB{
				GetCampaignFunc: func(id int) (*db.Campaign, error) {
					if id == campaign.ID {
						return campaign, nil
					}
					return nil, sql.ErrNoRows
				},
			}
			oh.DB = mdb
			oh.Logger = &logRecorder{}
			tc(t, oh, campaign, order)
		})
	}

}

func TestOrderHandler_show(t *testing.T) {
	t.Run("review - campaign found", func(t *testing.T) {
		tests := map[string]struct {
			tpl  *template.Template
			want func(*db.Order, *db.Campaign) string
		}{
			"order id": {
				tpl:  template.Must(template.New("").Parse("{{.Order.ID}}")),
				want: func(order *db.Order, _ *db.Campaign) string { return order.Payment.CustomerID },
			},
			"order address": {
				tpl:  template.Must(template.New("").Parse("{{.Order.Address}}")),
				want: func(order *db.Order, _ *db.Campaign) string { return order.Address.Raw },
			},
			"campaign price": {
				tpl:  template.Must(template.New("").Parse("{{.Campaign.Price}}")),
				want: func(_ *db.Order, campaign *db.Campaign) string { return fmt.Sprintf("%d", campaign.Price/100) },
			},
		}
		for name, tc := range tests {
			t.Run(name, func(t *testing.T) {
				oh := OrderHandler{}
				campaign := &db.Campaign{
					ID:    999,
					Price: 1000,
				}
				order := &db.Order{
					ID:         123,
					CampaignID: campaign.ID,
					Address: db.Address{
						Raw: `Chris Greene
PO BOX 295
BEDFORD PA 15522
UNITED STATES`,
					},
					Payment: db.Payment{
						CustomerID: "cus_abc123",
						Source:     "stripe",
					},
				}
				mdb := &mockDB{
					GetCampaignFunc: func(id int) (*db.Campaign, error) {
						if id == campaign.ID {
							return campaign, nil
						}
						return nil, sql.ErrNoRows
					},
				}
				oh.DB = mdb
				oh.Templates.Review = tc.tpl
				w := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodGet, "/orders/cus_abc123", nil)
				r = r.WithContext(context.WithValue(r.Context(), "order", order))
				oh.Show(w, r)
				res := w.Result()
				if res.StatusCode != http.StatusOK {
					t.Fatalf("Statuscode = %d; want %d", res.StatusCode, http.StatusOK)
				}
				defer res.Body.Close()
				body, err := ioutil.ReadAll(res.Body)
				if err != nil {
					t.Fatalf("ReadAll() err = %v; want %v", err, nil)
				}
				gotBody := string(body)
				if gotBody != tc.want(order, campaign) {
					t.Fatalf("Body = %s; want %s", gotBody, tc.want(order, campaign))
				}
			})
		}
	})

	t.Run("review - db error", func(t *testing.T) {
		oh := OrderHandler{}
		order := &db.Order{
			ID:         123,
			CampaignID: 999,
		}
		lr := &logRecorder{}
		oh.Logger = lr
		mdb := &mockDB{
			GetCampaignFunc: func(id int) (*db.Campaign, error) {
				return nil, sql.ErrNoRows
			},
		}
		oh.DB = mdb
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/orders/cus_abc123", nil)
		r = r.WithContext(context.WithValue(r.Context(), "order", order))
		oh.Show(w, r)
		res := w.Result()
		if res.StatusCode != http.StatusInternalServerError {
			t.Fatalf("Statuscode = %d; want %d", res.StatusCode, http.StatusInternalServerError)
		}
		defer res.Body.Close()
		body, err := ioutil.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("ReadAll() err = %v; want %v", err, nil)
		}
		gotBody := strings.TrimSpace(string(body))
		wantBody := "Something went wrong..."
		if gotBody != wantBody {
			t.Fatalf("Body = %s; want %s", gotBody, wantBody)
		}
	})

	t.Run("charged", func(t *testing.T) {
		tests := map[string]struct {
			stripeChg *stripe.Charge
			stripeErr error
			wantCode  int
			wantBody  string
		}{
			"succeeded": {
				stripeChg: &stripe.Charge{
					Status: "succeeded",
				},
				stripeErr: nil,
				wantCode:  http.StatusOK,
				wantBody:  "Your order has been completed successfully! You will be contacted when it ships.",
			},
			"pending": {
				stripeChg: &stripe.Charge{
					Status: "pending",
				},
				stripeErr: nil,
				wantCode:  http.StatusOK,
				wantBody:  "Your payment is still pending.",
			},
			"failed": {
				stripeChg: &stripe.Charge{
					Status: "failed",
				},
				stripeErr: nil,
				wantCode:  http.StatusOK,
				wantBody:  "Your payment failed. :( Please create a new order with a new card if you want to try again.",
			},
			"error getting charge": {
				stripeChg: nil,
				stripeErr: &stripe.Error{},
				wantCode:  http.StatusOK,
				wantBody:  "Failed to lookup the status of your order. Please try again, or contact me if this persists - jon@calhoun.io",
			},
		}
		for name, tc := range tests {
			t.Run(name, func(t *testing.T) {
				oh := OrderHandler{}
				oh.Logger = &logRecorder{}
				campaign := &db.Campaign{
					ID:    999,
					Price: 1000,
				}
				order := &db.Order{
					ID:         123,
					CampaignID: campaign.ID,
					Address: db.Address{
						Raw: `Chris Greene
PO BOX 295
BEDFORD PA 15522
UNITED STATES`,
					},
					Payment: db.Payment{
						ChargeID:   "chg_xyz890",
						CustomerID: "cus_abc123",
						Source:     "stripe",
					},
				}
				mdb := &mockDB{
					GetCampaignFunc: func(id int) (*db.Campaign, error) {
						if id == campaign.ID {
							return campaign, nil
						}
						return nil, sql.ErrNoRows
					},
				}
				oh.DB = mdb
				oh.Stripe.Client = &mockStripe{
					GetChargeFunc: func(id string) (*stripe.Charge, error) {
						return tc.stripeChg, tc.stripeErr
					},
				}
				w := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodGet, "/orders/cus_abc123", nil)
				r = r.WithContext(context.WithValue(r.Context(), "order", order))
				oh.Show(w, r)
				res := w.Result()
				if res.StatusCode != tc.wantCode {
					t.Fatalf("Statuscode = %d; want %d", res.StatusCode, http.StatusOK)
				}
				defer res.Body.Close()
				body, err := ioutil.ReadAll(res.Body)
				if err != nil {
					t.Fatalf("ReadAll() err = %v; want %v", err, nil)
				}
				gotBody := strings.TrimSpace(string(body))
				if !strings.Contains(gotBody, tc.wantBody) {
					t.Fatalf("Body = %s; want %s", gotBody, tc.wantBody)
				}
			})
		}
	})
}

func TestOrderHandler_Confirm(t *testing.T) {
	type checkFn func(*testing.T, *http.Response)
	hasStatus := func(code int) checkFn {
		return func(t *testing.T, res *http.Response) {
			if res.StatusCode != code {
				t.Fatalf("StatusCode = %d; want %d", res.StatusCode, code)
			}
		}
	}
	hasBody := func(want string) checkFn {
		return func(t *testing.T, res *http.Response) {
			defer res.Body.Close()
			bodyBytes, err := ioutil.ReadAll(res.Body)
			if err != nil {
				t.Fatalf("ReadAll() err = %v; want %v", err, nil)
			}
			got := strings.TrimSpace(string(bodyBytes))
			if got != want {
				t.Fatalf("Body = %v; want %v", got, want)
			}
		}
	}
	hasLogs := func(lr *logRecorder, want ...string) checkFn {
		return func(t *testing.T, res *http.Response) {
			if len(lr.logs) != len(want) {
				t.Fatalf("len(Logs) = %v; want %v", len(lr.logs), len(want))
			}
			for i, log := range lr.logs {
				if log != want[i] {
					t.Fatalf("log[%d] = %v; want %v", i, log, want[i])
				}
			}
		}
	}
	hasLocation := func(want string) checkFn {
		return func(t *testing.T, res *http.Response) {
			locURL, err := res.Location() // Header.Get("Location")
			if err != nil {
				t.Fatalf("Location() err = %v; want %v", err, nil)
			}
			gotLoc := locURL.Path
			if gotLoc != want {
				t.Fatalf("Redirect location = %s; want %s", gotLoc, want)
			}
		}
	}
	testOrder := func(campaignID int) *db.Order {
		return &db.Order{
			ID:         123,
			CampaignID: campaignID,
			Address: db.Address{
				Raw: `Chris Greene
PO BOX 295
BEDFORD PA 15522
UNITED STATES`,
			},
			Payment: db.Payment{
				CustomerID: "cus_abc123",
				Source:     "stripe",
			},
		}
	}

	runTests := func(t *testing.T, oh *OrderHandler, formData *url.Values, order *db.Order, checks ...checkFn) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/orders/cus_abc123", strings.NewReader(formData.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r = r.WithContext(context.WithValue(r.Context(), "order", order))
		oh.Confirm(w, r)
		res := w.Result()
		for _, check := range checks {
			check(t, res)
		}
	}

	t.Run("error getting campaign", func(t *testing.T) {
		oh := OrderHandler{}
		lr := &logRecorder{}
		oh.Logger = lr
		campaign := &db.Campaign{
			ID:    999,
			Price: 1000,
		}
		order := testOrder(campaign.ID)
		mdb := &mockDB{
			GetCampaignFunc: func(id int) (*db.Campaign, error) {
				return nil, sql.ErrNoRows
			},
		}
		oh.DB = mdb
		formData := &url.Values{
			"address-raw": []string{order.Address.Raw},
		}
		runTests(t, &oh, formData, order,
			hasStatus(http.StatusInternalServerError),
			hasBody("Something went wrong..."),
			hasLogs(lr, "error retrieving order campaign\n"),
		)
	})

	t.Run("stripe error creating charge", func(t *testing.T) {
		oh := OrderHandler{}
		campaign := &db.Campaign{
			ID:    999,
			Price: 1000,
		}
		order := testOrder(campaign.ID)
		mdb := &mockDB{
			GetCampaignFunc: func(id int) (*db.Campaign, error) {
				if id == campaign.ID {
					return campaign, nil
				}
				return nil, sql.ErrNoRows
			},
		}
		oh.DB = mdb
		formData := &url.Values{
			"address-raw": []string{order.Address.Raw},
		}
		oh.Stripe.Client = &mockStripe{
			ChargeFunc: func(customerID string, amount int) (*stripe.Charge, error) {
				return nil, stripe.Error{
					Message: "Failed to charge your card!",
				}
			},
		}
		runTests(t, &oh, formData, order,
			hasStatus(http.StatusOK),
			hasBody("Failed to charge your card!"),
		)
	})

	t.Run("non-stripe error when creating charge", func(t *testing.T) {
		oh := OrderHandler{}
		campaign := &db.Campaign{
			ID:    999,
			Price: 1000,
		}
		order := testOrder(campaign.ID)
		mdb := &mockDB{
			GetCampaignFunc: func(id int) (*db.Campaign, error) {
				if id == campaign.ID {
					return campaign, nil
				}
				return nil, sql.ErrNoRows
			},
		}
		oh.DB = mdb
		formData := &url.Values{
			"address-raw": []string{order.Address.Raw},
		}
		oh.Stripe.Client = &mockStripe{
			ChargeFunc: func(customerID string, amount int) (*stripe.Charge, error) {
				return nil, fmt.Errorf("not a stripe error")
			},
		}
		runTests(t, &oh, formData, order,
			hasStatus(http.StatusInternalServerError),
			hasBody("Something went wrong processing your card. Please contact me for support - jon@calhoun.io"),
		)
	})

	t.Run("error getting campaign", func(t *testing.T) {
		oh := OrderHandler{}
		lr := &logRecorder{}
		oh.Logger = lr
		campaign := &db.Campaign{
			ID:    999,
			Price: 1000,
		}
		order := testOrder(campaign.ID)
		mdb := &mockDB{
			GetCampaignFunc: func(id int) (*db.Campaign, error) {
				return nil, sql.ErrNoRows
			},
		}
		oh.DB = mdb
		formData := &url.Values{
			"address-raw": []string{order.Address.Raw},
		}
		runTests(t, &oh, formData, order,
			hasStatus(http.StatusInternalServerError),
			hasBody("Something went wrong..."),
			hasLogs(lr, "error retrieving order campaign\n"),
		)
	})

	t.Run("error connecting to database", func(t *testing.T) {
		paymentChargeID := "chg_123456"
		oh := OrderHandler{}
		campaign := &db.Campaign{
			ID:    999,
			Price: 1000,
		}
		order := testOrder(campaign.ID)
		mdb := &mockDB{
			GetCampaignFunc: func(id int) (*db.Campaign, error) {
				if id == campaign.ID {
					return campaign, nil
				}
				return nil, sql.ErrNoRows
			},
			ConfirmOrderFunc: func(orderID int, addressRaw, chargeID string) error {
				return sql.ErrConnDone
			},
		}
		oh.DB = mdb
		formData := &url.Values{
			"address-raw": []string{order.Address.Raw},
		}
		oh.Stripe.Client = &mockStripe{
			ChargeFunc: func(customerID string, amount int) (*stripe.Charge, error) {
				if customerID == order.Payment.CustomerID {
					return &stripe.Charge{
						ID: paymentChargeID,
					}, nil
				}
				return nil, stripe.Error{}
			},
		}
		runTests(t, &oh, formData, order,
			hasStatus(http.StatusInternalServerError),
			hasBody("You were charged, but something went wrong saving your data. Please contact me for support"+
				" - jon@calhoun.io"),
		)
	})

	t.Run("same address", func(t *testing.T) {
		paymentChargeID := "chg_123456"
		oh := OrderHandler{}
		campaign := &db.Campaign{
			ID:    999,
			Price: 1000,
		}
		order := testOrder(campaign.ID)
		wantAddress := order.Address.Raw
		mdb := &mockDB{
			GetCampaignFunc: func(id int) (*db.Campaign, error) {
				if id == campaign.ID {
					return campaign, nil
				}
				return nil, sql.ErrNoRows
			},
			ConfirmOrderFunc: func(orderID int, addressRaw, chargeID string) error {
				if orderID != order.ID {
					return fmt.Errorf("ConfirmOrder() ID : %d; want %d", orderID, order.ID)
				}
				if addressRaw != wantAddress {
					return fmt.Errorf("ConfirmOrder() addressRaw : %q; want %q", addressRaw, order.Address.Raw)
				}
				if paymentChargeID != chargeID {
					return fmt.Errorf("ConfirmOrder() paymentChargeID : %q; want %q", chargeID, paymentChargeID)
				}
				return nil
			},
		}
		oh.DB = mdb
		formData := &url.Values{
			"address-raw": []string{order.Address.Raw},
		}
		oh.Stripe.Client = &mockStripe{
			ChargeFunc: func(customerID string, amount int) (*stripe.Charge, error) {
				if customerID == order.Payment.CustomerID {
					return &stripe.Charge{
						ID: paymentChargeID,
					}, nil
				}
				return nil, stripe.Error{}
			},
		}
		runTests(t, &oh, formData, order,
			hasStatus(http.StatusFound),
			hasLocation(fmt.Sprintf("/orders/%s", order.Payment.CustomerID)),
		)
	})

	t.Run("different address", func(t *testing.T) {
		paymentChargeID := "chg_123456"
		newAddress := `NEW ADDRESS HERE`
		oh := OrderHandler{}
		campaign := &db.Campaign{
			ID:    999,
			Price: 1000,
		}
		order := testOrder(campaign.ID)
		mdb := &mockDB{
			GetCampaignFunc: func(id int) (*db.Campaign, error) {
				if id == campaign.ID {
					return campaign, nil
				}
				return nil, sql.ErrNoRows
			},
			ConfirmOrderFunc: func(orderID int, gotAddress, chargeID string) error {
				if gotAddress != newAddress {
					return fmt.Errorf("ConfirmOrder() addressRaw : %q; want %q", gotAddress, order.Address.Raw)
				}
				return nil
			},
		}
		oh.DB = mdb
		formData := &url.Values{
			"address-raw": []string{newAddress},
		}
		oh.Stripe.Client = &mockStripe{
			ChargeFunc: func(customerID string, amount int) (*stripe.Charge, error) {
				if customerID == order.Payment.CustomerID {
					return &stripe.Charge{
						ID: paymentChargeID,
					}, nil
				}
				return nil, stripe.Error{}
			},
		}
		runTests(t, &oh, formData, order,
			hasStatus(http.StatusFound),
			hasLocation(fmt.Sprintf("/orders/%s", order.Payment.CustomerID)),
		)
	})
}
