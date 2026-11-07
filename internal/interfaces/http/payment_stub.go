package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

var paymentTemplate = template.Must(template.New("payment").Parse(`<!doctype html>
<html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Оплата покупки</title></head><body><main>
<h1>Оплата покупки</h1><p>Тестовая касса. Деньги не списываются.</p>
{{if .Paid}}<p>Покупка оплачена</p>{{else}}
<p>К оплате: <strong>{{.Amount}} ₽</strong></p>
<form method="post" action="{{.Path}}/confirm"><button type="submit">Оплатить</button></form>
<form method="post" action="{{.Path}}/cancel"><button type="submit">Отказаться</button></form>
{{end}}<p><a href="{{.ReturnURL}}">Вернуться к покупке</a></p>
</main></body></html>`))

type paymentPage struct {
	ReturnURL string
	Amount    string
	Path      string
	Paid      bool
}

// paymentResponse сохраняет отказ middleware, чтобы касса всегда отвечала в браузерном формате.
type paymentResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (h *checkoutHandlers) pay(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseUUID(r.PathValue("checkoutId"))
	if err != nil {
		writeError(w, r, checkout.ErrNotFound)
		return
	}
	_, err = h.checkouts.Pay(r.Context(), BuyerID(r.Context()), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": "/api/v1/payment-stub/" + string(id)})
}

func (h *checkoutHandlers) paymentStub(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseUUID(r.PathValue("checkoutId"))
	if err != nil {
		writeError(w, r, checkout.ErrNotFound)
		return
	}
	state, err := h.checkouts.Payment(r.Context(), BuyerID(r.Context()), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	amount, err := appcheckout.PaymentAmount(state)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page := paymentPage{
		ReturnURL: h.checkouts.PaymentReturnURL(id),
		Amount:    fmt.Sprintf("%d,%02d", amount.Kopecks()/100, amount.Kopecks()%100),
		Path:      "/api/v1/payment-stub/" + string(id),
		Paid:      checkout.Restore(state).PaymentComplete(),
	}
	var body bytes.Buffer
	if err := paymentTemplate.Execute(&body, page); err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; frame-ancestors 'none'")
	_, _ = w.Write(body.Bytes())
}

func (h *checkoutHandlers) confirmPayment(w http.ResponseWriter, r *http.Request) {
	h.finishPayment(w, r, true)
}

func (h *checkoutHandlers) cancelPayment(w http.ResponseWriter, r *http.Request) {
	h.finishPayment(w, r, false)
}

func (h *checkoutHandlers) finishPayment(w http.ResponseWriter, r *http.Request, confirm bool) {
	id, err := ids.ParseUUID(r.PathValue("checkoutId"))
	if err != nil {
		writeError(w, r, checkout.ErrNotFound)
		return
	}
	if confirm {
		err = h.checkouts.ConfirmPayment(r.Context(), BuyerID(r.Context()), id)
	} else {
		_, err = h.checkouts.Payment(r.Context(), BuyerID(r.Context()), id)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	http.Redirect(w, r, h.checkouts.PaymentReturnURL(id), http.StatusSeeOther)
}

func (w *paymentResponse) Header() http.Header { return w.header }

func (w *paymentResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *paymentResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(body)
}

func (h *checkoutHandlers) paymentBrowser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		response := &paymentResponse{header: make(http.Header)}
		next(response, r)
		if response.status < http.StatusBadRequest {
			for key, values := range response.header {
				w.Header()[key] = values
			}
			w.WriteHeader(response.status)
			_, _ = w.Write(response.body.Bytes())
			return
		}
		id, _ := ids.ParseUUID(r.PathValue("checkoutId"))
		returnURL := h.checkouts.PaymentReturnURL(id)
		w.Header().Set("Cache-Control", "private, no-store")
		if r.Method == http.MethodPost {
			http.Redirect(w, r, returnURL, http.StatusSeeOther)
			return
		}
		message := "Не удалось открыть кассу"
		var detail errorBody
		if json.Unmarshal(response.body.Bytes(), &detail) == nil && detail.Error.Message != "" {
			message = detail.Error.Message
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(response.status)
		_, _ = fmt.Fprintf(
			w,
			`<!doctype html><html lang="ru"><meta charset="utf-8"><title>Оплата покупки</title>
<p>%s</p><a href="%s">Вернуться к покупке</a></html>`,
			template.HTMLEscapeString(message),
			template.HTMLEscapeString(returnURL),
		)
	}
}
