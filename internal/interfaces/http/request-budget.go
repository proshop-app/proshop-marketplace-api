package http

import (
	"context"
	"net/http"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

type requestClass int

// Классы маршрутов. Корзина и оформление имеют собственный пул для запросов HTTP.
const (
	readRequest requestClass = iota + 1
	checkoutRequest
	writeRequest
	backgroundRequest
	importRequest
)

// ErrRequestTimeout — общий срок запроса истёк до начала ответа.
var ErrRequestTimeout = errs.New(errs.Unavailable, "http.request_timeout", "время запроса истекло")

type budgetWriter struct {
	http.ResponseWriter
	request *http.Request
	written bool
	expired bool
}

// requestBudget получает класс из регистрации маршрута: класс выбирает пул базы
// со своими сроками SQL, общий срок запроса отменяет работу вместе с SQL.
func requestBudget(class requestClass, deps Deps, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		timeout := deps.RequestTimeout
		database := ports.DatabaseRead
		switch class {
		case readRequest:
		case checkoutRequest:
			database = ports.DatabaseCheckout
		case writeRequest:
			database = ports.DatabaseWrite
		case backgroundRequest:
			database = ports.DatabaseBackground
		case importRequest:
			database = ports.DatabaseBackground
			timeout = deps.ReferenceImportTimeout
		default:
			panic("маршрут без класса срока запроса")
		}
		ctx, cancel := context.WithTimeout(ports.WithDatabaseClass(r.Context(), database), timeout)
		defer cancel()
		request := r.WithContext(ctx)
		response := &budgetWriter{ResponseWriter: w, request: request}
		next(response, request)
		if !response.written && ctx.Err() == context.DeadlineExceeded {
			response.WriteHeader(http.StatusServiceUnavailable)
		}
	}
}

func (w *budgetWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *budgetWriter) WriteHeader(status int) {
	if w.written {
		return
	}
	w.written = true
	if w.request.Context().Err() == context.DeadlineExceeded {
		w.expired = true
		w.Header().Del("Content-Length")
		w.Header().Del("Content-Encoding")
		w.Header().Del("ETag")
		w.Header().Set("Cache-Control", "no-store")
		writeError(w.ResponseWriter, w.request, ErrRequestTimeout)
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *budgetWriter) Write(body []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	if w.expired {
		return len(body), nil
	}
	return w.ResponseWriter.Write(body)
}
