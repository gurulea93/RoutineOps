package keystore

import (
	"encoding/asn1"
	"fmt"
	"math/big"
)

// ecdsaRawToASN1 переводит «сырую» ECDSA-подпись r||s (фиксированной длины, как
// отдаёт Windows NCryptSignHash / CNG) в ASN.1 DER SEQUENCE{r,s} — формат,
// который ожидает crypto/tls от crypto.Signer. raw — конкатенация r и s равной
// длины (по размеру порядка кривой).
func ecdsaRawToASN1(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw)%2 != 0 {
		return nil, fmt.Errorf("ecdsa: некорректная длина сырой подписи %d", len(raw))
	}
	n := len(raw) / 2
	r := new(big.Int).SetBytes(raw[:n])
	s := new(big.Int).SetBytes(raw[n:])
	return asn1.Marshal(struct{ R, S *big.Int }{r, s})
}
