// Package api assembles the HTTP surface. Phase 1 covers health/readiness
// and the full auth group (login/register/change-password/preferences); the
// remaining feature groups port over in later phases with identical routes.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/auth"
	"khmer-ai-cs-go/internal/config"
	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/platform"
	"khmer-ai-cs-go/internal/rag"
	"khmer-ai-cs-go/internal/realtime"
	"khmer-ai-cs-go/internal/redisstore"
	"khmer-ai-cs-go/internal/security"
)

// App is the shared request state (DB pool, Redis, JWT, AI, config).
type App struct {
	Cfg    *config.Config
	DB     *pgxpool.Pool
	Redis  *redisstore.Client
	JWT    *auth.JWT
	Gemini *gemini.Service
	RAG    *rag.Service
	Logger *slog.Logger
	Sealer *security.Sealer

	// Pipe is the platform pipeline (used to wake outbound workers when an
	// agent reply or campaign template is enqueued).
	Pipe *platform.Pipeline
	// Realtime is the WebSocket inbox hub (may be nil in tests).
	Realtime *realtime.Hub

	// WebhookHandler serves the platform webhook endpoints (mounted by main).
	WebhookHandler http.Handler
}

// stripSourceMarkers removes [Source N] citation leftovers (ragQuery replies).
func stripSourceMarkers(text string) string {
	return gemini.StripSourceMarkers(text)
}

// ApiError maps to the HTTP envelope the frontend expects:
// {"error": "<message>"} with the appropriate status code.
type ApiError struct {
	Status  int
	Message string
}

func (e *ApiError) Error() string { return e.Message }

func ErrBadRequest(msg string) *ApiError   { return &ApiError{http.StatusBadRequest, msg} }
func ErrUnauthorized(msg string) *ApiError { return &ApiError{http.StatusUnauthorized, msg} }
func ErrForbidden(msg string) *ApiError    { return &ApiError{http.StatusForbidden, msg} }
func ErrNotFound(msg string) *ApiError     { return &ApiError{http.StatusNotFound, msg} }
func ErrConflict(msg string) *ApiError     { return &ApiError{http.StatusConflict, msg} }
func ErrTooMany(msg string) *ApiError      { return &ApiError{http.StatusTooManyRequests, msg} }
func ErrInternal(msg string) *ApiError     { return &ApiError{http.StatusInternalServerError, msg} }
func ErrServiceUnavailable(msg string) *ApiError {
	return &ApiError{http.StatusServiceUnavailable, msg}
}

// Handler returns either a JSON-serializable value (200) or an *ApiError.
// Return WithStatus to control the status code (e.g. 201 Created).
type Handler func(w http.ResponseWriter, r *http.Request) (any, error)

// WithStatus carries a body together with its HTTP status code.
type WithStatus struct {
	Status int
	Body   any
}

func (a *App) handle(h Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		val, err := h(w, r)
		if err != nil {
			apiErr, ok := err.(*ApiError)
			if !ok {
				apiErr = ErrInternal(err.Error())
			}
			if apiErr.Status >= 500 {
				a.Logger.Error("request failed", "status", apiErr.Status, "error", apiErr.Message, "path", r.URL.Path)
			}
			WriteJSON(w, apiErr.Status, map[string]string{"error": apiErr.Message})
			return
		}
		if ws, ok := val.(WithStatus); ok {
			WriteJSON(w, ws.Status, ws.Body)
			return
		}
		WriteJSON(w, http.StatusOK, val)
	}
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// CurrentUser is attached by the auth middleware.
type CurrentUser struct {
	UserID   int32
	Username string
	Role     string
}

// IsAdmin — platform admins also manage their own tenant.
func (u *CurrentUser) IsAdmin() bool         { return u.Role == "admin" || u.Role == "platform_admin" }
func (u *CurrentUser) IsPlatformAdmin() bool { return u.Role == "platform_admin" }

type ctxKey int

const userKey ctxKey = 1

func UserFrom(r *http.Request) (*CurrentUser, bool) {
	u, ok := r.Context().Value(userKey).(*CurrentUser)
	return u, ok
}
