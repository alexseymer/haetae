// Package pki is the internal certificate authority for agent mTLS.
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

const (
	caCertFile    = "ca.crt"
	caKeyFile     = "ca.key"
	hostURIScheme = "haetae"
	hostURIPrefix = "haetae://host/"
)

// CA signs the gateway's server certificate and the agents' client certificates.
type CA struct {
	Cert *x509.Certificate
	Key  crypto.Signer
	DER  []byte // Cert in DER form
}

// LoadOrCreateCA loads dir/ca.crt and dir/ca.key (PEM). If both are missing it creates
// a new ECDSA P-256 CA (CommonName "Haetae Agent CA", valid 10 years, IsCA, BasicConstraintsValid,
// MaxPathLenZero, KeyUsageCertSign|KeyUsageCRLSign), writes ca.crt with mode 0644 and ca.key
// (PKCS#8 "PRIVATE KEY" PEM) with mode 0600, creating dir with 0700 if needed.
// If only one of the two files exists, return an error (never overwrite).
func LoadOrCreateCA(dir string) (*CA, error) {
	certPath := filepath.Join(dir, caCertFile)
	keyPath := filepath.Join(dir, caKeyFile)

	certExists, err := fileExists(certPath)
	if err != nil {
		return nil, fmt.Errorf("pki: check ca.crt: %w", err)
	}
	keyExists, err := fileExists(keyPath)
	if err != nil {
		return nil, fmt.Errorf("pki: check ca.key: %w", err)
	}

	switch {
	case certExists && keyExists:
		return loadCA(certPath, keyPath)
	case !certExists && !keyExists:
		return createCA(dir, certPath, keyPath)
	case certExists:
		return nil, fmt.Errorf("pki: ca.key missing while ca.crt exists")
	default:
		return nil, fmt.Errorf("pki: ca.crt missing while ca.key exists")
	}
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func loadCA(certPath, keyPath string) (*CA, error) {
	certPEM, err := os.ReadFile(certPath) //nolint:gosec // G304: path comes from the operator's data dir
	if err != nil {
		return nil, fmt.Errorf("pki: read ca.crt: %w", err)
	}
	certDER, err := DecodeCertPEM(certPEM)
	if err != nil {
		return nil, fmt.Errorf("pki: decode ca.crt: %w", err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("pki: parse ca.crt: %w", err)
	}

	keyPEM, err := os.ReadFile(keyPath) //nolint:gosec // G304: path comes from the operator's data dir
	if err != nil {
		return nil, fmt.Errorf("pki: read ca.key: %w", err)
	}
	key, err := DecodeKeyPEM(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("pki: decode ca.key: %w", err)
	}

	return &CA{Cert: cert, Key: key, DER: certDER}, nil
}

func createCA(dir, certPath, keyPath string) (*CA, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("pki: create ca dir: %w", err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pki: generate ca key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, fmt.Errorf("pki: generate ca serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Haetae Agent CA"},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("pki: create ca certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("pki: parse ca certificate: %w", err)
	}

	certPEM := EncodeCertPEM(der)
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil { //nolint:gosec // G306: the CA certificate is public
		return nil, fmt.Errorf("pki: write ca.crt: %w", err)
	}

	keyPEM, err := EncodeKeyPEM(key)
	if err != nil {
		return nil, fmt.Errorf("pki: encode ca.key: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("pki: write ca.key: %w", err)
	}

	return &CA{Cert: cert, Key: key, DER: der}, nil
}

// IssueServerCert returns a TLS certificate for the gateway, valid for ttl, with the given
// names as SANs (entries that parse as IPs become IPAddresses, others DNSNames).
// ExtKeyUsageServerAuth. The returned tls.Certificate.Certificate chain is [leaf DER, CA DER]
// so clients can pin the CA. New ECDSA P-256 key each call. Leaf is set.
func (ca *CA) IssueServerCert(names []string, ttl time.Duration) (tls.Certificate, error) {
	if ca == nil || ca.Cert == nil || ca.Key == nil {
		return tls.Certificate{}, fmt.Errorf("pki: nil CA")
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("pki: generate server key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("pki: generate server serial: %w", err)
	}

	var dnsNames []string
	var ipAddrs []net.IP
	for _, name := range names {
		if ip := net.ParseIP(name); ip != nil {
			ipAddrs = append(ipAddrs, ip)
		} else {
			dnsNames = append(dnsNames, name)
		}
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "haetae-gateway"},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(ttl),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  ipAddrs,
	}

	leafDER, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("pki: create server certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("pki: parse server certificate: %w", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{leafDER, ca.DER},
		PrivateKey:  key,
		Leaf:        leaf,
	}, nil
}

// SignClientCSR verifies the CSR's signature and issues a client certificate for hostID.
// The CSR subject is ignored: Subject CommonName = hostID, one URI SAN "haetae://host/<hostID>",
// ExtKeyUsageClientAuth, KeyUsageDigitalSignature, random 128-bit serial, NotBefore = now-5m,
// NotAfter = now+ttl. Returns the certificate DER and the serial as lowercase hex.
// Reject CSRs whose public key is not ECDSA P-256 or Ed25519.
func (ca *CA) SignClientCSR(csrDER []byte, hostID string, ttl time.Duration) (certDER []byte, serial string, err error) {
	if ca == nil || ca.Cert == nil || ca.Key == nil {
		return nil, "", fmt.Errorf("pki: nil CA")
	}

	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, "", fmt.Errorf("pki: parse csr: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, "", fmt.Errorf("pki: verify csr signature: %w", err)
	}
	if err := checkAllowedPublicKey(csr.PublicKey); err != nil {
		return nil, "", err
	}

	uri, err := url.Parse(hostURIPrefix + hostID)
	if err != nil {
		return nil, "", fmt.Errorf("pki: host uri: %w", err)
	}

	serialNum, err := randomSerial()
	if err != nil {
		return nil, "", fmt.Errorf("pki: generate client serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serialNum,
		Subject:      pkix.Name{CommonName: hostID},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(ttl),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{uri},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, csr.PublicKey, ca.Key)
	if err != nil {
		return nil, "", fmt.Errorf("pki: create client certificate: %w", err)
	}

	return der, serialNum.Text(16), nil
}

func checkAllowedPublicKey(pub any) error {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		if k.Curve != elliptic.P256() {
			return fmt.Errorf("pki: unsupported ecdsa curve")
		}
		return nil
	case ed25519.PublicKey:
		return nil
	default:
		return fmt.Errorf("pki: unsupported public key type %T", pub)
	}
}

// ServerTLSConfig returns a tls.Config for the gateway: the given server certificate,
// MinVersion TLS 1.3, NextProtos ["h2"], ClientCAs = pool with ca.Cert,
// ClientAuth = tls.VerifyClientCertIfGiven (Enroll runs without a client cert).
func (ca *CA) ServerTLSConfig(serverCert tls.Certificate) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	return &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"h2"},
		ClientCAs:    pool,
		ClientAuth:   tls.VerifyClientCertIfGiven,
	}
}

// HostIDFromCert returns <id> from the URI SAN haetae://host/<id>. Error if absent or more than one.
func HostIDFromCert(cert *x509.Certificate) (string, error) {
	if cert == nil {
		return "", fmt.Errorf("pki: nil certificate")
	}
	var id string
	for _, u := range cert.URIs {
		if u == nil {
			continue
		}
		if u.Scheme != hostURIScheme || u.Host != "host" {
			continue
		}
		// haetae://host/<id> → Host="host", Path="/<id>" or Opaque depending on parsing.
		pathID := ""
		if u.Path != "" {
			pathID = u.Path
			if len(pathID) > 0 && pathID[0] == '/' {
				pathID = pathID[1:]
			}
		} else if u.Opaque != "" {
			// Rare for this form; keep defensive.
			pathID = u.Opaque
		}
		if pathID == "" {
			continue
		}
		if id != "" {
			return "", fmt.Errorf("pki: multiple host URI SANs")
		}
		id = pathID
	}
	if id == "" {
		return "", fmt.Errorf("pki: host URI SAN absent")
	}
	return id, nil
}

// Fingerprint returns "sha256:" + lowercase hex SHA-256 of der.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// NewKeyAndCSR creates an ECDSA P-256 key and a CSR (CommonName "haetae-agent") for it. Agent side.
func NewKeyAndCSR() (*ecdsa.PrivateKey, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: generate agent key: %w", err)
	}
	tmpl := &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "haetae-agent"},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: create csr: %w", err)
	}
	return key, csrDER, nil
}

// PinnedTLSConfig is for the agent's first contact (Enroll), before it has the CA.
// It sets InsecureSkipVerify=true and a VerifyConnection callback that: requires at least two
// certs in the peer chain; finds a cert in the chain whose Fingerprint equals caPin (constant-time
// compare) and that is a CA (IsCA); verifies the leaf against a pool containing only that CA with
// DNSName = serverName (if serverName parses as an IP, it must match an IP SAN) and
// ExtKeyUsageServerAuth. MinVersion TLS 1.3, NextProtos ["h2"].
func PinnedTLSConfig(caPin, serverName string) *tls.Config {
	return &tls.Config{
		// Chain verification is replaced, not skipped: VerifyConnection pins the CA.
		InsecureSkipVerify: true, //nolint:gosec // G402: see VerifyConnection
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{"h2"},
		ServerName:         serverName,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) < 2 {
				return fmt.Errorf("pki: peer chain too short")
			}
			var pinned *x509.Certificate
			for _, cert := range cs.PeerCertificates {
				fp := Fingerprint(cert.Raw)
				if subtle.ConstantTimeCompare([]byte(fp), []byte(caPin)) == 1 && cert.IsCA {
					pinned = cert
					break
				}
			}
			if pinned == nil {
				return fmt.Errorf("pki: pinned CA not found in peer chain")
			}

			pool := x509.NewCertPool()
			pool.AddCert(pinned)
			leaf := cs.PeerCertificates[0]
			if _, err := leaf.Verify(x509.VerifyOptions{
				DNSName:   serverName,
				Roots:     pool,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			}); err != nil {
				return fmt.Errorf("pki: verify leaf: %w", err)
			}
			return nil
		},
	}
}

// ClientTLSConfig is for normal operation: RootCAs = pool with caDER, the client certificate
// (certDER + key), ServerName = serverName, MinVersion TLS 1.3, NextProtos ["h2"].
func ClientTLSConfig(caDER, certDER []byte, key crypto.Signer, serverName string) (*tls.Config, error) {
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, fmt.Errorf("pki: parse ca der: %w", err)
	}
	leaf, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("pki: parse client der: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	return &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{certDER},
			PrivateKey:  key,
			Leaf:        leaf,
		}},
		RootCAs:    pool,
		ServerName: serverName,
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{"h2"},
	}, nil
}

// EncodeCertPEM encodes der as a CERTIFICATE PEM block.
func EncodeCertPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// DecodeCertPEM returns DER; error if no CERTIFICATE block.
func DecodeCertPEM(b []byte) ([]byte, error) {
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("pki: no CERTIFICATE PEM block")
	}
	return block.Bytes, nil
}

// EncodeKeyPEM encodes key as PKCS#8 "PRIVATE KEY" PEM.
func EncodeKeyPEM(key crypto.Signer) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("pki: marshal private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// DecodeKeyPEM decodes a PKCS#8 "PRIVATE KEY" PEM into a crypto.Signer.
func DecodeKeyPEM(b []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("pki: no PRIVATE KEY PEM block")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("pki: parse private key: %w", err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("pki: private key is not a signer")
	}
	return signer, nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, err
	}
	// Ensure non-zero serial.
	if serial.Sign() == 0 {
		serial = big.NewInt(1)
	}
	return serial, nil
}
