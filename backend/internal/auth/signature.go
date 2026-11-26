package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// EvidenceSigner signs and verifies temporary HMAC-SHA256 evidence proxy URLs.
type EvidenceSigner struct {
	secret []byte
}

// NewEvidenceSigner constructs an EvidenceSigner using the shared HMAC signing secret.
func NewEvidenceSigner(secret string) *EvidenceSigner {
	return &EvidenceSigner{secret: []byte(secret)}
}

// SignEvidenceURL generates a signed URL valid for ttl starting from now.
func (s *EvidenceSigner) SignEvidenceURL(baseURL, alarmID, kind string, ttl time.Duration) string {
	return s.SignEvidenceURLAt(baseURL, alarmID, kind, ttl, time.Now().UTC())
}

// SignEvidenceURLAt generates a signed URL valid for ttl relative to now (deterministic for tests).
func (s *EvidenceSigner) SignEvidenceURLAt(baseURL, alarmID, kind string, ttl time.Duration, now time.Time) string {
	exp := strconv.FormatInt(now.Add(ttl).Unix(), 10)
	sig := s.computeMAC(alarmID, kind, exp)
	base := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf("%s/api/v1/evidence/%s/%s?exp=%s&sig=%s",
		base,
		url.PathEscape(alarmID),
		url.PathEscape(kind),
		exp,
		sig,
	)
}

// VerifyEvidenceSignature checks HMAC integrity and expiration against now.
func (s *EvidenceSigner) VerifyEvidenceSignature(alarmID, kind, expStr, sigHex string, now time.Time) bool {
	if alarmID == "" || kind == "" || expStr == "" || sigHex == "" {
		return false
	}
	expUnix, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return false
	}
	if expUnix <= now.Unix() {
		return false
	}
	gotSig, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	expectedHex := s.computeMAC(alarmID, kind, expStr)
	expectedSig, err := hex.DecodeString(expectedHex)
	if err != nil {
		return false
	}
	return hmac.Equal(gotSig, expectedSig)
}

func (s *EvidenceSigner) computeMAC(alarmID, kind, expStr string) string {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(alarmID + ":" + kind + ":" + expStr))
	return hex.EncodeToString(mac.Sum(nil))
}
