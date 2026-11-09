package client_test

// Sweep 2026-11-08, finding G-5. The EIP-712 domain name and version were taken
// from the seller's 402. A wrong domain cannot steal funds, because the token
// rejects the signature, but it comes back as 402-after-payment, which the
// client holds against the budget as unconfirmed: a hostile or broken seller
// could burn a whole session budget at $0.01 a call without moving any money.

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DV1-321/scalpstream-mcp/client"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/sha3"
)

const usdcOnBase = `"scheme":"exact","network":"eip155:8453","amount":"10000","asset":"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913","payTo":"0x1111111111111111111111111111111111111111"`

// A seller naming another domain for the real token is refused unsigned, so
// nothing is held against the budget for a payment it was going to fail.
func TestADomainThatIsNotUSDCsIsRefusedUnsigned(t *testing.T) {
	for name, accept := range map[string]string{
		"wrong name":          `{` + usdcOnBase + `,"extra":{"name":"Not USDC","version":"2"}}`,
		"wrong version":       `{` + usdcOnBase + `,"extra":{"name":"USD Coin","version":"9"}}`,
		"name is not a string": `{` + usdcOnBase + `,"extra":{"name":7,"version":"2"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv, paid := hostileSeller(t, accept)
			c := &client.Client{Signer: testSigner(t)}
			_, err := c.Fetch(context.Background(), srv.URL)
			var refused *client.ErrPaymentRequired
			if !errors.As(err, &refused) {
				t.Fatalf("want ErrPaymentRequired, got %v", err)
			}
			if paid.Load() != 0 {
				t.Fatal("a payment was signed for a domain the token does not have")
			}
			if held, n := c.Unconfirmed(); held != "0" || n != 0 {
				t.Errorf("held %s over %d calls, want nothing", held, n)
			}
		})
	}
}

// A seller that omits the domain gets a signature over the real one: the
// on-chain DOMAIN_SEPARATOR of Base USDC, written out here rather than computed
// by the code under test, so this is what the contract would verify.
func TestTheSignatureIsOverThePinnedDomainWhateverTheSellerSent(t *testing.T) {
	const baseUSDCDomainSeparator = "02fa7265e7c5d81118673727957699e4d68f74cd74b7db77da710fe8a2c7834f"

	var presented string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sig := r.Header.Get("PAYMENT-SIGNATURE"); sig != "" {
			presented = sig
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"x402Version":2,"accepts":[{` + usdcOnBase + `}]}`))
	}))
	t.Cleanup(srv.Close)

	signer := testSigner(t)
	c := &client.Client{Signer: signer}
	if _, err := c.Fetch(context.Background(), srv.URL); err != nil {
		t.Fatalf("an option with no extra must still be paid: %v", err)
	}

	raw, err := base64.StdEncoding.DecodeString(presented)
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Payload struct {
			Signature     string `json:"signature"`
			Authorization struct {
				From, To, Value, ValidAfter, ValidBefore, Nonce string
			} `json:"authorization"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	a := env.Payload.Authorization

	keccak := func(parts ...[]byte) []byte {
		h := sha3.NewLegacyKeccak256()
		for _, p := range parts {
			h.Write(p)
		}
		return h.Sum(nil)
	}
	word := func(hexValue string) []byte {
		b, err := hex.DecodeString(strings.TrimPrefix(hexValue, "0x"))
		if err != nil {
			t.Fatal(err)
		}
		return append(make([]byte, 32-len(b)), b...)
	}
	number := func(dec string) []byte {
		n, ok := new(big.Int).SetString(dec, 10)
		if !ok {
			t.Fatalf("not a number: %q", dec)
		}
		return n.FillBytes(make([]byte, 32))
	}
	separator, _ := hex.DecodeString(baseUSDCDomainSeparator)
	structHash := keccak(
		keccak([]byte("TransferWithAuthorization(address from,address to,uint256 value,uint256 validAfter,uint256 validBefore,bytes32 nonce)")),
		word(a.From), word(a.To), number(a.Value), number(a.ValidAfter), number(a.ValidBefore), word(a.Nonce),
	)
	digest := keccak([]byte{0x19, 0x01}, separator, structHash)

	sig, err := hex.DecodeString(strings.TrimPrefix(env.Payload.Signature, "0x"))
	if err != nil || len(sig) != 65 {
		t.Fatalf("signature %q: %v", env.Payload.Signature, err)
	}
	compact := append([]byte{sig[64]}, sig[:64]...)
	pub, _, err := ecdsa.RecoverCompact(compact, digest)
	if err != nil {
		t.Fatal(err)
	}
	address := "0x" + hex.EncodeToString(keccak(pub.SerializeUncompressed()[1:])[12:])
	if !strings.EqualFold(address, signer.Address()) {
		t.Errorf("the signature recovers to %s, not the signer %s: it is not over Base USDC's real domain", address, signer.Address())
	}
}
