package githubapp

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// webhook HMAC 验签：同密钥通过；错误密钥、缺头、sha1= 前缀拒绝。

func TestVerifySignature_ValidSecret(t *testing.T) {
	secret := "whsec_test"
	body := []byte(`{"action":"opened","number":1}`)

	sig := SignSignature(secret, body)
	if !VerifySignature(secret, body, sig) {
		t.Fatal("same secret must verify")
	}
}

func TestVerifySignature_WrongSecret(t *testing.T) {
	body := []byte(`{"action":"opened"}`)
	sig := SignSignature("secret-a", body)
	if VerifySignature("secret-b", body, sig) {
		t.Fatal("different secret must be rejected")
	}
}

func TestVerifySignature_MissingHeader(t *testing.T) {
	body := []byte(`{}`)
	if VerifySignature("secret", body, "") {
		t.Fatal("missing header must be rejected")
	}
	if VerifySignature("", body, SignSignature("", body)) {
		t.Fatal("empty secret must be rejected (unsigned traffic never accepted)")
	}
}

func TestVerifySignature_RejectsSha1Prefix(t *testing.T) {
	body := []byte(`{}`)
	sum := sha256.Sum256(body)
	sha1ish := "sha1=" + hex.EncodeToString(sum[:])
	if VerifySignature("secret", body, sha1ish) {
		t.Fatal("sha1= prefix must be rejected")
	}
}

func TestVerifySignature_TamperedBody(t *testing.T) {
	secret := "whsec"
	sig := SignSignature(secret, []byte(`{"a":1}`))
	if VerifySignature(secret, []byte(`{"a":2}`), sig) {
		t.Fatal("tampered body must be rejected")
	}
}
