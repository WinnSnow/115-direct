package wecom

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"testing"
)

func TestDecryptAndSignature(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	aesKey := base64.StdEncoding.EncodeToString(key)
	aesKey = aesKey[:len(aesKey)-1]
	message := []byte("<xml><Content>hello</Content></xml>")
	corpID := "corp-test"
	plain := append(bytes.Repeat([]byte{1}, 16), make([]byte, 4)...)
	binary.BigEndian.PutUint32(plain[16:20], uint32(len(message)))
	plain = append(plain, message...)
	plain = append(plain, corpID...)
	pad := 32 - len(plain)%32
	plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, _ := aes.NewCipher(key)
	encrypted := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, key[:16]).CryptBlocks(encrypted, plain)
	encoded := base64.StdEncoding.EncodeToString(encrypted)
	got, err := Decrypt(aesKey, corpID, encoded)
	if err != nil || !bytes.Equal(got, message) {
		t.Fatalf("decrypt: %q %v", got, err)
	}
	if Signature("token", "1", "2", encoded) != Signature("token", "2", "1", encoded) {
		t.Fatal("signature must sort inputs")
	}
	if _, err := Decrypt(aesKey, "other", encoded); err == nil {
		t.Fatal("corp id mismatch accepted")
	}
}
