// Package api exposes the REST API for the KYT screening service.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/alert"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/audit"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/metrics"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/screen"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/webhook"
)

// Services bundles all dependencies used by the HTTP handlers.
type Services struct {
	Screen  *screen.Service
	Alerts  *alert.Service
	Webhook *webhook.Processor
	Audit   *audit.Emitter
}

// NewMux returns the HTTP handler with all routes registered.
func NewMux(s *Services) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)
	mux.HandleFunc("/metrics", metrics.Handler())
	mux.HandleFunc("POST /v1/kyt/screen", screenHandler(s))
	mux.HandleFunc("GET /v1/kyt/alerts/{id}", getAlertHandler(s))
	mux.HandleFunc("GET /v1/kyt/alerts", listAlertsHandler(s))
	mux.HandleFunc("POST /v1/kyt/alerts/{id}/assign", assignAlertHandler(s))
	mux.HandleFunc("POST /v1/kyt/alerts/{id}/close", closeAlertHandler(s))
	mux.HandleFunc("POST /v1/webhooks/{vendor}", webhookHandler(s))
	return mux
}

// NewServer wires middleware and returns an *http.Server.
func NewServer(s *Services, addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           NewMux(s),
		ReadHeaderTimeout: 5 * time.Second,
	}
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// AppError carries a code and status code.
type AppError struct {
	Code       string
	Message    string
	StatusCode int
}

func (e *AppError) Error() string { return e.Message }

func newAppError(code, msg string, status int) *AppError {
	return &AppError{Code: code, Message: msg, StatusCode: status}
}

var (
	errBadJSON      = newAppError("bad_json", "invalid JSON body", http.StatusBadRequest)
	errAlertMissing = newAppError("alert_not_found", "alert not found", http.StatusNotFound)
	errAlertClosed  = newAppError("alert_closed", "alert already closed", http.StatusConflict)
	errSigMismatch  = newAppError("signature_mismatch", "webhook signature mismatch", http.StatusUnauthorized)
)

type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, err error) {
	ae := toAppError(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(ae.StatusCode)
	var env errorEnvelope
	env.Error.Code = ae.Code
	env.Error.Message = ae.Message
	_ = json.NewEncoder(w).Encode(env)
}

func toAppError(err error) *AppError {
	if err == nil {
		return nil
	}
	var ae *AppError
	if errors.As(err, &ae) {
		return ae
	}
	if errors.Is(err, alert.ErrNotFound) {
		return errAlertMissing
	}
	if errors.Is(err, alert.ErrAlreadyClosed) {
		return errAlertClosed
	}
	if errors.Is(err, webhook.ErrSignatureMismatch) {
		return errSigMismatch
	}
	if errors.Is(err, webhook.ErrBodyTooLarge) {
		return newAppError("body_too_large", err.Error(), http.StatusRequestEntityTooLarge)
	}
	return newAppError("internal_error", "internal server error", http.StatusInternalServerError)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeJSON(r *http.Request, dst any) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return errBadJSON
	}
	if len(body) == 0 {
		return errBadJSON
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return errBadJSON
	}
	return nil
}

func screenHandler(s *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req screen.Request
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		resp, err := s.Screen.Screen(ctx, req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func getAlertHandler(s *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		a, err := s.Alerts.Get(id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, a)
	}
}

func listAlertsHandler(s *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status := r.URL.Query().Get("status")
		alerts, err := s.Alerts.List(status)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"alerts": alerts})
	}
}

type assignRequest struct {
	Assignee string `json:"assignee"`
}

func assignAlertHandler(s *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req assignRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, err)
			return
		}
		a, err := s.Alerts.Assign(id, req.Assignee)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, a)
	}
}

func closeAlertHandler(s *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req assignRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, err)
			return
		}
		a, err := s.Alerts.Close(id, req.Assignee)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, a)
	}
}

func webhookHandler(s *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vendor := r.PathValue("vendor")
		body, err := webhook.ReadBody(r.Body, 1<<20)
		if err != nil {
			writeError(w, err)
			return
		}
		sig := r.Header.Get("X-Webhook-Signature")
		if sig == "" {
			sig = r.Header.Get("X-Signature")
		}
		res := s.Webhook.Ingest(r.Context(), vendor, body, sig)
		switch {
		case res.Accepted:
			metrics.Global().WebhookAcceptTotal.Add(1)
			writeJSON(w, http.StatusOK, map[string]any{"accepted": true, "event_id": res.EventID})
		case res.Duplicate:
			metrics.Global().WebhookDuplicateTotal.Add(1)
			writeJSON(w, http.StatusOK, map[string]any{"accepted": true, "duplicate": true, "event_id": res.EventID})
		default:
			metrics.Global().WebhookRejectTotal.Add(1)
			if strings.Contains(res.Reason, "signature") {
				writeError(w, errSigMismatch)
				return
			}
			writeError(w, newAppError("bad_webhook", res.Reason, http.StatusBadRequest))
		}
	}
}