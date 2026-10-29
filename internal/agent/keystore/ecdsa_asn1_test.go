package keystore

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"math/big"
	"testing"
)

// TestECDSARawToASN1VerifiesAgainstGo: собираем raw r||s так же, как CNG, и
// проверяем, что преобразованную DER-подпись принимает crypto/ecdsa.
func TestECDSARawToASN1VerifiesAgainstGo(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	digest := sha256.Sum256([]byte("windows cert store signer"))

	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}

	// Эмулируем выход NCrypt: r и s по 32 байта (размер порядка P-256), big-endian.
	const n = 32
	raw := make([]byte, 2*n)
	r.FillBytes(raw[:n])
	s.FillBytes(raw[n:])

	der, err := ecdsaRawToASN1(raw)
	if err != nil {
		t.Fatalf("ecdsaRawToASN1: %v", err)
	}
	if !ecdsa.VerifyASN1(&key.PublicKey, digest[:], der) {
		t.Fatal("преобразованная DER-подпись не прошла проверку")
	}

	// И что DER действительно содержит исходные r,s.
	var parsed struct{ R, S *big.Int }
	if _, err := asn1.Unmarshal(der, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.R.Cmp(r) != 0 || parsed.S.Cmp(s) != 0 {
		t.Fatal("r/s в DER не совпали с исходными")
	}
}

func TestECDSARawToASN1RejectsBadLength(t *testing.T) {
	for _, raw := range [][]byte{nil, {1, 2, 3}} {
		if _, err := ecdsaRawToASN1(raw); err == nil {
			t.Fatalf("ожидали ошибку на длине %d", len(raw))
		}
	}
}
