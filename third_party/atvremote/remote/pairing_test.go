package remote

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestExponentBytes(t *testing.T) {
	// The common RSA exponent 65537 must be encoded as 0x010001, matching
	// bytes.fromhex(f"0{exponent:X}") in the Python implementation.
	if got := exponentBytes(65537); !bytes.Equal(got, []byte{0x01, 0x00, 0x01}) {
		t.Fatalf("exponentBytes(65537) = %x, want 010001", got)
	}
}

func TestPairingSecret(t *testing.T) {
	clientKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	code := "5A1234"
	secret, err := pairingSecret(&clientKey.PublicKey, &serverKey.PublicKey, code)
	if err != nil {
		t.Fatal(err)
	}

	// Recompute the reference hash the way the protocol defines it.
	h := sha256.New()
	h.Write(clientKey.PublicKey.N.Bytes())
	h.Write([]byte{0x01, 0x00, 0x01})
	h.Write(serverKey.PublicKey.N.Bytes())
	h.Write([]byte{0x01, 0x00, 0x01})
	suffix, _ := hex.DecodeString("1234")
	h.Write(suffix)
	want := h.Sum(nil)

	if !bytes.Equal(secret, want) {
		t.Fatalf("pairingSecret = %x, want %x", secret, want)
	}

	// The first two characters of the pairing code must not affect the hash.
	secret2, err := pairingSecret(&clientKey.PublicKey, &serverKey.PublicKey, "FF1234")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(secret, secret2) {
		t.Fatal("pairing code prefix should not affect the secret")
	}

	if _, err := pairingSecret(&clientKey.PublicKey, &serverKey.PublicKey, "5A12ZZ"); err == nil {
		t.Fatal("expected error for non-hex pairing code")
	}
}
