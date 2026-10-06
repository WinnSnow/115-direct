package organize

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
)

func SignMedia(secret []byte, id string) string {
	return signValue(secret, id)
}

func SignJellyfinRequest(secret []byte, id string) string {
	return signValue(secret, "jellyfin|"+id)
}

func signValue(secret []byte, value string) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func VerifyMedia(secret []byte, id, signature string) bool {
	return verifyValue(secret, id, signature)
}

func VerifyJellyfinRequest(secret []byte, id, signature string) bool {
	return verifyValue(secret, "jellyfin|"+id, signature)
}

func verifyValue(secret []byte, value, signature string) bool {
	want, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(value))
	return hmac.Equal(want, mac.Sum(nil))
}
