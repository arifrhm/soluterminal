package machineauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"time"
)

const (
	AuthorizationHeader = "Authorization"
	BearerPrefix        = "Bearer "
	ContentTypeJSON     = "application/json"
	TimestampHeader     = "X-Solu-Timestamp"
	SignatureHeader     = "X-Solu-Sig"
	DefaultMaxSkew      = 5 * time.Minute
)

var (
	ErrBadTimestamp        = errors.New("bad timestamp")
	ErrTimestampOutOfRange = errors.New("timestamp out of range")
	ErrBadSignature        = errors.New("bad signature")
	ErrIncompleteHeaders   = errors.New("incomplete signature headers")
)

type VerifyResult struct {
	Legacy bool
}

func SignHeaders(h http.Header, token string, body []byte, now time.Time) {
	ts := strconv.FormatInt(now.Unix(), 10)
	h.Set(TimestampHeader, ts)
	h.Set(SignatureHeader, Signature(token, ts, body))
}

func SignRequest(req *http.Request, token string, body []byte, now time.Time) {
	req.Header.Set(AuthorizationHeader, BearerPrefix+token)
	req.Header.Set("Content-Type", ContentTypeJSON)
	SignHeaders(req.Header, token, body, now)
}

func Signature(token, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func VerifyHeaders(h http.Header, token string, body []byte, now time.Time) (VerifyResult, error) {
	return Verify(h.Get(TimestampHeader), h.Get(SignatureHeader), token, body, now, DefaultMaxSkew)
}

func Verify(timestampHeader, signatureHeader, token string, body []byte, now time.Time, maxSkew time.Duration) (VerifyResult, error) {
	switch {
	case signatureHeader != "" && timestampHeader != "":
		ts, err := strconv.ParseInt(timestampHeader, 10, 64)
		if err != nil {
			return VerifyResult{}, ErrBadTimestamp
		}
		if skew := now.Sub(time.Unix(ts, 0)); skew > maxSkew || skew < -maxSkew {
			return VerifyResult{}, ErrTimestampOutOfRange
		}
		got, err := hex.DecodeString(signatureHeader)
		if err != nil {
			return VerifyResult{}, ErrBadSignature
		}
		expected := Signature(token, timestampHeader, body)
		want, err := hex.DecodeString(expected)
		if err != nil {
			return VerifyResult{}, ErrBadSignature
		}
		if !hmac.Equal(got, want) {
			return VerifyResult{}, ErrBadSignature
		}
		return VerifyResult{}, nil
	case signatureHeader == "" && timestampHeader == "":
		return VerifyResult{Legacy: true}, nil
	default:
		return VerifyResult{}, ErrIncompleteHeaders
	}
}
