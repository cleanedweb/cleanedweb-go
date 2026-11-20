package cleanedweb

import (
	"crypto/rand"
	"fmt"
	"regexp"
)

var idempotencyKeyRE = regexp.MustCompile(`^[A-Za-z0-9._:\-]{1,200}$`)

// NewIdempotencyKey returns a random UUIDv4, the key Run generates when you give none.
func NewIdempotencyKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("cleanedweb: no randomness for an idempotency key: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ValidateIdempotencyKey reports whether key is 1 to 200 characters of letters, digits and . _ : -
func ValidateIdempotencyKey(key string) error {
	if !idempotencyKeyRE.MatchString(key) {
		return invalidArgument("idempotency key must be 1-200 characters: letters, digits, '.', '_', ':', '-'")
	}
	return nil
}

func keyOrNew(key string) (string, error) {
	if key == "" {
		return NewIdempotencyKey(), nil
	}
	return key, ValidateIdempotencyKey(key)
}
