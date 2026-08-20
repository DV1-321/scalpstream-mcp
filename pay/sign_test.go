package pay

import (
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// baseSepoliaUSDC is the domain read FROM THE CONTRACT: name "USD Coin",
// version "2", chain 84532, at the Sepolia USDC address. A wrong domain yields
// a signature the token rejects, so this is pinned, not assumed.
func baseSepoliaUSDC() TokenDomain {
	return TokenDomain{Name: "USD Coin", Version: "2", ChainID: 84532, Contract: USDCBaseSepolia}
}

// TestSignatureRecoversToSigner is the property that matters: the 65-byte
// [R||S||V] we emit must ecrecover to the signer's own address. If it does not,
// the token contract's ecrecover would attribute the transfer to someone else
// and reject it. This checks our whole chain — EIP-712 digest, compact-sig
// reordering, V normalisation — against secp256k1's own recovery.
// testKey was generated for these tests and has never been used on any network,
// mainnet or testnet. It is published deliberately so the tests are reproducible;
// it must never be funded. Do not copy it into anything that spends.
const testKey = "0xcc20f897f3181022d04694efab0c83f1697b2a1e0da3359ec25f27cce9c1ccd2"

func TestSignatureRecoversToSigner(t *testing.T) {
	s, err := NewSigner(testKey)
	if err != nil {
		t.Fatal(err)
	}
	if s.Address() != "0xC526BED50BA5E1B0D006878537c13816E788CdA7" {
		t.Fatalf("address = %s, want the generated testnet address", s.Address())
	}

	pay, err := s.AuthorizeTransfer(baseSepoliaUSDC(),
		"0xD0f5d2E48e67A265174593aCe2cd540337C1564a", "110000", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	digest, err := eip712Digest(baseSepoliaUSDC(), pay.Authorization)
	if err != nil {
		t.Fatal(err)
	}

	// Rebuild the compact form secp256k1 expects ([V||R||S], V in 27..) from our
	// Ethereum [R||S||V], then recover.
	raw, _ := hex.DecodeString(pay.Signature[2:])
	if len(raw) != 65 {
		t.Fatalf("signature is %d bytes, want 65", len(raw))
	}
	compact := make([]byte, 65)
	compact[0] = raw[64] // V (27/28)
	copy(compact[1:], raw[:64])

	pub, _, err := ecdsa.RecoverCompact(compact, digest)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if got := addressOf(pub); got != s.Address() {
		t.Errorf("signature recovered to %s, want %s", got, s.Address())
	}
}

// TestChecksumMatchesKnownAddresses guards the address derivation against the
// two addresses whose checksums were independently verified (EIP-55).
func TestChecksumMatchesKnownAddresses(t *testing.T) {
	for _, want := range []string{
		"0xC526BED50BA5E1B0D006878537c13816E788CdA7",
		"0xD0f5d2E48e67A265174593aCe2cd540337C1564a",
		"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913", // USDC Base mainnet
	} {
		if got := toChecksum(want); got != want {
			t.Errorf("toChecksum(%s) = %s", want, got)
		}
	}
}

// TestDomainSeparatorMatchesContract pins the EIP-712 domain separator against
// the value read live from Base mainnet USDC (DOMAIN_SEPARATOR() ->
// 0x02fa72...834f). If our domain encoding drifts, this catches it offline
// instead of at settlement.
func TestDomainSeparatorMatchesContract(t *testing.T) {
	d := TokenDomain{Name: "USD Coin", Version: "2", ChainID: 8453, Contract: USDCBase}
	sep := keccak(
		keccak([]byte(eip712DomainType)),
		keccak([]byte(d.Name)),
		keccak([]byte(d.Version)),
		uintWordFromBig(big.NewInt(d.ChainID)),
		mustAddrWord(t, d.Contract),
	)
	const want = "02fa7265e7c5d81118673727957699e4d68f74cd74b7db77da710fe8a2c7834f"
	if got := hex.EncodeToString(sep); got != want {
		t.Errorf("domain separator = %s\n            want   %s", got, want)
	}
}

func mustAddrWord(t *testing.T, a string) []byte {
	t.Helper()
	w, err := addrWord(a)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// A uint256 has no negative range and no values above 2^256-1. Both used to
// pass through big.Int.FillBytes silently or fatally: a negative amount encoded
// as its magnitude (so "-1" authorised a transfer of 1) and an oversized one
// panicked with "buffer too small to fit value".
func TestUintWordRejectsOutOfRangeValues(t *testing.T) {
	for _, bad := range []string{
		"-1", // was silently encoded as +1
		"-100000",
		"115792089237316195423570985008687907853269984665640564039457584007913129639936", // 2^256, was a panic
	} {
		if _, err := uintWord(bad); err == nil {
			t.Errorf("uintWord(%q) accepted an out-of-range value", bad)
		}
	}
}

// The boundary value must still encode: 2^256-1 is the largest legal uint256.
func TestUintWordAcceptsTheBoundary(t *testing.T) {
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	w, err := uintWord(max.String())
	if err != nil {
		t.Fatalf("uintWord(2^256-1) = %v, want it accepted", err)
	}
	if len(w) != 32 {
		t.Errorf("got a %d-byte word, want 32", len(w))
	}
}

// A bad quote must be refused, not signed. AuthorizeTransfer is exported, so a
// caller without its own price cap relies on this.
func TestAuthorizeTransferRefusesANegativeAmount(t *testing.T) {
	s, err := NewSigner("0x1111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	d := TokenDomain{Name: "USD Coin", Version: "2", ChainID: 8453,
		Contract: "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"}
	if _, err := s.AuthorizeTransfer(d, "0x0000000000000000000000000000000000000001", "-1", time.Minute); err == nil {
		t.Error("signed an authorization for a negative amount")
	}
}
