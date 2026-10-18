package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strconv"
	"time"
)

// Signer creates and verifies expiring signatures for URL paths. It is used
// for download links that clients fetch without sending credentials.
type Signer struct{ key []byte }

// NewSigner creates a Signer. With an empty secret a random key is used, so
// signatures are only valid for the lifetime of the process.
func NewSigner(secret string) *Signer {
	if secret != "" {
		return &Signer{key: []byte(secret)}
	}
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	return &Signer{key: k}
}

func (s *Signer) mac(path string, expires int64) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(path))
	m.Write([]byte{0})
	m.Write([]byte(strconv.FormatInt(expires, 10)))
	return hex.EncodeToString(m.Sum(nil))
}

// Sign returns the query string authorizing access to path for ttl.
func (s *Signer) Sign(path string, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	return url.Values{"expires": {strconv.FormatInt(exp, 10)}, "signature": {s.mac(path, exp)}}.Encode()
}

// Verify checks the expires and signature query parameters for path.
func (s *Signer) Verify(path string, q url.Values) bool {
	exp, err := strconv.ParseInt(q.Get("expires"), 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(s.mac(path, exp)), []byte(q.Get("signature")))
}
