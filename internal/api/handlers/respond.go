package handlers

import (
	"log/slog"
	"net/http"

	"github.com/getsentry/sentry-go"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/render"
)

// errResponse is the canonical JSON error body returned by all handlers.
type errResponse struct {
	HTTPStatusCode int    `json:"-"`
	Error          string `json:"error"`
}

func (e *errResponse) Render(w http.ResponseWriter, r *http.Request) error {
	render.Status(r, e.HTTPStatusCode)
	return nil
}

// validationErrResponse is the canonical JSON validation error body.
type validationErrResponse struct {
	HTTPStatusCode int               `json:"-"`
	Error          string            `json:"error"`
	Fields         map[string]string `json:"fields"`
}

func (e *validationErrResponse) Render(w http.ResponseWriter, r *http.Request) error {
	render.Status(r, e.HTTPStatusCode)
	return nil
}

func respondErr(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if status >= 500 {
		slog.ErrorContext(r.Context(), "server error",
			slog.String("request_id", chiMiddleware.GetReqID(r.Context())),
			slog.Int("status", status),
			slog.String("error", msg),
			slog.String("path", r.URL.Path),
		)
		if hub := sentry.GetHubFromContext(r.Context()); hub != nil {
			hub.CaptureMessage(msg)
		}
	}
	render.Render(w, r, &errResponse{HTTPStatusCode: status, Error: msg}) //nolint:errcheck
}

func respondValidation(w http.ResponseWriter, r *http.Request, err *ValidationError) {
	if err == nil {
		err = &ValidationError{}
	}
	render.Render(w, r, &validationErrResponse{ //nolint:errcheck
		HTTPStatusCode: http.StatusUnprocessableEntity,
		Error:          "validation failed",
		Fields:         err.Fields,
	})
}

func respondOK(w http.ResponseWriter, r *http.Request, payload any) {
	render.JSON(w, r, payload)
}

func respondCreated(w http.ResponseWriter, r *http.Request, payload any) {
	render.Status(r, http.StatusCreated)
	render.JSON(w, r, payload)
}

func respondNoContent(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}
