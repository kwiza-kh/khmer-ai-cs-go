package api

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/platform"
	"khmer-ai-cs-go/internal/usage"
)

// requestID assigns a request id (inbound or generated) and echoes it via
// X-Request-ID.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-ID")
		if rid == "" {
			rid = platform.NewUUID()
		}
		r = r.WithContext(context.WithValue(r.Context(), ridKey, rid))
		w.Header().Set("X-Request-ID", rid)
		next.ServeHTTP(w, r)
	})
}

const ridKey ctxKey = 2

// cors enforces the configured origin allowlist (403 for strangers, 204 for
// OPTIONS, always sets the Allow-* headers) — Go/Rust parity.
// pi-lens-ignore: wildcard-cors
func (a *App) cors(next http.Handler) http.Handler {
	allowed := make(map[string]bool, len(a.Cfg.AllowedOrigins))
	for _, o := range a.Cfg.AllowedOrigins {
		allowed[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		// The website-chat widget is embedded on arbitrary customer domains and
		// authenticated by its own publishable token — those routes answer any
		// origin (no credentials; the admin allowlist still guards everything
		// else).
		isWidget := strings.HasPrefix(r.URL.Path, "/api/v1/widget/")
		isAllowed := origin != "" && (allowed[origin] || isWidget)
		if origin != "" && !isAllowed {
			WriteJSON(w, http.StatusForbidden, map[string]string{"error": "跨域请求被拒绝"})
			return
		}
		h := w.Header()
		if isWidget && origin != "" {
			// Any origin, deliberately: the widget is embedded on arbitrary
			// customer domains and authenticated by its own publishable token,
			// so the Allow-Origin echo cannot be narrowed. The dangerous shape
			// is a wildcard *paired with* Access-Control-Allow-Credentials, and
			// this branch never sets it. A request with no Origin is not a CORS
			// request at all, so the old unconditional `*` fallback was dead
			// weight that read like a credentialed wildcard.
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
		} else if isAllowed {
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Add("Vary", "Origin")
		}
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, X-Request-ID")
		// X-Model-List-Warning carries why a model list came back incomplete (the
		// region's listing 404'd or returned nothing). It is a response header
		// rather than part of the body so the "available" contract stays a bare
		// array, which means it is only readable by the console if it is exposed
		// here — the admin UI is served from a different origin than the API.
		h.Set("Access-Control-Expose-Headers", "Content-Length, Content-Disposition, X-Request-ID, X-Model-List-Warning")
		h.Set("Access-Control-Max-Age", "600")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// logging writes one structured line per request (level by status).
func (a *App) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		latency := time.Since(start)
		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"latency_ms", latency.Milliseconds(),
			"ip", clientIP(r),
		}
		if u, ok := UserFrom(r); ok {
			attrs = append(attrs, "user_id", u.UserID)
		}
		switch {
		case rec.status >= 500:
			a.Logger.Error("request", attrs...)
		case rec.status >= 400:
			a.Logger.Warn("request", attrs...)
		default:
			a.Logger.Info("request", attrs...)
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

// WriteHeader is idempotent. net/http ignores a second call and only logs
// "superfluous response.WriteHeader call" (29 such lines in three production
// days); the previous version forwarded that second call and *also* overwrote
// status, so the access log named a code that never reached the wire.
func (s *statusRecorder) WriteHeader(code int) {
	if s.wroteHeader {
		return
	}
	s.wroteHeader = true
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Write marks the header as sent. Without it a handler that writes a body
// (the implicit 200) and later calls WriteHeader would have that later code
// recorded as the status while net/http, header already sent, ignores it —
// the same log/reality split from the other direction.
func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wroteHeader = true
	return s.ResponseWriter.Write(b)
}

// Hijack, Flush and Unwrap pass through so WebSocket upgrades (gorilla) and
// SSE streaming (chatStream) keep working when the logging/audit middlewares
// wrap the ResponseWriter. Without these the wrappers hide the interfaces.
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("response does not implement http.Hijacker")
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// clientIP resolves the originating client address for rate limiting, audit
// attribution and request logs.
//
// Trust boundary: the documented production chain is Cloudflare → local nginx
// (X-Real-IP overwritten with $remote_addr, X-Forwarded-For appended) → this
// process, so the immediate peer being a loopback address means the request
// came through the trusted proxy and the client identity is the value nginx
// computed: X-Real-IP. The client-supplied FIRST X-Forwarded-For element is
// never trusted — an attacker could rotate it per request to get fresh
// rate-limit buckets and forge audit attribution. When the peer is not a
// loopback proxy (direct :8081 access), the socket peer is used and all
// forwarded headers are ignored.
func clientIP(r *http.Request) string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}
	peer = strings.Trim(peer, "[]")
	if isLoopbackIP(peer) {
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
			return xr
		}
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// nginx appends its own peer to XFF, so the last element is the
			// trusted proxy's view of the client; earlier elements are
			// client-suppliable and never used.
			parts := strings.Split(xff, ",")
			for i := len(parts) - 1; i >= 0; i-- {
				if ip := strings.TrimSpace(parts[i]); ip != "" {
					return ip
				}
			}
		}
	}
	if peer == "" {
		return "-"
	}
	return peer
}

func isLoopbackIP(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	return parsed.IsLoopback()
}

// auth parses the Bearer credential (JWT or kcs_ API key), verifies the
// account is active, and attaches CurrentUser.
func (a *App) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" {
			WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "未提供认证令牌"})
			return
		}
		scheme, token, found := strings.Cut(header, " ")
		if !found || scheme != "Bearer" || token == "" {
			WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "认证格式错误"})
			return
		}

		var user *CurrentUser
		// API keys carry no token version — they are revoked by is_active or
		// expiry instead. -1 marks "not applicable".
		jwtVersion := -1
		if strings.HasPrefix(token, "kcs_") {
			resolved, err := a.resolveAPIKey(r.Context(), token)
			if err != nil {
				WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "用户查询失败"})
				return
			}
			if resolved == nil {
				WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "无效的 API 密钥"})
				return
			}
			user = resolved
		} else {
			claims, err := a.JWT.ParseToken(token)
			if err != nil {
				WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "令牌无效或已过期"})
				return
			}
			user = &CurrentUser{UserID: claims.UserID, Username: claims.Username, Role: claims.Role}
			jwtVersion = claims.TokenVersion
		}

		// Production guard: a disabled tenant is rejected on every request, and
		// a JWT whose version no longer matches the account's has been revoked
		// (password change, role change, or a 2FA change). Both checks ride on
		// the one query this middleware already made — which now also resolves
		// the caller's seat (tenant + permissions), so membership costs no extra
		// round trip.
		var isActive bool
		var currentVersion int
		var memberOwnerID *int32
		var memberPerms []byte
		err := a.DB.QueryRow(r.Context(),
			"SELECT u.is_active, u.token_version, t.owner_user_id, t.permissions "+
				"FROM users u LEFT JOIN agent_teams t "+
				"  ON t.agent_user_id = u.user_id AND t.is_active = true "+
				"WHERE u.user_id = $1", user.UserID).
			Scan(&isActive, &currentVersion, &memberOwnerID, &memberPerms)
		if err != nil {
			WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "用户查询失败"})
			return
		}
		if !isActive {
			WriteJSON(w, http.StatusForbidden, map[string]string{"error": "账户已被禁用"})
			return
		}
		if jwtVersion >= 0 && jwtVersion != currentVersion {
			WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "会话已失效，请重新登录"})
			return
		}
		applyMembership(user, memberOwnerID, memberPerms)

		// Tag the context with the caller so auxiliary model spend incurred
		// while serving this request (translation, RAG rerank and rewrite,
		// transcription, image description) is billed to their tenant via
		// gemini.AuxUsageObserver. For a seat that is the owner's tenant, not the
		// member's own (empty) account.
		ctx := usage.WithUser(r.Context(), user.Tenant())
		r = r.WithContext(context.WithValue(ctx, userKey, user))
		next.ServeHTTP(w, r)
	})
}

// resolveAPIKey maps a kcs_ token to its owner (SHA-256 hash lookup; the
// plaintext is never stored). Nil result = unknown/inactive/expired key.
func (a *App) resolveAPIKey(ctx context.Context, token string) (*CurrentUser, error) {
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])
	row := a.DB.QueryRow(ctx,
		"SELECT k.user_id, u.username, u.role::text, k.is_active, k.expires_at "+
			"FROM api_keys k JOIN users u ON u.user_id = k.user_id "+
			"WHERE k.key_hash = $1 AND k.is_active = true", hash)
	var (
		userID    int32
		username  string
		role      string
		isActive  bool
		expiresAt *time.Time
	)
	if err := row.Scan(&userID, &username, &role, &isActive, &expiresAt); err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil, nil
		}
		return nil, err
	}
	if !isActive || (expiresAt != nil && expiresAt.Before(time.Now())) {
		return nil, nil
	}
	_, _ = a.DB.Exec(ctx, "UPDATE api_keys SET last_used_at = NOW() WHERE key_hash = $1", hash)
	return &CurrentUser{UserID: userID, Username: username, Role: role}, nil
}

// rateLimit applies a Redis fixed window (per user when authenticated, else
// per client IP). 429 + Retry-After when exceeded; 503 when Redis is down.
func (a *App) rateLimit(maxRPM uint32) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var key string
			if user, ok := UserFrom(r); ok {
				key = fmt.Sprintf("u:%d", user.UserID)
			} else {
				key = "ip:" + clientIP(r)
			}
			allowed, err := a.Redis.CheckRateLimit(r.Context(), key, maxRPM)
			if err != nil {
				WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "限流服务不可用"})
				return
			}
			if !allowed {
				w.Header().Set("Retry-After", "60")
				WriteJSON(w, http.StatusTooManyRequests, map[string]any{
					"error":       "请求过于频繁",
					"retry_after": 60,
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
