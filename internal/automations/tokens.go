package automations

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// sign is the hex HMAC of parts under the engine's key, by purpose.
func (e *Engine) sign(purpose string, parts ...string) string {
	mac := hmac.New(sha256.New, e.key)
	mac.Write([]byte(purpose))
	for _, part := range parts {
		mac.Write([]byte{0})
		mac.Write([]byte(part))
	}
	return hex.EncodeToString(mac.Sum(nil))
}

// CheckDispatcherKey says whether key is the dispatcher's.
func (e *Engine) CheckDispatcherKey(key string) bool {
	return e.opts.DispatchSecret != "" && subtle.ConstantTimeCompare([]byte(key), []byte(e.opts.DispatchSecret)) == 1
}

// RunToken is a run's bearer token: good for that run until expires.
func (e *Engine) RunToken(runID string, expires time.Time) string {
	exp := strconv.FormatInt(expires.Unix(), 10)
	return "gstr_" + runID + "." + exp + "." + e.sign("run-token", runID, exp)
}

// ParseRunToken returns the run a token is for, if it is genuine and has
// not expired.
func (e *Engine) ParseRunToken(token string) (string, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(token), "gstr_")
	parts := strings.Split(rest, ".")
	if !ok || len(parts) != 3 {
		return "", errors.New("not a run token")
	}
	runID, exp, sig := parts[0], parts[1], parts[2]
	if !hmac.Equal([]byte(sig), []byte(e.sign("run-token", runID, exp))) {
		return "", errors.New("run token is not genuine")
	}
	unix, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() > unix {
		return "", errors.New("run token expired")
	}
	return runID, nil
}

// CallbackURL is where outside systems call a run back.
func (e *Engine) CallbackURL(runID string) string {
	if e.opts.DispatchURL == "" {
		return ""
	}
	return e.opts.DispatchURL + "/cb/" + runID + "/" + e.CallbackSignature(runID)
}

// CallbackSignature signs a run id for its callback URL.
func (e *Engine) CallbackSignature(runID string) string {
	return e.sign("callback", runID)[:32]
}
