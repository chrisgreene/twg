//+build int

package http_test

import (
	"context"
	"database/sql"
	"flag"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/joncalhoun/twg/stripe"
	"github.com/joncalhoun/twg/swag/db"
	. "github.com/joncalhoun/twg/swag/http"
)

var (
	stripeSecretKey = ""
)

func init() {
	flag.StringVar(&stripeSecretKey, "stripe", "", "stripe secret key for integration testing")
}

func TestOrderHandler_Create_stripeInt(t *testing.T) {
	flag.Parse()
	if stripeSecretKey == "" {
		t.Skip("stripe secret key not provided")
	}
	type checkFn func(*testing.T, *http.Response)
	hasCode := func(want int) checkFn {
		return func(t *testing.T, res *http.Response) {
			if res.StatusCode != want {
				t.Fatalf("StatusCode = %d; want %d", res.StatusCode, want)
			}
		}
	}
	// bodyContains := func(want string) checkFn {
	// 	return func(t *testing.T, res *http.Response) {
	// 		return
	// 		defer res.Body.Close()
	// 		body, err := ioutil.ReadAll(res.Body)
	// 		if err != nil {
	// 			t.Fatalf("ReadAll() err = %v; want %v", err, nil)
	// 		}
	// 		gotBody := strings.TrimSpace(string(body))
	// 		if !strings.Contains(gotBody, want) {
	// 			t.Fatalf("Body = %v; want substring %v", gotBody, want)
	// 		}
	// 	}
	// }
	hasLocationPrefix := func(want string) checkFn {
		return func(t *testing.T, res *http.Response) {
			locURL, err := res.Location()
			if err != nil {
				t.Fatalf("Location() err = %v; want %v", err, nil)
			}
			gotLoc := locURL.Path
			if !strings.HasPrefix(gotLoc, want) {
				t.Fatalf("Redirect location = %s; want prefix %s", gotLoc, want)
			}
		}
	}
	hasCustomerID := func(customerID *string) checkFn {
		return func(t *testing.T, res *http.Response) {
			locURL, err := res.Location()
			if err != nil {
				t.Fatalf("Location() err = %v; want %v", err, nil)
			}
			gotLoc := locURL.Path
			gotStripeCusID := gotLoc[len("/orders/"):]
			stripeCustomerID := *customerID
			if gotStripeCusID != stripeCustomerID {
				t.Fatalf("Stripe Customer ID = %s; want %s", gotStripeCusID, stripeCustomerID)
			}
		}
	}
	// hasLogs := func(logger *logRecorder, logs ...string) checkFn {
	// 	return func(t *testing.T, _ *http.Response) {
	// 		if len(logger.logs) != len(logs) {
	// 			t.Fatalf("len(logs) = %d; want %d", len(logger.logs), len(logs))
	// 		}
	// 		for i, log := range logs {
	// 			if !strings.HasPrefix(logger.logs[i], log) {
	// 				t.Fatalf("log[%d] = %s; want prefix %s", i, logger.logs[i], log)
	// 			}
	// 		}
	// 	}
	// }
	stripeClientAndIDCapture := func(stripeClient interface {
		Customer(email, token string) (*stripe.Customer, error)
	}) (*mockStripe, *string) {
		stripeCustomerID := ""
		return &mockStripe{
			CustomerFunc: func(email, token string) (*stripe.Customer, error) {
				cus, err := stripeClient.Customer(email, token)
				if cus != nil {
					stripeCustomerID = cus.ID
				}
				return cus, err
			},
		}, &stripeCustomerID
	}

	tests := map[string]func(*testing.T, *OrderHandler) (string, []checkFn){
		"visa": func(t *testing.T, oh *OrderHandler) (string, []checkFn) {
			stripeClient, stripeCustomerID := stripeClientAndIDCapture(oh.Stripe.Client)
			oh.Stripe.Client = stripeClient
			oh.Logger = &logRecorderFail{t}

			return "tok_visa", []checkFn{
				hasCode(http.StatusFound),
				hasLocationPrefix("/orders/"),
				hasCustomerID(stripeCustomerID),
			}
		},
		"cvc check failure": func(t *testing.T, oh *OrderHandler) (string, []checkFn) {
			lr := &logRecorder{}
			oh.Logger = lr

			return "tok_cvcCheckFail", []checkFn{
				hasCode(http.StatusFound),
				// bodyContains("Something went wrong processing your payment information."),
				// hasLogs(lr, "Error creating stripe customer."),
			}
		},
		"amex": func(t *testing.T, oh *OrderHandler) (string, []checkFn) {
			stripeClient, stripeCustomerID := stripeClientAndIDCapture(oh.Stripe.Client)
			oh.Stripe.Client = stripeClient
			oh.Logger = &logRecorderFail{t}

			return "tok_amex", []checkFn{
				hasCode(http.StatusFound),
				hasLocationPrefix("/orders/"),
				hasCustomerID(stripeCustomerID),
			}
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			oh := OrderHandler{}
			oh.DB = &mockDB{
				CreateOrderFunc: func(order *db.Order) error {
					order.ID = 123
					return nil
				},
			}
			oh.Stripe.Client = &stripe.Client{
				Key: stripeSecretKey,
			}
			oh.Logger = &logRecorder{}

			token, checks := tc(t, &oh)

			formData := url.Values{
				"Name":         []string{"Jon Calhoun"},
				"Email":        []string{"jon@calhoun.io"},
				"stripe-token": []string{token},
			}
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(formData.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r = r.WithContext(context.WithValue(r.Context(), "campaign", &db.Campaign{
				ID: 333,
			}))
			oh.Create(w, r)
			res := w.Result()
			for _, check := range checks {
				check(t, res)
			}
		})
	}
}

func TestOrderHandler_Show_stripeInt(t *testing.T) {
	if stripeSecretKey == "" {
		t.Skip("stripe secret key not provided")
	}
	t.Run("charged", func(t *testing.T) {
		price := 1000
		tests := map[string]struct {
			chgID func(*testing.T, *stripe.Client) string
			wantCode  int
			wantBody  string
		}{
			"succeeded": {
				chgID: func(t *testing.T, sc *stripe.Client) string{
					cus, err := sc.Customer("tok_visa", "success@gopherswag.com")
					if err != nil {
						t.Fatalf("Customer() err = %v; want %v", err, nil)
					}
					chg, err := sc.Charge(cus.ID, price)
					if err != nil {
						t.Fatalf("Charge() err = %v; want %v", err, nil)
					}
					return chg.ID
				},
				wantCode:  http.StatusOK,
				wantBody:  "Your order has been completed successfully! You will be contacted when it ships.",
			},
			"error getting charge": {
				chgID: func(t *testing.T, sc *stripe.Client) string{
					return "chg_fake_id"
				},
				wantCode:  http.StatusOK,
				wantBody:  "Failed to lookup the status of your order. Please try again, or contact me if this persists - jon@calhoun.io",
			},
		}
		for name, tc := range tests {
			t.Run(name, func(t *testing.T) {
				oh := OrderHandler{}
				sc := &stripe.Client{
					Key: stripeSecretKey,
				}
				oh.Stripe.Client = sc
				oh.Logger = &logRecorder{}
				campaign := &db.Campaign{
					ID:    999,
					Price: price,
				}
				chgID := tc.chgID(t, sc)
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
						ChargeID:   chgID,
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
