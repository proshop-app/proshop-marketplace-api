package http

import (
	"errors"
	"net/http"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type trackingJSON struct {
	Carrier string  `json:"carrier"`
	Number  *string `json:"number"`
	URL     string  `json:"url"`
}

type checkoutLineJSON struct {
	OfferID         string `json:"offerId"`
	ProductID       string `json:"productId"`
	Name            string `json:"name"`
	Quantity        int    `json:"quantity"`
	Price           rubles `json:"price"`
	Amount          rubles `json:"amount"`
	BaseAmount      rubles `json:"baseAmount"`
	Discount        rubles `json:"discount"`
	DiscountPercent int    `json:"discountPercent"`
}

type quoteJSON struct {
	Status    string    `json:"status"`
	Reason    string    `json:"reason,omitempty"`
	BuyerCost rubles    `json:"buyerCost"`
	MinDays   int       `json:"minDays"`
	MaxDays   int       `json:"maxDays"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type checkoutGroupJSON struct {
	PaymentHold               string               `json:"paymentHold,omitempty"`
	Regions                   []string             `json:"regions"`
	Carriers                  []string             `json:"carriers"`
	CreationStatus            string               `json:"creationStatus,omitempty"`
	ReconciliationRequired    bool                 `json:"reconciliationRequired"`
	CreationDeadline          *time.Time           `json:"creationDeadline,omitempty"`
	Seller                    *participant.Seller  `json:"seller"`
	Tracking                  *trackingJSON        `json:"tracking"`
	TenantID                  string               `json:"tenantId"`
	ExternalCheckoutID        string               `json:"externalCheckoutId"`
	Lines                     []checkoutLineJSON   `json:"lines"`
	BaseTotal                 rubles               `json:"baseTotal"`
	Discount                  rubles               `json:"discount"`
	Total                     rubles               `json:"total"`
	Goods                     rubles               `json:"goods"`
	Quote                     *quoteJSON           `json:"quote"`
	Status                    checkout.GroupStatus `json:"status"`
	OrderID                   string               `json:"orderId,omitempty"`
	ExpiresAt                 *time.Time           `json:"expiresAt,omitempty"`
	DeliveryPrice             rubles               `json:"deliveryPrice"`
	Reason                    string               `json:"reason,omitempty"`
	CurrentPrices             []priceChangeJSON    `json:"currentPrices,omitempty"`
	ReturnFailed              bool                 `json:"returnFailed"`
	ReturnReason              string               `json:"returnReason,omitempty"`
	ReturnRemaining           []returnLineJSON     `json:"returnRemaining,omitempty"`
	PollFailures              int                  `json:"pollFailures"`
	NextPollAt                time.Time            `json:"nextPollAt"`
	CancellationAttempts      int                  `json:"cancellationAttempts"`
	CancellationLastErrorCode string               `json:"cancellationLastErrorCode,omitempty"`
	LastErrorCode             string               `json:"lastErrorCode,omitempty"`
	CreateFailures            int                  `json:"createFailures"`
	CreateExhausted           bool                 `json:"createExhausted"`
}

type priceChangeJSON struct {
	ProductID ids.ProductID `json:"productId"`
	Price     rubles        `json:"price"`
}

type selectionJSON struct {
	RegionCode      string `json:"regionCode"`
	Carrier         string `json:"carrier"`
	PickupPointCode string `json:"pickupPointCode"`
	City            string `json:"city"`
	Address         string `json:"address"`
}

// checkoutJSON — покупка; суммы — рубли из согласованного снимка.
type checkoutJSON struct {
	Carriers             []string            `json:"carriers"`
	ConfirmationRequired bool                `json:"confirmationRequired"`
	PaymentStarted       bool                `json:"paymentStarted"`
	CreationComplete     bool                `json:"creationComplete"`
	ID                   string              `json:"id"`
	Status               string              `json:"status"`
	Groups               []checkoutGroupJSON `json:"groups"`
	Delivery             *selectionJSON      `json:"delivery"`
	BaseTotal            rubles              `json:"baseTotal"`
	Discount             rubles              `json:"discount"`
	PaidTotal            rubles              `json:"paidTotal"`
	GoodsTotal           rubles              `json:"goodsTotal"`
	DeliveryTotal        rubles              `json:"deliveryTotal"`
	Total                rubles              `json:"total"`
	PayableTotal         rubles              `json:"payableTotal"`
	Version              int64               `json:"version"`
	CreatedAt            time.Time           `json:"createdAt"`
	UpdatedAt            time.Time           `json:"updatedAt"`
	ConfirmedAt          *time.Time          `json:"confirmedAt"`
}

type checkoutHandlers struct {
	checkouts *appcheckout.Service
}

type quoteRequestJSON struct {
	City            string `json:"city"`
	Carrier         string `json:"carrier"`
	PickupPointCode string `json:"pickupPointCode"`
}

type returnLineJSON struct {
	OfferID  string `json:"offerId"`
	Quantity int    `json:"quantity"`
}

func toCheckoutJSON(state checkout.State) (checkoutJSON, error) {
	payable, err := appcheckout.PaymentAmount(state)
	if err != nil &&
		!errors.Is(err, checkout.ErrTransition) &&
		!errors.Is(err, checkout.ErrPaymentPaused) &&
		!errors.Is(err, checkout.ErrPaymentExpired) && !errors.Is(err, checkout.ErrDeliveryChanged) {
		return checkoutJSON{}, err
	}

	purchase := checkout.Restore(state)
	out := checkoutJSON{
		Carriers:             supportedCarrierCodes(),
		PaymentStarted:       state.PaymentStarted,
		ConfirmationRequired: purchase.NeedsConfirmation(),
		ID:                   string(state.ID),
		CreationComplete:     purchase.CreationComplete(),
		Status:               string(state.Status),
		Groups:               make([]checkoutGroupJSON, 0, len(state.Groups)),
		GoodsTotal:           rubles(state.GoodsTotal.Kopecks()),
		DeliveryTotal:        rubles(state.DeliveryTotal.Kopecks()),
		Total:                rubles(state.Total.Kopecks()),
		PayableTotal:         rubles(payable.Kopecks()),
		Version:              state.Version,
		CreatedAt:            state.CreatedAt.UTC(),
		UpdatedAt:            state.UpdatedAt.UTC(),
	}

	if !state.ConfirmedAt.IsZero() {
		confirmed := state.ConfirmedAt.UTC()
		out.ConfirmedAt = &confirmed
	}

	if d := state.Delivery; d != nil {
		out.Delivery = &selectionJSON{
			Carrier:         string(d.Carrier),
			PickupPointCode: d.PickupPointCode,
			RegionCode:      d.RegionCode,
			City:            d.City,
			Address:         d.Address,
		}
	}

	for _, group := range state.Groups {
		g, err := checkoutGroupResponse(group)
		if err != nil {
			return checkoutJSON{}, err
		}
		if group.Paid() {
			paid, err := money.FromKopecks(int64(out.PaidTotal)).Add(money.FromKopecks(int64(g.Total)))
			if err != nil {
				return checkoutJSON{}, err
			}
			out.PaidTotal = rubles(paid.Kopecks())
		}
		if !group.Excluded() {
			base, err := money.FromKopecks(int64(out.BaseTotal)).Add(money.FromKopecks(int64(g.BaseTotal)))
			if err != nil {
				return checkoutJSON{}, err
			}
			out.BaseTotal = rubles(base.Kopecks())
		}
		out.Groups = append(out.Groups, g)
	}

	discount, err := money.FromKopecks(int64(out.BaseTotal)).Sub(state.GoodsTotal)
	if err != nil {
		return checkoutJSON{}, err
	}
	if discount.IsNegative() {
		return checkoutJSON{}, money.ErrInvalid
	}
	out.Discount = rubles(discount.Kopecks())
	return out, nil
}

func (h *checkoutHandlers) respond(
	w http.ResponseWriter,
	r *http.Request,
	status int,
	state checkout.State,
	err error,
) {
	if err != nil {
		if state.ID != "" || errors.Is(err, checkout.ErrDeliveryChanged) || errors.Is(err, checkout.ErrPaymentPaused) {
			body, encodeErr := h.responseBody(r, state)
			if encodeErr != nil {
				writeError(w, r, encodeErr)
				return
			}
			writeErrorWithCheckout(w, r, err, &body)
			return
		}
		writeError(w, r, err)
		return
	}
	body, err := h.responseBody(r, state)
	if err != nil {
		writeError(w, r, err)

		return
	}

	writeJSON(w, status, body)
}

// create — покупка из корзины; заголовок Idempotency-Key обязателен.
func (h *checkoutHandlers) create(w http.ResponseWriter, r *http.Request) {
	state, err := h.checkouts.Create(r.Context(), BuyerID(r.Context()), r.Header.Get("Idempotency-Key"))
	h.respond(w, r, http.StatusCreated, state, err)
}

func (h *checkoutHandlers) get(w http.ResponseWriter, r *http.Request) {
	checkoutID, err := ids.ParseUUID(r.PathValue("checkoutId"))
	if err != nil {
		writeError(w, r, checkout.ErrNotFound)

		return
	}

	state, err := h.checkouts.Get(r.Context(), BuyerID(r.Context()), checkoutID)
	h.respond(w, r, http.StatusOK, state, err)
}

// quote — выбрать пункт выдачи и посчитать доставку у продавцов.
func (h *checkoutHandlers) quote(w http.ResponseWriter, r *http.Request) {
	checkoutID, err := ids.ParseUUID(r.PathValue("checkoutId"))
	if err != nil {
		writeError(w, r, checkout.ErrNotFound)

		return
	}

	var body quoteRequestJSON
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)

		return
	}

	carrier, err := delivery.ParseCarrier(body.Carrier)
	if err != nil {
		writeError(w, r, err)

		return
	}

	if _, err := delivery.NormalizeCity(body.City); err != nil {
		writeError(w, r, err)
		return
	}
	state, err := h.checkouts.Quote(
		r.Context(),
		BuyerID(r.Context()),
		checkoutID,
		carrier,
		body.PickupPointCode,
		body.City,
	)
	h.respond(w, r, http.StatusOK, state, err)
}

// confirm — перепроверить у магазинов и подтвердить покупку.
func (h *checkoutHandlers) confirm(w http.ResponseWriter, r *http.Request) {
	checkoutID, err := ids.ParseUUID(r.PathValue("checkoutId"))
	if err != nil {
		writeError(w, r, checkout.ErrNotFound)

		return
	}

	state, err := h.checkouts.Confirm(r.Context(), BuyerID(r.Context()), checkoutID)
	h.respond(w, r, http.StatusOK, state, err)
}

func (h *checkoutHandlers) responseBody(
	r *http.Request,
	state checkout.State,
) (checkoutJSON, error) {
	body, err := toCheckoutJSON(state)
	if err != nil {
		return body, err
	}
	sellers, err := h.checkouts.Sellers(r.Context(), []checkout.State{state})
	if err != nil {
		return body, err
	}
	setGroupSellers(&body, sellers)
	return body, nil
}

func setGroupSellers(body *checkoutJSON, sellers map[ids.TenantID]*participant.Seller) {
	for i := range body.Groups {
		body.Groups[i].Seller = sellers[ids.TenantID(body.Groups[i].TenantID)]
	}
}

func trackingResponse(value *checkout.Tracking) *trackingJSON {
	if value == nil {
		return nil
	}
	return &trackingJSON{Carrier: value.Carrier, Number: value.Number, URL: value.URL}
}

func creationStatus(result checkout.CreationResult) string {
	switch result {
	case checkout.CreationPending:
		return "creating"
	case checkout.CreationCreated:
		return "created"
	case checkout.CreationUnconfirmed:
		return "unconfirmed"
	case checkout.CreationRejected:
		return "rejected"
	default:
		return ""
	}
}

func checkoutGroupResponse(group checkout.Group) (checkoutGroupJSON, error) {
	g := checkoutGroupJSON{
		TenantID:                  string(group.TenantID),
		Carriers:                  append([]string{}, group.Carriers...),
		Regions:                   append([]string{}, group.Regions...),
		ReconciliationRequired:    group.ReconciliationRequired,
		CreationStatus:            creationStatus(group.CreationResult()),
		ExternalCheckoutID:        group.ExternalCheckoutID,
		Lines:                     make([]checkoutLineJSON, 0, len(group.Lines)),
		Goods:                     rubles(group.Goods.Kopecks()),
		Status:                    group.Status,
		OrderID:                   group.OrderID,
		DeliveryPrice:             rubles(group.DeliveryPrice.Kopecks()),
		Reason:                    group.Reason,
		PaymentHold:               group.PaymentHold,
		Tracking:                  trackingResponse(group.Tracking),
		ReturnFailed:              group.ReturnFailed,
		ReturnReason:              group.ReturnReason,
		PollFailures:              group.PollFailures,
		NextPollAt:                group.NextPollAt,
		CreateFailures:            group.CreateFailures,
		LastErrorCode:             group.LastErrorCode,
		CancellationAttempts:      group.CancellationAttempts,
		CancellationLastErrorCode: group.CancellationLastErrorCode,
		CreateExhausted:           group.CreateExhausted,
	}

	for _, change := range group.CurrentPrices {
		g.CurrentPrices = append(
			g.CurrentPrices,
			priceChangeJSON{
				ProductID: change.ProductID,
				Price:     rubles(change.Price.Kopecks()),
			},
		)
	}

	for _, remaining := range group.ReturnRemaining {
		g.ReturnRemaining = append(
			g.ReturnRemaining,
			returnLineJSON{
				OfferID:  remaining.Offer.String(),
				Quantity: remaining.Quantity,
			},
		)
	}
	if !group.CreationDeadline.IsZero() {
		deadline := group.CreationDeadline
		g.CreationDeadline = &deadline
	}
	if !group.ExpiresAt.IsZero() {
		expires := group.ExpiresAt
		g.ExpiresAt = &expires
	}
	lines, base, err := checkoutLinesResponse(group.Lines)
	if err != nil {
		return checkoutGroupJSON{}, err
	}
	g.Lines = lines
	g.BaseTotal = rubles(base.Kopecks())

	if q := group.Quote; q != nil {
		g.Quote = &quoteJSON{
			Status: string(q.Status),
			Reason: q.Reason,

			MinDays:   q.MinDays,
			MaxDays:   q.MaxDays,
			ExpiresAt: q.ExpiresAt.UTC(),
		}
	}
	discount, err := money.FromKopecks(int64(g.BaseTotal)).Sub(group.Goods)
	if err != nil {
		return checkoutGroupJSON{}, err
	}
	if discount.IsNegative() {
		return checkoutGroupJSON{}, money.ErrInvalid
	}
	g.Discount = rubles(discount.Kopecks())
	groupTotal, err := group.Goods.Add(group.DeliveryPrice)
	if err != nil {
		return checkoutGroupJSON{}, err
	}
	g.Total = rubles(groupTotal.Kopecks())
	return g, nil
}

func checkoutLinesResponse(source []checkout.Line) ([]checkoutLineJSON, money.Money, error) {
	var lines []checkoutLineJSON
	var total money.Money
	for _, line := range source {
		amount, err := cart.DiscountedAmount(line.Price, line.Quantity, line.Discounts)
		if err != nil {
			return nil, money.Money{}, err
		}

		base, discount, percent, err := lineBreakdown(line.Price, line.Quantity, amount)
		if err != nil {
			return nil, money.Money{}, err
		}
		baseTotal, err := total.Add(base)
		if err != nil {
			return nil, money.Money{}, err
		}
		total = baseTotal

		lines = append(
			lines,
			checkoutLineJSON{
				OfferID:         line.Offer.String(),
				ProductID:       string(line.Offer.Product),
				Name:            line.Name,
				Quantity:        line.Quantity,
				Price:           rubles(line.Price.Kopecks()),
				Amount:          rubles(amount.Kopecks()),
				BaseAmount:      rubles(base.Kopecks()),
				Discount:        rubles(discount.Kopecks()),
				DiscountPercent: percent,
			},
		)
	}

	return lines, total, nil
}

func supportedCarrierCodes() []string {
	result := []string{}
	for _, carrier := range delivery.Carriers() {
		result = append(result, string(carrier.Code))
	}
	return result
}
