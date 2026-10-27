package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// The daemon's own identity key (ECDSA P-256). Approved devices pin its public
// key at enrolment and the private relay channel (private_relay.go) proves it
// on every session, so a relay in the middle cannot pose as this computer.
// The key never leaves this file; it is created on first use.

const daemonIdentityFile = "daemon-identity.pem"

type daemonIdentity struct {
	key  *ecdsa.PrivateKey
	spki string // base64 SPKI DER of the public key
}

var (
	daemonIdentityMu    sync.Mutex
	daemonIdentityCache = map[string]*daemonIdentity{}
)

// loadDaemonIdentity returns the identity stored in dataDir, creating it once.
func loadDaemonIdentity(dataDir string) (*daemonIdentity, error) {
	path := filepath.Join(dataDir, daemonIdentityFile)
	daemonIdentityMu.Lock()
	defer daemonIdentityMu.Unlock()
	if id := daemonIdentityCache[path]; id != nil {
		return id, nil
	}
	var key *ecdsa.PrivateKey
	if b, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(b)
		if block == nil {
			return nil, errors.New("daemon identity: not PEM")
		}
		parsed, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		key = parsed
	} else if os.IsNotExist(err) {
		if key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
			return nil, err
		}
		der, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			return nil, err
		}
		if err := writeFileAtomicSynced(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	id := &daemonIdentity{key: key, spki: base64.StdEncoding.EncodeToString(der)}
	daemonIdentityCache[path] = id
	return id, nil
}

// sign returns a P1363 (r||s) signature over SHA-256 of msg, the form
// WebCrypto verifies.
func (d *daemonIdentity) sign(msg []byte) (string, error) {
	digest := sha256.Sum256(msg)
	r, s, err := ecdsa.Sign(rand.Reader, d.key, digest[:])
	if err != nil {
		return "", err
	}
	out := make([]byte, 64)
	r.FillBytes(out[:32])
	s.FillBytes(out[32:])
	return base64.RawURLEncoding.EncodeToString(out), nil
}
