package hiapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"testing"
	"time"
)

const testSecret = "whsec_test_key"

// sign produces a valid signature for body at timestamp, mirroring the server.
func sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhook_Valid(t *testing.T) {
	body := []byte(`{"taskId":"t1","model":"m","status":"success"}`)
	ts := "1781492551"
	sig := sign(testSecret, ts, body)
	fixedNow := time.Unix(1781492560, 0)

	task, err := VerifyWebhook(body, sig, ts, testSecret, VerifyParams{Now: fixedNow})
	if err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
	if task.TaskID != "t1" || task.Status != "success" {
		t.Fatalf("parsed: %+v", task)
	}
}

func TestVerifyWebhook_SignatureMismatch(t *testing.T) {
	body := []byte(`{"taskId":"t1"}`)
	ts := "1781492551"
	_, err := VerifyWebhook(body, "deadbeef", ts, testSecret, VerifyParams{Now: time.Unix(1781492560, 0)})
	var we *WebhookError
	if !errors.As(err, &we) {
		t.Fatalf("expected *WebhookError, got %v", err)
	}
}

func TestVerifyWebhook_BodyTamperedBreaksSignature(t *testing.T) {
	body := []byte(`{"taskId":"t1","status":"success"}`)
	ts := "1781492551"
	sig := sign(testSecret, ts, body)
	tampered := []byte(`{"taskId":"t1","status":"fail"}`)
	if _, err := VerifyWebhook(tampered, sig, ts, testSecret, VerifyParams{Now: time.Unix(1781492560, 0)}); err == nil {
		t.Fatal("tampered body should fail verification")
	}
}

func TestVerifyWebhook_StaleTimestamp(t *testing.T) {
	body := []byte(`{"taskId":"t1"}`)
	ts := "1781492551"
	sig := sign(testSecret, ts, body)
	// now is 10 minutes after ts, beyond the 5-minute default tolerance.
	now := time.Unix(1781492551+600, 0)
	_, err := VerifyWebhook(body, sig, ts, testSecret, VerifyParams{Now: now})
	if err == nil {
		t.Fatal("stale timestamp should fail")
	}
}

func TestVerifyWebhook_ToleranceDisabled(t *testing.T) {
	body := []byte(`{"taskId":"t1"}`)
	ts := "1781492551"
	sig := sign(testSecret, ts, body)
	now := time.Unix(1781492551+99999, 0)
	// Negative tolerance disables the timestamp check.
	task, err := VerifyWebhook(body, sig, ts, testSecret, VerifyParams{Tolerance: -1, Now: now})
	if err != nil {
		t.Fatalf("disabled tolerance should pass: %v", err)
	}
	if task.TaskID != "t1" {
		t.Fatalf("parsed: %+v", task)
	}
}

func TestVerifyWebhook_MissingHeaders(t *testing.T) {
	if _, err := VerifyWebhook([]byte("{}"), "", "123", testSecret, VerifyParams{}); err == nil {
		t.Fatal("missing signature should fail")
	}
	if _, err := VerifyWebhook([]byte("{}"), "sig", "", testSecret, VerifyParams{}); err == nil {
		t.Fatal("missing timestamp should fail")
	}
}

func TestVerifyWebhook_BadTimestamp(t *testing.T) {
	if _, err := VerifyWebhook([]byte("{}"), "sig", "notanumber", testSecret, VerifyParams{}); err == nil {
		t.Fatal("non-numeric timestamp should fail")
	}
}

func TestWebhooks_VerifyFromHeaders(t *testing.T) {
	body := []byte(`{"taskId":"t1","status":"success"}`)
	ts := "1781492551"
	sig := sign(testSecret, ts, body)

	w := &Webhooks{secret: testSecret}
	headers := http.Header{}
	headers.Set(TimestampHeader, ts)
	headers.Set(SignatureHeader, sig)

	task, err := w.Verify(body, headers, VerifyParams{Now: time.Unix(1781492560, 0)})
	if err != nil {
		t.Fatalf("verify from headers: %v", err)
	}
	if task.TaskID != "t1" {
		t.Fatalf("parsed: %+v", task)
	}
}

func TestWebhooks_PerCallSecretOverride(t *testing.T) {
	body := []byte(`{"taskId":"t1"}`)
	ts := "1781492551"
	sig := sign("other_secret", ts, body)

	w := &Webhooks{secret: testSecret} // client default differs
	headers := http.Header{}
	headers.Set(TimestampHeader, ts)
	headers.Set(SignatureHeader, sig)

	task, err := w.Verify(body, headers, VerifyParams{Secret: "other_secret", Now: time.Unix(1781492560, 0)})
	if err != nil {
		t.Fatalf("override secret: %v", err)
	}
	if task.TaskID != "t1" {
		t.Fatalf("parsed: %+v", task)
	}
}

func TestWebhooks_NoSecret(t *testing.T) {
	w := &Webhooks{secret: ""}
	if _, err := w.Verify([]byte("{}"), http.Header{}, VerifyParams{}); err == nil {
		t.Fatal("missing secret should fail")
	}
}
