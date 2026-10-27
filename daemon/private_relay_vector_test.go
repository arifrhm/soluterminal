package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The web client (web/src/lib/privateRelay) implements the same handshake in
// TypeScript. This test pins the exchange to a fixture both sides check: the
// hello and reply bytes, the signed messages, the derived keys and sealed
// records, all for fixed keys. The TypeScript tests replay it against the
// browser implementation; this one recomputes everything here with the
// daemon's own functions, so a change on either side fails a test.
//
// Regenerate (signatures are randomised, the rest is deterministic):
//
//	SOLU_WRITE_PRIVATE_RELAY_VECTOR=1 go test -run TestPrivateRelayWebVector .

const privateRelayVectorPath = "../web/src/lib/privateRelay/testdata/handshake-vector.json"

type vectorJWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	D   string `json:"d"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

type vectorRecord struct {
	Seq    uint64 `json:"seq"`
	Plain  string `json:"plain"`
	Sealed string `json:"sealed"` // hex
}

type privateRelayVector struct {
	DeviceJWK       vectorJWK      `json:"deviceJwk"`
	DeviceSPKI      string         `json:"deviceSpki"`
	DeviceID        string         `json:"deviceId"`
	Target          string         `json:"target"`
	Time            string         `json:"time"`
	Nonce           string         `json:"nonce"`
	ClientEphJWK    vectorJWK      `json:"clientEphJwk"`
	ClientEph       string         `json:"clientEph"`
	HelloSigMessage string         `json:"helloSigMessage"`
	HelloRaw        string         `json:"helloRaw"`
	IdentitySPKI    string         `json:"identitySpki"`
	DaemonEphJWK    vectorJWK      `json:"daemonEphJwk"`
	ReplyNonce      string         `json:"replyNonce"`
	ReplySigMessage string         `json:"replySigMessage"`
	ReplyRaw        string         `json:"replyRaw"`
	Shared          string         `json:"shared"` // hex
	C2SKey          string         `json:"c2sKey"` // hex
	S2CKey          string         `json:"s2cKey"` // hex
	C2S             []vectorRecord `json:"c2s"`
	S2C             []vectorRecord `json:"s2c"`
}

func vectorScalar(label string) []byte {
	s := sha256.Sum256([]byte("SOLU-PRIVATE-V1 test vector " + label))
	return s[:]
}

func vectorECDSA(t *testing.T, label string) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), vectorScalar(label))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func vectorECDH(t *testing.T, label string) *ecdh.PrivateKey {
	t.Helper()
	k, err := ecdh.P256().NewPrivateKey(vectorScalar(label))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func jwkOf(t *testing.T, d []byte, pub []byte) vectorJWK {
	t.Helper()
	if len(pub) != 65 || pub[0] != 4 {
		t.Fatalf("unexpected point encoding")
	}
	b := base64.RawURLEncoding.EncodeToString
	return vectorJWK{Kty: "EC", Crv: "P-256", D: b(d), X: b(pub[1:33]), Y: b(pub[33:])}
}

func spkiOf(t *testing.T, k *ecdsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(der)
}

func TestPrivateRelayWebVector(t *testing.T) {
	write := os.Getenv("SOLU_WRITE_PRIVATE_RELAY_VECTOR") == "1"
	var recorded privateRelayVector
	if !write {
		raw, err := os.ReadFile(privateRelayVectorPath)
		if err != nil {
			t.Fatalf("read vector (regenerate with SOLU_WRITE_PRIVATE_RELAY_VECTOR=1): %v", err)
		}
		if err := json.Unmarshal(raw, &recorded); err != nil {
			t.Fatal(err)
		}
	}

	device := vectorECDSA(t, "device")
	identityKey := vectorECDSA(t, "identity")
	identity := &daemonIdentity{key: identityKey, spki: spkiOf(t, identityKey)}
	clientEph := vectorECDH(t, "client-eph")
	daemonEph := vectorECDH(t, "daemon-eph")
	b64 := base64.RawURLEncoding.EncodeToString
	devPub, _ := device.PublicKey.Bytes()
	devD, _ := device.Bytes()

	v := privateRelayVector{
		DeviceJWK:    jwkOf(t, devD, devPub),
		DeviceSPKI:   spkiOf(t, device),
		Target:       testMachine + "/wsl",
		Time:         "1800000000",
		Nonce:        b64(vectorScalar("hello-nonce")[:16]),
		ClientEphJWK: jwkOf(t, clientEph.Bytes(), clientEph.PublicKey().Bytes()),
		ClientEph:    b64(clientEph.PublicKey().Bytes()),
		IdentitySPKI: identity.spki,
		DaemonEphJWK: jwkOf(t, daemonEph.Bytes(), daemonEph.PublicKey().Bytes()),
		ReplyNonce:   b64(vectorScalar("reply-nonce")[:16]),
	}
	v.DeviceID = deviceID(v.DeviceSPKI)

	hello := privateHello{V: 1, Device: v.DeviceID, Target: v.Target, Time: v.Time, Nonce: v.Nonce, Eph: v.ClientEph}
	v.HelloSigMessage = string(privateHelloMessage(hello))
	if write {
		td := testDevice{key: device, spki: v.DeviceSPKI}
		hello.Sig = td.sign(t, privateHelloMessage(hello))
	} else {
		var h privateHello
		if err := json.Unmarshal([]byte(recorded.HelloRaw), &h); err != nil {
			t.Fatal(err)
		}
		hello.Sig = h.Sig
	}
	if !verifyDeviceSignature(&device.PublicKey, privateHelloMessage(hello), hello.Sig) {
		t.Fatal("hello signature does not verify")
	}
	helloRaw, err := json.Marshal(hello)
	if err != nil {
		t.Fatal(err)
	}
	v.HelloRaw = string(helloRaw)

	reply := privateReply{Eph: b64(daemonEph.PublicKey().Bytes()), Nonce: v.ReplyNonce, Identity: identity.spki}
	v.ReplySigMessage = string(privateReplyMessage(helloRaw, reply))
	if write {
		if reply.Sig, err = identity.sign(privateReplyMessage(helloRaw, reply)); err != nil {
			t.Fatal(err)
		}
	} else {
		var r privateReply
		if err := json.Unmarshal([]byte(recorded.ReplyRaw), &r); err != nil {
			t.Fatal(err)
		}
		reply.Sig = r.Sig
	}
	if !verifyDeviceSignature(&identityKey.PublicKey, privateReplyMessage(helloRaw, reply), reply.Sig) {
		t.Fatal("reply signature does not verify")
	}
	replyRaw, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	v.ReplyRaw = string(replyRaw)

	shared, err := daemonEph.ECDH(clientEph.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	if other, _ := clientEph.ECDH(daemonEph.PublicKey()); !bytes.Equal(other, shared) {
		t.Fatal("ECDH disagrees")
	}
	v.Shared = hex.EncodeToString(shared)
	salt := sha256.Sum256(append(append([]byte{}, helloRaw...), replyRaw...))
	for info, dst := range map[string]*string{"c2s": &v.C2SKey, "s2c": &v.S2CKey} {
		k, err := hkdf.Key(sha256.New, shared, salt[:], privateRelayVersion+" "+info, 32)
		if err != nil {
			t.Fatal(err)
		}
		*dst = hex.EncodeToString(k)
	}
	c2s, s2c, err := privateKeys(shared, helloRaw, replyRaw)
	if err != nil {
		t.Fatal(err)
	}
	aad := []byte(privateRelayVersion)
	for i, plain := range []string{"\x00\x00\x00\x01\x01", "\x00\x00\x00\x01\x02GET / HTTP/1.1\r\n\r\n"} {
		v.C2S = append(v.C2S, vectorRecord{Seq: uint64(i), Plain: plain, Sealed: hex.EncodeToString(c2s.Seal(nil, privateNonce(uint64(i)), []byte(plain), aad))})
	}
	for i, plain := range []string{"\x00\x00\x00\x01\x02HTTP/1.1 200 OK\r\n\r\n", "\x00\x00\x00\x01\x03"} {
		v.S2C = append(v.S2C, vectorRecord{Seq: uint64(i), Plain: plain, Sealed: hex.EncodeToString(s2c.Seal(nil, privateNonce(uint64(i)), []byte(plain), aad))})
	}

	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if write {
		if err := os.MkdirAll(filepath.Dir(privateRelayVectorPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(privateRelayVectorPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, _ := json.MarshalIndent(recorded, "", "  ")
	want = append(want, '\n')
	if !bytes.Equal(got, want) {
		t.Fatalf("private relay vector drifted from %s; regenerate with SOLU_WRITE_PRIVATE_RELAY_VECTOR=1 and update the web client\n got: %s\nwant: %s", privateRelayVectorPath, got, want)
	}
}
