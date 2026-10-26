// Package khqrpay is the client for the KHQRPay gateway (khqr.cc /
// anajakpay.com): it creates one dynamic KHQR for one order, and — once an
// operator fills in a verification path — asks the gateway what happened to it.
//
// It is the second one-time checkout channel next to internal/paypal, with the
// same semantics: one 30-day period per payment, no subscription, no wallet.
// Money settles into the merchant's own bank account, so nothing here models a
// balance, a refund or a payout (docs/khqrpay-integration-spec.md §七).
//
// Nothing here reads the environment — the caller passes a Config, tests aim
// BaseURL at an httptest server, and internal/config owns the env plumbing, the
// same split internal/paypal uses.
//
// SECRET DISCIPLINE. The signing secret and the signature derived from it must
// not reach a log line or an error string, and the gateway is not trusted to
// keep them out of its own error bodies: a gateway that echoes the form it
// received echoes the hash too. So no response body is ever copied into an
// error verbatim — only the status code, the provider's code and its message
// survive, and every string is passed through sanitize() with the secret and the
// hash it knows about. This is the lesson internal/api/meta_oauth.go records
// above metaRequest (a transport error once rendered the request URL, secret
// parameters and all, into a string that reached a tenant).
package khqrpay

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/textutil"
)

const (
	// DefaultBaseURL is the production host. It is configurable so a test
	// profile or a staging host never needs a code change.
	//
	// anajakpay.com, not khqr.cc: khqr.cc's / 301s here and both hosts reach the
	// same application, but the endpoint verified with real credentials
	// (2026-10-26) is this one.
	DefaultBaseURL = "https://anajakpay.com"

	// CreatePathTemplate is the built-in create path, with {profile} standing in
	// for the merchant id. It carries the /api prefix and ends in `qr-api`: the
	// marketing page documents neither (`…/qr-api-khqr` without /api answers 404
	// from Laravel's JSON fallback), and the logged-in documentation is the truth —
	// POST /api/{profileId}/payment-gateway/v1/payments/qr-api answered 422
	// "Bakong Token Required", i.e. route and signature were accepted.
	CreatePathTemplate = "/api/{profile}/payment-gateway/v1/payments/qr-api"

	// ProfilePlaceholder is the token a configured path may use to say where the
	// escaped merchant id belongs.
	ProfilePlaceholder = "{profile}"

	// CallbackStatusToken is the literal every callback signature ends with. The
	// gateway only posts a paid callback (status "SUCCESS", whatever the case), and
	// the documented signature is
	// sha256(secret + req_time + transaction_id + amount + "SUCCESS") — the word,
	// not the status field, which is why VerifyCallback signs this constant.
	CallbackStatusToken = "SUCCESS"

	// A QR is created in a second or two; anything beyond this is a hung
	// connection, and there is a buyer staring at an empty dialog.
	requestTimeout = 20 * time.Second

	// Error bodies are the provider's own text; keep enough to diagnose.
	maxErrorBody = 300
)

var (
	// ErrNotConfigured means this deployment has no KHQR credentials, so every
	// call would fail. Callers check Enabled() first and answer "not
	// configured" instead of opening a payment dialog that cannot work.
	ErrNotConfigured = errors.New("khqrpay: profile id/secret not configured")

	// ErrVerifyNotConfigured is returned by Verify when KHQRPAY_VERIFY_PATH is
	// empty — the shipped default, where the webhook is the only signal.
	ErrVerifyNotConfigured = errors.New("khqrpay: no verification path configured")
)

// Config is the credential set behind the KHQRPAY_* environment variables. The
// secret is deliberately a plain field here: internal/config reads it from the
// environment and nothing in this tree persists it.
type Config struct {
	ProfileID string
	Secret    string
	BaseURL   string
	Currency  string
	// CreatePath overrides the whole create path (KHQRPAY_CREATE_PATH). Empty
	// means the built-in template, which is what every test and the shipped
	// default use. It exists because the marketing page's documented path answered
	// 404 on the real gateway (probed 2026-10-26, Laravel's JSON fallback with the
	// requested path echoed): correcting the route must be an .env-go edit, not a
	// rebuild and an upload.
	CreatePath string
	// VerifyPath is the verification endpoint, relative to BaseURL/{ProfileID} and
	// configured on its own. It shares no field, default or placeholder rule with
	// CreatePath: the two endpoints are different routes, and one being customized
	// must never move the other.
	VerifyPath string
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: requestTimeout}}
}

// Enabled reports whether both credentials are present. The operator's
// KHQRPAY_ENABLED switch lives in internal/config (KHQRPayConfig.Enabled), which
// also gates the routes: a switch without credentials is not a channel.
func (c *Client) Enabled() bool {
	return c != nil && strings.TrimSpace(c.cfg.ProfileID) != "" && strings.TrimSpace(c.cfg.Secret) != ""
}

// VerifyConfigured reports whether server-side confirmation is available. When
// it is not, the webhook is a trigger and the local order row is the record
// (docs/khqrpay-integration-spec.md §三.3).
func (c *Client) VerifyConfigured() bool {
	return c.Enabled() && strings.TrimSpace(c.cfg.VerifyPath) != ""
}

// CreateInput is one order, already resolved to a price by the caller: prices
// come from configuration, never from the browser.
type CreateInput struct {
	TransactionID string
	Amount        string // decimal string; rendered to two places for the form
	SuccessURL    string
	// Remark is REQUIRED by the gateway and takes part in the signature. CreateQR
	// refuses an empty one rather than posting a request the gateway rejects.
	Remark string
	// Items and CustomFields are optional and are NOT signed. The gateway echoes
	// them back in the callback, which is what makes a payment match a tenant by
	// eye when the transaction id is not enough (custom_fields is base64 JSON on
	// the wire; this package encodes it).
	Items        string
	CustomFields map[string]any
}

// QR is what the gateway hands back: the KHQR payload to render and the image
// URL the console shows. Either one may be the only thing present.
type QR struct {
	TransactionID string
	QR            string
	QRURL         string
	// ReqTime is the gateway's own clock at creation (data.req_time). It goes into
	// payments.detail so a disputed QR can be lined up against the vendor's logs.
	ReqTime int64
	// Response is the provider's payload as received (with anything that looks
	// like signing material removed), kept for the payments.detail column: when
	// a merchant disputes a QR, the vendor's own words are the only evidence.
	Response map[string]any
}

// Verify is the provider's answer to "what happened to this transaction". Paid
// is the only field the money path acts on, and it is true only for a status
// word this package recognises — never for a missing or unknown one.
type Verify struct {
	TransactionID string
	Status        string
	Amount        string
	Currency      string
	Paid          bool
	Response      map[string]any
}

func (c *Client) baseURL() string {
	if custom := strings.TrimRight(strings.TrimSpace(c.cfg.BaseURL), "/"); custom != "" {
		return custom
	}
	return DefaultBaseURL
}

// endpoint builds {base}/{profile}{suffix}: the shape the configured verify path
// uses. The profile id comes from configuration, never from a request, so nothing
// user-supplied can reshape the path.
func (c *Client) endpoint(suffix string) string {
	return c.baseURL() + "/" + url.PathEscape(strings.TrimSpace(c.cfg.ProfileID)) + suffix
}

// createEndpoint is the create URL: BASE_URL (host only) plus either the
// configured path or the built-in template, with {profile} expanded.
func (c *Client) createEndpoint() string {
	return c.baseURL() + c.expandPath(c.createPath())
}

// createPath is KHQRPAY_CREATE_PATH when set, the built-in template otherwise.
// The default is a literal constant so an unconfigured deployment keeps exactly
// the URL it always sent.
func (c *Client) createPath() string {
	if custom := strings.TrimSpace(c.cfg.CreatePath); custom != "" {
		return custom
	}
	return CreatePathTemplate
}

// expandPath substitutes every {profile} with the escaped merchant id. A path
// without the placeholder is used as written — the operator may have put the id
// in a query string instead — and that choice is theirs; what this function
// guarantees is only that the id, wherever it appears, is escaped and that a path
// pasted without its leading slash still forms a URL instead of gluing the host
// and the first segment together.
func (c *Client) expandPath(path string) string {
	path = strings.TrimSpace(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return strings.ReplaceAll(path, ProfilePlaceholder, url.PathEscape(strings.TrimSpace(c.cfg.ProfileID)))
}

// CreateQR asks for one dynamic KHQR. The amount is formatted exactly once: the
// same string is posted as the `amount` field and fed to the signature, because
// the gateway re-derives the hash from the fields it receives — formatting the
// two independently is how a correct order gets rejected.
//
// remark is REQUIRED (the logged-in docs list it as mandatory and it is part of
// the signature). An empty one is refused here rather than posted: the gateway
// would reject it anyway, and this way the reason names the missing field.
func (c *Client) CreateQR(ctx context.Context, in CreateInput) (QR, error) {
	if !c.Enabled() {
		return QR{}, ErrNotConfigured
	}
	txID := strings.TrimSpace(in.TransactionID)
	if txID == "" {
		return QR{}, errors.New("khqrpay: create qr needs a transaction id")
	}
	amount, err := twoDecimals(in.Amount)
	if err != nil {
		return QR{}, err
	}
	successURL := strings.TrimSpace(in.SuccessURL)
	remark := strings.TrimSpace(in.Remark)
	if remark == "" {
		return QR{}, errors.New("khqrpay: create qr needs a remark (required by the gateway and covered by the signature)")
	}
	hash := c.sign(txID, amount, successURL, remark)

	form := url.Values{}
	form.Set("transaction_id", txID)
	form.Set("amount", amount)
	form.Set("success_url", successURL)
	form.Set("remark", remark)
	form.Set("hash", hash)
	// Optional and unsigned. They exist so a callback can be matched to a tenant by
	// eye; failing to encode them must not cost the buyer their QR, so a marshal
	// failure simply drops the field.
	if items := strings.TrimSpace(in.Items); items != "" {
		form.Set("items", items)
	}
	if encoded := encodeCustomFields(in.CustomFields); encoded != "" {
		form.Set("custom_fields", encoded)
	}

	raw, err := c.postForm(ctx, c.createEndpoint(), form, hash)
	if err != nil {
		return QR{}, err
	}
	b, err := decodeBody(raw)
	if err != nil {
		return QR{}, c.sanitizeErr(err, hash)
	}
	if !b.success() {
		return QR{}, c.sanitizeErr(fmt.Errorf("khqrpay: order rejected (%s)", b.diagnosis()), hash)
	}
	qr, qrURL := b.qrFields()
	if qr == "" && qrURL == "" {
		return QR{}, c.sanitizeErr(fmt.Errorf("khqrpay: response carried neither qr nor qr_url (%s)", b.diagnosis()), hash)
	}
	return QR{
		TransactionID: txID,
		QR:            qr,
		QRURL:         qrURL,
		ReqTime:       b.reqTime(),
		Response:      b.scrubbed(),
	}, nil
}

// encodeCustomFields renders the reconciliation payload the gateway documents:
// base64 of a JSON object, echoed back (already decoded) in the callback. An empty
// map means "send nothing", and a marshal failure means the same — it must never
// fail the order.
func encodeCustomFields(fields map[string]any) string {
	if len(fields) == 0 {
		return ""
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// Verify asks the gateway for one transaction's state. The endpoint's exact
// contract is behind a merchant login, so only what the public docs state is
// implemented: the configured path is called with the transaction id and a
// signature over it. Revisit hash inputs with the real docs before switching
// this path on (docs/khqrpay-integration-spec.md §三.3) — an endpoint that
// expects something else answers an error, which the caller treats as "not
// confirmed" rather than "not paid".
func (c *Client) Verify(ctx context.Context, transactionID string) (Verify, error) {
	if !c.VerifyConfigured() {
		return Verify{}, ErrVerifyNotConfigured
	}
	txID := strings.TrimSpace(transactionID)
	if txID == "" {
		return Verify{}, errors.New("khqrpay: verify needs a transaction id")
	}
	hash := c.signVerify(txID)
	query := url.Values{}
	query.Set("transaction_id", txID)
	query.Set("hash", hash)
	path := "/" + strings.Trim(strings.TrimSpace(c.cfg.VerifyPath), "/")
	raw, err := c.get(ctx, c.endpoint(path)+"?"+query.Encode(), hash)
	if err != nil {
		return Verify{}, err
	}
	b, err := decodeBody(raw)
	if err != nil {
		return Verify{}, c.sanitizeErr(err, hash)
	}
	if !b.success() {
		return Verify{}, c.sanitizeErr(fmt.Errorf("khqrpay: verify rejected (%s)", b.diagnosis()), hash)
	}
	fields := b.statusFields()
	return Verify{
		TransactionID: textutil.FirstNonEmpty(fields.TransactionID, txID),
		Status:        fields.Status,
		Amount:        fields.Amount,
		Currency:      fields.Currency,
		Paid:          fields.IsPaid(),
		Response:      b.scrubbed(),
	}, nil
}

// VerifyCallback checks the signature the gateway puts on every payment callback:
//
//	sha256_hex(secret + req_time + transaction_id + amount + "SUCCESS")
//
// req_time is rendered as a plain decimal integer (no exponent, no ".0"), and
// amount must be the callback's ORIGINAL string — "5.00" and "5" are different
// signatures, so normalising before hashing would reject every real payment.
//
// status is accepted case-insensitively and must be a paid word: the documentation
// says the callback always reports "SUCCESS", so anything else is either not a
// payment or not from a version of the API this code understands — and the
// signature constant is the literal word, not the field.
//
// A missing hash, or req_time of 0 (the field absent), is refused rather than
// "verified" against a value the gateway never sent.
//
// OPERATIONAL TRAP: rotating KHQRPAY_SECRET invalidates callbacks that were
// signed with the old value and are still being retried. Keep the old secret
// configured until the queue has drained, or a paid order will fail verification
// until the gateway gives up.
func (c *Client) VerifyCallback(reqTime int64, transactionID, amount, status, hash string) error {
	if !c.Enabled() {
		return ErrNotConfigured
	}
	if !IsPaidWord(status) {
		return fmt.Errorf("khqrpay: callback status %q is not a paid one",
			textutil.Ellipsize(strings.Join(strings.Fields(status), " "), 32))
	}
	if reqTime <= 0 {
		return errors.New("khqrpay: callback carried no req_time")
	}
	presented := strings.ToLower(strings.TrimSpace(hash))
	if presented == "" {
		return errors.New("khqrpay: callback carried no hash")
	}
	expected := c.signCallback(reqTime, strings.TrimSpace(transactionID), amount)
	if !hmac.Equal([]byte(presented), []byte(expected)) {
		return errors.New("khqrpay: callback signature did not verify")
	}
	return nil
}

// sign is sha1_hex(secret + transaction_id + amount + success_url + remark) —
// the four fields concatenated in that order with no separator.
func (c *Client) sign(txID, amount, successURL, remark string) string {
	return sha1Hex(strings.TrimSpace(c.cfg.Secret) + txID + amount + successURL + remark)
}

// signCallback is the callback signature, kept next to sign so the two can never
// be "unified" by mistake: the create request signs sha1 over four request
// fields, the callback signs sha256 over the gateway's own report.
func (c *Client) signCallback(reqTime int64, transactionID, amount string) string {
	return sha256Hex(strings.TrimSpace(c.cfg.Secret) + strconv.FormatInt(reqTime, 10) +
		transactionID + amount + CallbackStatusToken)
}

// signVerify is the verification call's signature. The public docs only say
// "your transaction id and signed hash", and the request carries no amount to
// re-derive anything from, so it signs the secret and the id.
//
// THE INPUTS ARE A GUESS (secret + transaction_id) — not a fact. Nobody here has
// read the endpoint's documentation (it needs a merchant login), so do not build
// on this ordering and do not repeat it as the vendor's scheme; replace it with
// the real field list and separators as soon as the docs arrive
// (docs/khqrpay-integration-spec.md §三.3). A wrong guess fails closed: the
// endpoint answers an error, which the caller treats as "not confirmed".
func (c *Client) signVerify(txID string) string {
	return sha1Hex(strings.TrimSpace(c.cfg.Secret) + txID)
}

func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (c *Client) postForm(ctx context.Context, endpoint string, form url.Values, hash string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		// The error here is a malformed URL, i.e. our own configuration; it
		// carries the endpoint, which holds the profile id but no secret.
		return nil, fmt.Errorf("khqrpay: build create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.roundTrip(req, hash)
}

func (c *Client) get(ctx context.Context, endpoint, hash string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("khqrpay: build verify request: %w", err)
	}
	return c.roundTrip(req, hash)
}

// roundTrip is the one place a response body is read and judged. A transport
// error carries the request URL verbatim, so it is re-rendered without the
// %w chain and sanitized like everything else.
func (c *Client) roundTrip(req *http.Request, hash string) ([]byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		// The URL of a create request is {base}/{profile}/… : no secret, no
		// hash. Redact anyway — err.Error() is whatever the transport chose to
		// include.
		return nil, c.sanitizeErr(fmt.Errorf("khqrpay: request failed: %s", err.Error()), hash)
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return nil, c.sanitizeErr(fmt.Errorf("khqrpay: read response: %s", readErr.Error()), hash)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Never the body: see the package comment. The provider's own message
		// field is the diagnosis, and even that is sanitized below.
		return nil, c.sanitizeErr(fmt.Errorf("khqrpay: gateway answered HTTP %d (%s)", resp.StatusCode, bodyDiagnosis(raw)), hash)
	}
	return raw, nil
}

// sanitizeErr replaces the secret and the signature in an error string. Both
// travel into WARN/ERROR lines and, through a 5xx envelope, potentially into a
// console; neither may appear. errors.Is is not relied on across this boundary
// (only ErrNotConfigured/ErrVerifyNotConfigured are compared, and those are
// returned verbatim above).
func (c *Client) sanitizeErr(err error, extra ...string) error {
	if err == nil {
		return nil
	}
	return errors.New(sanitize(err.Error(), append([]string{strings.TrimSpace(c.cfg.Secret)}, extra...)...))
}

// sanitize removes every non-empty needle from s, replacing it with "***".
func sanitize(s string, needles ...string) string {
	for _, needle := range needles {
		if len(needle) >= 4 {
			s = strings.ReplaceAll(s, needle, "***")
		}
	}
	return s
}

// bodyDiagnosis is diagnosis() for a body that has already been read and
// rejected for its status code. It never copies the body into the string.
func bodyDiagnosis(raw []byte) string {
	b, err := decodeBody(raw)
	if err != nil {
		return "unreadable response body"
	}
	return b.diagnosis()
}

// ============================================
// Response shapes
// ============================================

// body is one gateway response: the envelope's own two fields plus the payload.
//
// payload is embedded, so the JSON tags of its fields are promoted: a flat body
// ({"qr":…,"qr_url":…}) and a nested one ({"data":{"qr":…}}) decode into the
// same type, and qrFields/statusFields walk the data chain looking for what they
// need. The public Node.js sample reads response.data.data.qr, which is why the
// walk exists at all (docs/khqrpay-integration-spec.md §三.1).
type body struct {
	// The documented envelope is {"responseCode":0,"responseMessage":"Success",
	// "data":{…}}. The older public sample used {"code":…,"message":…}, and a
	// response can still arrive in that shape, so both are read — responseCode
	// wins when present, and success() only accepts 0 there (not 200, not a word).
	Code            json.RawMessage `json:"code"`
	Message         string          `json:"message"`
	ResponseCode    json.RawMessage `json:"responseCode"`
	ResponseMessage string          `json:"responseMessage"`
	payload
	// raw is the body as the vendor sent it, which is what pays off in
	// payments.detail: a reconstruction from the fields this package happens to
	// read would lose everything it did not model.
	raw map[string]any
}

type payload struct {
	QR            string   `json:"qr"`
	QRURL         string   `json:"qr_url"`
	TransactionID string   `json:"transaction_id"`
	Status        string   `json:"status"`
	Amount        string   `json:"amount"`
	Currency      string   `json:"currency"`
	Paid          *bool    `json:"paid"`
	ReqTime       *int64   `json:"req_time"`
	Data          *payload `json:"data"`
}

func decodeBody(raw []byte) (*body, error) {
	var b body
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, errors.New("khqrpay: response is not JSON")
	}
	var verbatim map[string]any
	if err := json.Unmarshal(raw, &verbatim); err == nil {
		b.raw = verbatim
	}
	return &b, nil
}

// qrFields returns the first qr/qr_url pair on the data chain.
func (b *body) qrFields() (string, string) {
	for cur := &b.payload; cur != nil; cur = cur.Data {
		if cur.QR != "" || cur.QRURL != "" {
			return strings.TrimSpace(cur.QR), strings.TrimSpace(cur.QRURL)
		}
	}
	return "", ""
}

// reqTime returns the gateway's own clock from the response, 0 when absent.
func (b *body) reqTime() int64 {
	for cur := &b.payload; cur != nil; cur = cur.Data {
		if cur.ReqTime != nil {
			return *cur.ReqTime
		}
	}
	return 0
}

// statusFields returns the first verification payload on the data chain.
func (b *body) statusFields() statusFields {
	for cur := &b.payload; cur != nil; cur = cur.Data {
		if cur.Status != "" || cur.Amount != "" || cur.Currency != "" || cur.Paid != nil || cur.TransactionID != "" {
			out := statusFields{
				TransactionID: strings.TrimSpace(cur.TransactionID),
				Status:        strings.TrimSpace(cur.Status),
				Amount:        strings.TrimSpace(cur.Amount),
				Currency:      strings.TrimSpace(cur.Currency),
			}
			if cur.Paid != nil {
				out.PaidFlag, out.PaidSet = *cur.Paid, true
			}
			return out
		}
	}
	return statusFields{}
}

type statusFields struct {
	TransactionID string
	Status        string
	Amount        string
	Currency      string
	PaidFlag      bool
	PaidSet       bool
}

// IsPaid reports whether the provider says the money arrived. An explicit
// boolean wins when the endpoint sends one; otherwise only a recognised word
// counts, so an unknown status can never open a plan.
func (s statusFields) IsPaid() bool {
	if s.PaidSet {
		return s.PaidFlag
	}
	return IsPaidWord(s.Status)
}

// IsPaidWord reports whether a provider status word means "the money arrived".
// Exported because the API layer judges the unauthenticated callback body with
// the same vocabulary — two lists would drift, and the drift would show up as a
// payment nobody grants.
func IsPaidWord(status string) bool {
	return paidStatuses[strings.ToLower(strings.TrimSpace(status))]
}

// paidStatuses are the words this package accepts as "paid". The verification
// and callback vocabulary is not in the public docs, so the list is deliberately
// short and anything else is not paid — the failure direction on a money path is
// "do not activate".
var paidStatuses = map[string]bool{
	"success": true, "successful": true, "paid": true,
	"completed": true, "complete": true, "settled": true,
}

// diagnosis is the provider's own explanation, never the raw body: a gateway
// that echoes the submitted form would echo the signature with it.
//
// responseMessage comes first because that is the field the real API fills — the
// 422 "Bakong Token Required" answer only carries responseCode/responseMessage, so
// reading `message` alone left every rejected request with an empty diagnosis.
func (b *body) diagnosis() string {
	code := codeText(b.ResponseCode)
	label := "responseCode "
	if code == "" {
		code = codeText(b.Code)
		label = "code "
	}
	msg := textutil.Ellipsize(oneLine(textutil.FirstNonEmpty(b.ResponseMessage, b.Message)), maxErrorBody)
	switch {
	case code != "" && msg != "":
		return label + code + ": " + msg
	case code != "":
		return label + code
	case msg != "":
		return msg
	}
	return "no responseCode or responseMessage"
}

// oneLine collapses whitespace so a multi-line provider message stays readable in
// a log line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// scrubbed is the response as received, minus anything that looks like signing
// material. The payload goes into payments.detail so a disputed QR can be traced
// back to what the vendor actually said; a signature does not belong in the
// database any more than in a log line. The decoded map is this call's own copy,
// so scrubbing it in place cannot affect the response the caller also reads.
func (b *body) scrubbed() map[string]any {
	if b.raw == nil {
		return nil
	}
	scrubSecretKeys(b.raw)
	return b.raw
}

// scrubSecretKeys removes signing material at any depth, in place.
func scrubSecretKeys(v any) {
	switch node := v.(type) {
	case map[string]any:
		for key, value := range node {
			switch strings.ToLower(key) {
			case "hash", "secret", "signature", "sign", "token":
				delete(node, key)
				continue
			}
			scrubSecretKeys(value)
		}
	case []any:
		for _, item := range node {
			scrubSecretKeys(item)
		}
	}
}

// success reports whether the envelope says "accepted".
//
// responseCode == 0 (a JSON number) is the documented success and the only value
// accepted when that field is present — 200 and "success" were guesses from the
// marketing sample and must not paper over a changed contract. When responseCode
// is absent the older `code` shapes are still honoured, so a response in the
// previously-implemented form keeps working (the QR payload is evidence either
// way, and CreateQR still refuses one with neither qr nor qr_url).
func (b *body) success() bool {
	if code := codeText(b.ResponseCode); code != "" {
		return code == "0"
	}
	switch strings.ToLower(codeText(b.Code)) {
	case "", "0", "200", "success", "ok", "true":
		return true
	}
	return false
}

// codeText renders a code field as text: it is a number in the documented
// envelope and a word in the older sample.
func codeText(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	return strings.Trim(s, `"`)
}

// twoDecimals renders a configured price as the two-place string the gateway
// signs and posts ("29" → "29.00", "0.5" → "0.50"). Lexically, never through a
// float: the string is what the signature covers, and 29.99 has no exact binary
// representation.
func twoDecimals(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", errors.New("khqrpay: amount is empty")
	}
	if strings.HasPrefix(v, "-") || strings.HasPrefix(v, "+") {
		// We only ever charge a positive price; a signed amount is a
		// configuration mistake, not something to normalise.
		return "", fmt.Errorf("khqrpay: amount %q must be a positive decimal", raw)
	}
	intPart, frac, hasDot := strings.Cut(v, ".")
	if !allDigits(intPart) || (hasDot && !allDigits(frac)) {
		return "", fmt.Errorf("khqrpay: amount %q is not a decimal", raw)
	}
	// Three digits, so the third decides the rounding of a price configured
	// with more precision than KHQR can charge.
	frac += "000"
	roundUp := frac[2] >= '5'
	digits := intPart + frac[:2]
	if roundUp {
		digits = incrementDigits(digits)
	}
	return digits[:len(digits)-2] + "." + digits[len(digits)-2:], nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// incrementDigits adds one to a decimal digit string, carrying leftwards.
func incrementDigits(digits string) string {
	out := []byte(digits)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i] != '9' {
			out[i]++
			return string(out)
		}
		out[i] = '0'
	}
	return "1" + string(out)
}
