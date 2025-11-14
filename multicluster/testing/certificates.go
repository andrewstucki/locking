package testing

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"
)

type CACertificate struct {
	cert *x509.Certificate
	pk   *ecdsa.PrivateKey

	pem []byte
}

func (c CACertificate) Bytes() []byte {
	return c.pem
}

func (c CACertificate) Sign(t *testing.T, names ...string) Certificate {
	if len(names) == 0 {
		t.Fatalf("must specify at least one name")
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}

	ips := []net.IP{}
	dns := []string{}
	for _, name := range names {
		if ip := net.ParseIP(name); ip != nil {
			ips = append(ips, ip)
		} else {
			dns = append(dns, name)
		}
	}

	serialNumber, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   names[0],
			Organization: []string{"test"},
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(365 * 24 * time.Hour),

		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              dns,
		IPAddresses:           ips,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &tmpl, c.cert, &priv.PublicKey, c.pk)
	if err != nil {
		t.Fatalf("generate server cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("generate marshaling cert: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	return Certificate{
		privateKey:  keyPEM,
		certificate: certPEM,
	}
}

func (c CACertificate) SignCA(t *testing.T, domain string) Certificate {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate intermediate key: %v", err)
	}

	serialNumber, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   domain,
			Organization: []string{"test"},
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(365 * 24 * time.Hour),

		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &tmpl, c.cert, &priv.PublicKey, c.pk)
	if err != nil {
		t.Fatalf("generate intermediate cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("generate marshaling cert: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	return Certificate{
		privateKey:  keyPEM,
		certificate: certPEM,
	}
}

type Certificate struct {
	privateKey  []byte
	certificate []byte
}

func (c Certificate) PrivateKeyBytes() []byte {
	return c.privateKey
}

func (c Certificate) Bytes() []byte {
	return c.certificate
}

func GenerateCA(t *testing.T) CACertificate {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("error generating CA: %v", err)
	}

	serialNumber, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	caTemplate := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   "test",
			Organization: []string{"test"},
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(10 * 365 * 24 * time.Hour),

		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &caTemplate, &caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("error creating CA certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	caCert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		t.Fatalf("error parsing CA: %v", err)
	}

	return CACertificate{
		cert: caCert,
		pk:   caKey,
		pem:  certPEM,
	}
}
