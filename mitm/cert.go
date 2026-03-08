package mitm

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"time"
)

const (
	day         = 24 * time.Hour
	doubleWeeks = day * 14
	month       = 1
	year        = 1
)

// GenerateCA rt
func (m *MITM) GenerateCA() error {
	var err error
	if m.pk, err = loadPKFromFile(m.TLSConf.PrivateKeyFile); err != nil {
		m.pk, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return fmt.Errorf("Unable to generate private key: %s", err)
		}
		writePKToFile(m.pk, m.TLSConf.PrivateKeyFile)
	}
	m.pkPem = pemEncodePrivateKey(m.pk)
	m.issuingCert, err = loadCertificateFromFile(m.TLSConf.CertFile)
	if err != nil || m.issuingCert.NotAfter.Before(time.Now().AddDate(0, month, 0)) {
		m.issuingCert, err = createCertificate(
			m.pk,
			time.Now().AddDate(year, 0, 0),
			true,
			nil,
			m.TLSConf.Organization,
			m.TLSConf.CommonName,
		)
		if err != nil {
			return fmt.Errorf("Unable to generate self-signed issuing certificate: %s", err)
		}
		writeCertToFile(m.issuingCert, m.TLSConf.CertFile)
	}
	return nil
}

// FakeCert rt
func (m *MITM) FakeCert(domain string) (*tls.Certificate, error) {
	cert, has := m.cache.Get("DC" + domain)
	if has {
		return cert.(*tls.Certificate), nil
	}

	//create certificate
	certTTL := doubleWeeks
	generatedCert, err := createCertificate(
		m.pk,
		time.Now().Add(certTTL),
		false,
		m.issuingCert,
		m.TLSConf.Organization,
		domain)
	if err != nil {
		return nil, fmt.Errorf("Unable to issue certificate: %s", err)
	}
	keyPair, err := tls.X509KeyPair(pemEncodeCertificate(generatedCert), m.pkPem)
	if err != nil {
		return nil, fmt.Errorf("Unable to parse keypair for tls: %s", err)
	}

	m.cache.Set("DC"+domain, &keyPair, time.Hour*48)
	return &keyPair, nil
}

// loadPKFromFile 从PEM文件加载RSA私钥
func loadPKFromFile(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block from %s", path)
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

// writePKToFile 将RSA私钥以PEM格式写入文件
func writePKToFile(pk *rsa.PrivateKey, path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(pk),
	})
}

// pemEncodePrivateKey 返回RSA私钥的PEM编码字节
func pemEncodePrivateKey(pk *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(pk),
	})
}

// loadCertificateFromFile 从PEM文件加载X.509证书
func loadCertificateFromFile(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block from %s", path)
	}
	return x509.ParseCertificate(block.Bytes)
}

// writeCertToFile 将X.509证书以PEM格式写入文件
func writeCertToFile(cert *x509.Certificate, path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	})
}

// pemEncodeCertificate 返回X.509证书的PEM编码字节
func pemEncodeCertificate(cert *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	})
}

// createCertificate 创建X.509证书（CA自签名或由issuer签发的域名证书）
func createCertificate(pk *rsa.PrivateKey, expiry time.Time, isCA bool, issuer *x509.Certificate, org string, cn string) (*x509.Certificate, error) {
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("failed to generate serial number: %s", err)
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{org},
			CommonName:   cn,
		},
		NotBefore: time.Now(),
		NotAfter:  expiry,
	}

	if isCA {
		template.BasicConstraintsValid = true
		template.IsCA = true
		template.KeyUsage = x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign
	} else {
		template.DNSNames = []string{cn}
		template.KeyUsage = x509.KeyUsageDigitalSignature
	}

	// 自签名时 parent 为自身，否则使用 issuer 作为 parent
	parent := template
	if issuer != nil {
		parent = issuer
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, parent, &pk.PublicKey, pk)
	if err != nil {
		return nil, fmt.Errorf("failed to create certificate: %s", err)
	}

	return x509.ParseCertificate(certDER)
}
