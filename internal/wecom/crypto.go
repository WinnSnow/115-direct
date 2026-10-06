package wecom

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

func Signature(token, timestamp, nonce, encrypted string) string {
	parts := []string{token, timestamp, nonce, encrypted}
	sort.Strings(parts)
	sum := sha1.Sum([]byte(strings.Join(parts, "")))
	return hex.EncodeToString(sum[:])
}

func Decrypt(aesKey, expectedCorpID, encrypted string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(aesKey + "=")
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("invalid EncodingAESKey")
	}
	raw, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil || len(raw)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("invalid encrypted payload")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, key[:aes.BlockSize]).CryptBlocks(plain, raw)
	plain, err = unpad(plain)
	if err != nil || len(plain) < 20 {
		return nil, fmt.Errorf("invalid decrypted payload")
	}
	length := int(binary.BigEndian.Uint32(plain[16:20]))
	if length < 0 || 20+length > len(plain) {
		return nil, fmt.Errorf("invalid message length")
	}
	message, corpID := plain[20:20+length], string(plain[20+length:])
	if expectedCorpID != "" && corpID != expectedCorpID {
		return nil, fmt.Errorf("corp id mismatch")
	}
	return message, nil
}

func unpad(value []byte) ([]byte, error) {
	if len(value) == 0 {
		return nil, fmt.Errorf("empty padding")
	}
	n := int(value[len(value)-1])
	if n < 1 || n > 32 || n > len(value) {
		return nil, fmt.Errorf("bad padding")
	}
	if !bytes.Equal(value[len(value)-n:], bytes.Repeat([]byte{byte(n)}, n)) {
		return nil, fmt.Errorf("bad padding")
	}
	return value[:len(value)-n], nil
}
