package pki

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadOrCreateCA_CreateAndReload(t *testing.T) {
	dir := t.TempDir()

	ca1, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateCA: %v", err)
	}
	if ca1.Cert == nil || ca1.Key == nil || len(ca1.DER) == 0 {
		t.Fatal("CA fields not populated")
	}

	certInfo, err := os.Stat(filepath.Join(dir, "ca.crt"))
	if err != nil {
		t.Fatalf("stat ca.crt: %v", err)
	}
	if got := certInfo.Mode().Perm(); got != 0o644 {
		t.Fatalf("ca.crt mode = %o, want 0644", got)
	}

	keyInfo, err := os.Stat(filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatalf("stat ca.key: %v", err)
	}
	if got := keyInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("ca.key mode = %o, want 0600", got)
	}

	ca2, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateCA reload: %v", err)
	}
	if !bytes.Equal(ca1.DER, ca2.DER) {
		t.Fatal("reloaded CA DER differs from created CA")
	}
}

func TestLoadOrCreateCA_OnlyCertExists(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("not-a-cert"), 0o644); err != nil {
		t.Fatalf("write ca.crt: %v", err)
	}
	if _, err := LoadOrCreateCA(dir); err == nil {
		t.Fatal("expected error when only ca.crt exists")
	}
}

func TestMTLSRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ca, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateCA: %v", err)
	}

	serverCert, err := ca.IssueServerCert([]string{"127.0.0.1", "localhost"}, time.Hour)
	if err != nil {
		t.Fatalf("IssueServerCert: %v", err)
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", ca.ServerTLSConfig(serverCert))
	if err != nil {
		t.Fatalf("tls.Listen: %v", err)
	}
	defer ln.Close()

	hostID := "host-abc-123"
	peerDone := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			peerDone <- err
			return
		}
		defer conn.Close()
		tlsConn, ok := conn.(*tls.Conn)
		if !ok {
			peerDone <- errNotTLS
			return
		}
		if err := tlsConn.Handshake(); err != nil {
			peerDone <- err
			return
		}
		state := tlsConn.ConnectionState()
		if len(state.PeerCertificates) == 0 {
			peerDone <- errNoPeer
			return
		}
		got, err := HostIDFromCert(state.PeerCertificates[0])
		if err != nil {
			peerDone <- err
			return
		}
		if got != hostID {
			peerDone <- errWrongHostID
			return
		}
		peerDone <- nil
	}()

	key, csrDER, err := NewKeyAndCSR()
	if err != nil {
		t.Fatalf("NewKeyAndCSR: %v", err)
	}
	certDER, _, err := ca.SignClientCSR(csrDER, hostID, time.Hour)
	if err != nil {
		t.Fatalf("SignClientCSR: %v", err)
	}
	clientCfg, err := ClientTLSConfig(ca.DER, certDER, key, "localhost")
	if err != nil {
		t.Fatalf("ClientTLSConfig: %v", err)
	}

	conn, err := tls.Dial("tcp", ln.Addr().String(), clientCfg)
	if err != nil {
		t.Fatalf("tls.Dial: %v", err)
	}
	defer conn.Close()

	if err := <-peerDone; err != nil {
		t.Fatalf("server side: %v", err)
	}
}

var (
	errNotTLS      = errString("not tls conn")
	errNoPeer      = errString("no peer cert")
	errWrongHostID = errString("wrong host id")
)

type errString string

func (e errString) Error() string { return string(e) }

func TestPinnedTLSConfig(t *testing.T) {
	dir1 := t.TempDir()
	ca1, err := LoadOrCreateCA(dir1)
	if err != nil {
		t.Fatalf("LoadOrCreateCA ca1: %v", err)
	}
	pin1 := Fingerprint(ca1.DER)

	dir2 := t.TempDir()
	ca2, err := LoadOrCreateCA(dir2)
	if err != nil {
		t.Fatalf("LoadOrCreateCA ca2: %v", err)
	}

	t.Run("right pin succeeds", func(t *testing.T) {
		serverCert, err := ca1.IssueServerCert([]string{"127.0.0.1", "localhost"}, time.Hour)
		if err != nil {
			t.Fatalf("IssueServerCert: %v", err)
		}
		ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
			Certificates: []tls.Certificate{serverCert},
			MinVersion:   tls.VersionTLS13,
			NextProtos:   []string{"h2"},
		})
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		defer ln.Close()
		serveOnce(t, ln)

		conn, err := tls.Dial("tcp", ln.Addr().String(), PinnedTLSConfig(pin1, "localhost"))
		if err != nil {
			t.Fatalf("dial with right pin: %v", err)
		}
		conn.Close()
	})

	t.Run("wrong pin fails", func(t *testing.T) {
		serverCert, err := ca1.IssueServerCert([]string{"127.0.0.1", "localhost"}, time.Hour)
		if err != nil {
			t.Fatalf("IssueServerCert: %v", err)
		}
		ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
			Certificates: []tls.Certificate{serverCert},
			MinVersion:   tls.VersionTLS13,
			NextProtos:   []string{"h2"},
		})
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		defer ln.Close()
		serveOnce(t, ln)

		_, err = tls.Dial("tcp", ln.Addr().String(), PinnedTLSConfig(Fingerprint(ca2.DER), "localhost"))
		if err == nil {
			t.Fatal("expected dial to fail with wrong pin")
		}
	})

	t.Run("different CA chain with matching pin fails", func(t *testing.T) {
		// Leaf signed by ca2, but chain includes ca1 so the pin matches a CA in the chain.
		leaf, err := ca2.IssueServerCert([]string{"127.0.0.1", "localhost"}, time.Hour)
		if err != nil {
			t.Fatalf("IssueServerCert: %v", err)
		}
		mismatched := tls.Certificate{
			Certificate: [][]byte{leaf.Certificate[0], ca1.DER},
			PrivateKey:  leaf.PrivateKey,
			Leaf:        leaf.Leaf,
		}
		ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
			Certificates: []tls.Certificate{mismatched},
			MinVersion:   tls.VersionTLS13,
			NextProtos:   []string{"h2"},
		})
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		defer ln.Close()
		serveOnce(t, ln)

		_, err = tls.Dial("tcp", ln.Addr().String(), PinnedTLSConfig(pin1, "localhost"))
		if err == nil {
			t.Fatal("expected dial to fail when leaf is not signed by pinned CA")
		}
	})
}

func serveOnce(t *testing.T, ln net.Listener) {
	t.Helper()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if tc, ok := conn.(*tls.Conn); ok {
			_ = tc.Handshake()
			_, _ = io.Copy(io.Discard, tc)
		}
	}()
}

func TestSignClientCSR_Rejects(t *testing.T) {
	dir := t.TempDir()
	ca, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateCA: %v", err)
	}

	t.Run("tampered signature", func(t *testing.T) {
		_, csrDER, err := NewKeyAndCSR()
		if err != nil {
			t.Fatalf("NewKeyAndCSR: %v", err)
		}
		tampered := append([]byte(nil), csrDER...)
		tampered[len(tampered)-1] ^= 0xff
		if _, _, err := ca.SignClientCSR(tampered, "host-1", time.Hour); err == nil {
			t.Fatal("expected error for tampered CSR")
		}
	})

	t.Run("rsa csr", func(t *testing.T) {
		rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
			Subject: pkix.Name{CommonName: "haetae-agent"},
		}, rsaKey)
		if err != nil {
			t.Fatalf("CreateCertificateRequest: %v", err)
		}
		if _, _, err := ca.SignClientCSR(csrDER, "host-1", time.Hour); err == nil {
			t.Fatal("expected error for RSA CSR")
		}
	})
}

func TestHostIDFromCert_NoURI(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "no-uri"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if _, err := HostIDFromCert(cert); err == nil {
		t.Fatal("expected error for cert without host URI SAN")
	}
}

func TestPEMHelpersRoundTrip(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "roundtrip"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}

	certPEM := EncodeCertPEM(der)
	gotDER, err := DecodeCertPEM(certPEM)
	if err != nil {
		t.Fatalf("DecodeCertPEM: %v", err)
	}
	if !bytes.Equal(gotDER, der) {
		t.Fatal("cert DER round-trip mismatch")
	}
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatal("EncodeCertPEM did not produce CERTIFICATE PEM")
	}

	keyPEM, err := EncodeKeyPEM(key)
	if err != nil {
		t.Fatalf("EncodeKeyPEM: %v", err)
	}
	gotKey, err := DecodeKeyPEM(keyPEM)
	if err != nil {
		t.Fatalf("DecodeKeyPEM: %v", err)
	}
	gotECDSA, ok := gotKey.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("decoded key type %T, want *ecdsa.PrivateKey", gotKey)
	}
	if gotECDSA.D.Cmp(key.D) != 0 {
		t.Fatal("key round-trip mismatch")
	}
	block, _ = pem.Decode(keyPEM)
	if block == nil || block.Type != "PRIVATE KEY" {
		t.Fatal("EncodeKeyPEM did not produce PRIVATE KEY PEM")
	}

	// DecodeCertPEM errors without CERTIFICATE block.
	if _, err := DecodeCertPEM([]byte("not pem")); err == nil {
		t.Fatal("expected DecodeCertPEM error")
	}
	_ = crypto.Signer(gotKey) // compile-time check
}
