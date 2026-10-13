// Package auth implements password hashing (argon2id, PHC format compatible
// with legacy passlib hashes), JWT session tokens, TOTP 2FA and API keys.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/argon2"
)

// ---------------- Argon2 (PHC format) ----------------

const (
	argonTime    = 1
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// HashPassword creates a new argon2id PHC hash.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword validates a password against a PHC-format argon2 hash.
// Supports any m/t/p parameters (legacy passlib hashes parse identically).
func VerifyPassword(password, hash string) bool {
	parts := strings.Split(hash, "$")
	// expected: ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, key]
	if len(parts) != 6 || !strings.HasPrefix(parts[1], "argon2") {
		return false
	}
	var mem, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &p); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		salt, err = base64.StdEncoding.DecodeString(parts[4])
		if err != nil {
			return false
		}
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		want, err = base64.StdEncoding.DecodeString(parts[5])
		if err != nil {
			return false
		}
	}
	var got []byte
	if parts[1] == "argon2id" {
		got = argon2.IDKey([]byte(password), salt, t, mem, p, uint32(len(want)))
	} else {
		got = argon2.Key([]byte(password), salt, t, mem, p, uint32(len(want)))
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ---------------- Random helpers ----------------

// RandomToken returns a URL-safe random string of n bytes entropy.
func RandomToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// RandomHex returns a hex string of n bytes.
func RandomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// ---------------- JWT ----------------

// Claims is the session token payload.
type Claims struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	APIKeyID int64  `json:"api_key_id,omitempty"`
	jwt.RegisteredClaims
}

// IssueToken signs a session JWT for the user (valid days days, not before validFrom).
func IssueToken(secret string, userID int64, username string, apiKeyID int64, days int, validFrom time.Time) (string, error) {
	now := time.Now().UTC()
	if validFrom.After(now) {
		now = validFrom
	}
	claims := Claims{
		UserID:   userID,
		Username: username,
		APIKeyID: apiKeyID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "servy",
			Subject:   strconv.FormatInt(userID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(days) * 24 * time.Hour)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// ParseToken validates and decodes a session token.
func ParseToken(secret, token string) (*Claims, error) {
	var claims Claims
	_, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	return &claims, nil
}

// ---------------- TOTP ----------------

// GenerateTOTPSecret creates a base32 TOTP secret for a user.
func GenerateTOTPSecret(username string) (secret string, url string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "servy", AccountName: username})
	if err != nil {
		return "", "", err
	}
	return key.Secret(), key.URL(), nil
}

// VerifyTOTP validates a 6-digit code against a secret (allows ±1 step skew if enabled).
func VerifyTOTP(secret, code string, skew bool) bool {
	opts := totp.ValidateOpts{Period: 30, Skew: 0, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}
	if skew {
		opts.Skew = 1
	}
	ok, err := totp.ValidateCustom(strings.TrimSpace(code), secret, time.Now().UTC(), opts)
	if err != nil {
		return false
	}
	return ok
}

// TOTPCode returns the current code (testing helper).
func TOTPCode(secret string) string {
	code, _ := totp.GenerateCode(secret, time.Now().UTC())
	return code
}
