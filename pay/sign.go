package pay

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/sha3"
)

// Buyer-side EIP-3009 signing.
//
// This is CLIENT code. The resource server never calls it and never holds a
// key — it exists so our own agent (and the end-to-end test) can pay an EVM
// x402 charge, exactly as internal/xrpl signs an XRPL payment for the agent.
//
// EIP-3009 transferWithAuthorization lets a token holder authorise a transfer
// off-chain; anyone may then submit it and pay the gas. That indirection is
// what keeps the merchant custody-free: the facilitator submits, so the seller
// needs no key and no gas.
//
// The signature is EIP-712 typed data over the token's own domain, so it is
// bound to one token contract on one chain and cannot be replayed elsewhere.

// eip3009TypeHash is keccak256 of the TransferWithAuthorization type signature,
// exactly as USDC's contract computes it.
const eip3009Type = "TransferWithAuthorization(address from,address to,uint256 value,uint256 validAfter,uint256 validBefore,bytes32 nonce)"

// eip712DomainType is keccak256 of the EIP-712 domain type used by USDC
// (name, version, chainId, verifyingContract).
const eip712DomainType = "EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"

// Signer holds a buyer's private key in process memory and signs with it.
//
// THIS SIGNS REAL MAINNET PAYMENTS. Treat the key accordingly: supply it from
// the environment or a secret manager, never a literal or a committed file, and
// prefer a throwaway funded with only what you intend to spend. A deployment
// with a larger balance at stake should not use this at all — it should keep the
// key in a wallet or HSM and sign there, then hand the resulting ExactPayload to
// the client.
//
// The key is never logged, written to disk, or transmitted; only the resulting
// signature leaves the process.
type Signer struct {
	priv *secp256k1.PrivateKey
	addr string
}

// NewSigner parses a hex private key (with or without 0x).
func NewSigner(privHex string) (*Signer, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(privHex), "0x"))
	if err != nil {
		return nil, fmt.Errorf("pay: bad private key hex: %w", err)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("pay: private key must be 32 bytes, got %d", len(b))
	}
	priv := secp256k1.PrivKeyFromBytes(b)
	return &Signer{priv: priv, addr: addressOf(priv.PubKey())}, nil
}

// Address returns the signer's EIP-55 checksummed address.
func (s *Signer) Address() string { return s.addr }

// TokenDomain identifies the ERC-20 whose EIP-712 domain the signature is
// bound to. For USDC on Base these are name "USD Coin", version "2" — read
// from the contract, never assumed, since a wrong domain produces a signature
// the token rejects.
type TokenDomain struct {
	Name     string
	Version  string
	ChainID  int64
	Contract string
}

// AuthorizeTransfer builds and signs an EIP-3009 authorization paying `value`
// atomic units to `to`, valid from now until now+ttl.
//
// The nonce is 32 random bytes, not a counter: the token contract records used
// nonces, so randomness makes each authorization single-use on-chain,
// independently of our own replay store.
func (s *Signer) AuthorizeTransfer(d TokenDomain, to, value string, ttl time.Duration) (ExactPayload, error) {
	var zero ExactPayload

	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return zero, fmt.Errorf("pay: nonce: %w", err)
	}
	now := time.Now().Unix()
	auth := Authorization{
		From:  s.addr,
		To:    to,
		Value: value,
		// validAfter is now-60 to tolerate clock skew between us, the
		// facilitator, and the chain; a tight boundary would reject a valid
		// authorization for no benefit.
		ValidAfter:  fmt.Sprint(now - 60),
		ValidBefore: fmt.Sprint(now + int64(ttl.Seconds())),
		Nonce:       "0x" + hex.EncodeToString(nonce[:]),
	}

	digest, err := eip712Digest(d, auth)
	if err != nil {
		return zero, err
	}
	sig, err := signDigest(s.priv, digest)
	if err != nil {
		return zero, err
	}
	return ExactPayload{Signature: sig, Authorization: auth}, nil
}

// eip712Digest computes keccak256(0x1901 || domainSeparator || structHash).
func eip712Digest(d TokenDomain, a Authorization) ([]byte, error) {
	addrTo, err := addrWord(a.To)
	if err != nil {
		return nil, fmt.Errorf("pay: to: %w", err)
	}
	addrFrom, err := addrWord(a.From)
	if err != nil {
		return nil, fmt.Errorf("pay: from: %w", err)
	}
	verifying, err := addrWord(d.Contract)
	if err != nil {
		return nil, fmt.Errorf("pay: verifyingContract: %w", err)
	}
	value, err := uintWord(a.Value)
	if err != nil {
		return nil, fmt.Errorf("pay: value: %w", err)
	}
	validAfter, err := uintWord(a.ValidAfter)
	if err != nil {
		return nil, fmt.Errorf("pay: validAfter: %w", err)
	}
	validBefore, err := uintWord(a.ValidBefore)
	if err != nil {
		return nil, fmt.Errorf("pay: validBefore: %w", err)
	}
	nonce, err := bytes32Word(a.Nonce)
	if err != nil {
		return nil, fmt.Errorf("pay: nonce: %w", err)
	}

	domainSep := keccak(
		keccak([]byte(eip712DomainType)),
		keccak([]byte(d.Name)),
		keccak([]byte(d.Version)),
		uintWordFromBig(big.NewInt(d.ChainID)),
		verifying,
	)
	structHash := keccak(
		keccak([]byte(eip3009Type)),
		addrFrom, addrTo, value, validAfter, validBefore, nonce,
	)
	return keccak([]byte{0x19, 0x01}, domainSep, structHash), nil
}

// signDigest produces a 65-byte [R || S || V] signature with V in {27,28},
// the encoding Ethereum's ecrecover expects.
func signDigest(priv *secp256k1.PrivateKey, digest []byte) (string, error) {
	sig := ecdsa.SignCompact(priv, digest, false)
	if len(sig) != 65 {
		return "", fmt.Errorf("pay: sign: unexpected signature length %d", len(sig))
	}
	// SignCompact returns [V || R || S] with V in 27..30; Ethereum wants
	// [R || S || V]. Reorder rather than assume the layouts agree.
	v := sig[0]
	if v >= 31 { // compressed-pubkey variants
		v -= 4
	}
	out := make([]byte, 65)
	copy(out[0:64], sig[1:65])
	out[64] = v
	return "0x" + hex.EncodeToString(out), nil
}

func keccak(parts ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

// addrWord left-pads a 20-byte address into a 32-byte EIP-712 word.
func addrWord(addr string) ([]byte, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(addr, "0x"), "0X"))
	if err != nil || len(b) != 20 {
		return nil, fmt.Errorf("not a 20-byte address: %q", addr)
	}
	w := make([]byte, 32)
	copy(w[12:], b)
	return w, nil
}

func uintWord(dec string) ([]byte, error) {
	n, ok := new(big.Int).SetString(dec, 10)
	if !ok {
		return nil, fmt.Errorf("not a decimal integer: %q", dec)
	}
	return uintWordFromBig(n), nil
}

func uintWordFromBig(n *big.Int) []byte {
	w := make([]byte, 32)
	n.FillBytes(w)
	return w
}

func bytes32Word(h string) ([]byte, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(h, "0x"))
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("not 32 bytes: %q", h)
	}
	return b, nil
}

// addressOf derives an EIP-55 address from a public key.
func addressOf(pub *secp256k1.PublicKey) string {
	sum := keccak(pub.SerializeUncompressed()[1:])
	return toChecksum(hex.EncodeToString(sum[12:]))
}

func toChecksum(hexAddr string) string {
	low := strings.ToLower(strings.TrimPrefix(hexAddr, "0x"))
	sum := hex.EncodeToString(keccak([]byte(low)))
	b := []byte(low)
	for i := range b {
		if sum[i] >= '8' && b[i] >= 'a' && b[i] <= 'f' {
			b[i] -= 32
		}
	}
	return "0x" + string(b)
}
