package hiapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// Webhook callback headers.
const (
	TimestampHeader = "X-HiAPI-Timestamp"
	SignatureHeader = "X-HiAPI-Signature"
	// DefaultTolerance rejects callbacks whose timestamp is older or newer than
	// this, guarding against replay.
	DefaultTolerance = 300 * time.Second
)

// VerifyParams are optional overrides for verification. The zero value uses
// DefaultTolerance and the real wall clock.
type VerifyParams struct {
	// Tolerance is the max allowed clock skew. A negative value disables the
	// timestamp check. Zero means DefaultTolerance.
	Tolerance time.Duration
	// Now overrides the current time (for testing). Zero means time.Now().
	Now time.Time
	// Secret, when set, overrides the client's default signing key for this one
	// call. Used only by Webhooks.Verify; VerifyWebhook takes its secret directly.
	Secret string
}

// VerifyWebhook verifies a raw callback and returns the parsed [Task].
//
// It recomputes the signature over the raw request body (never re-serialized
// JSON — re-encoding can change bytes and break the check), guards against
// replay with a timestamp tolerance, and parses the verified body into a Task.
//
// The signature scheme is hex(HMAC_SHA256(secret, timestamp + "." + body)).
// On any failure it returns a [*WebhookError].
func VerifyWebhook(body []byte, signature, timestamp, secret string, params VerifyParams) (*Task, error) {
	if signature == "" || timestamp == "" {
		return nil, &WebhookError{Reason: "missing signature or timestamp header"}
	}
	if secret == "" {
		return nil, &WebhookError{Reason: "no webhook signing secret configured"}
	}

	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return nil, &WebhookError{Reason: "invalid timestamp header", Err: err}
	}

	tolerance := params.Tolerance
	if tolerance == 0 {
		tolerance = DefaultTolerance
	}
	if tolerance > 0 {
		now := params.Now
		if now.IsZero() {
			now = time.Now()
		}
		skew := now.Unix() - ts
		if skew < 0 {
			skew = -skew
		}
		if time.Duration(skew)*time.Second > tolerance {
			return nil, &WebhookError{Reason: "timestamp outside the allowed tolerance (possible replay)"}
		}
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return nil, &WebhookError{Reason: "signature mismatch"}
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, &WebhookError{Reason: "verified body is not valid JSON", Err: err}
	}
	return taskFromMap(payload), nil
}

// Webhooks is the client.Webhooks service — verifies inbound callbacks.
type Webhooks struct {
	secret string
}

// Verify verifies a callback using its request headers.
//
// It pulls X-HiAPI-Timestamp / X-HiAPI-Signature from headers (canonicalized by
// net/http). The signing secret defaults to the one set via WithWebhookSecret
// (or HIAPI_WEBHOOK_SECRET); set params.Secret to override it for this call.
func (w *Webhooks) Verify(body []byte, headers http.Header, params VerifyParams) (*Task, error) {
	secret := params.Secret
	if secret == "" {
		secret = w.secret
	}
	if secret == "" {
		return nil, &WebhookError{
			Reason: "no webhook secret: set params.Secret, WithWebhookSecret on the client, or HIAPI_WEBHOOK_SECRET",
		}
	}
	return VerifyWebhook(
		body,
		headers.Get(SignatureHeader),
		headers.Get(TimestampHeader),
		secret,
		params,
	)
}
