package server

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/semihalev/log"
	"github.com/semihalev/sdns/config"
	"github.com/semihalev/sdns/middleware/blocklist"
	"github.com/semihalev/sdns/mock"
	"github.com/stretchr/testify/assert"
)

func TestMain(m *testing.M) {
	log.Root().SetHandler(log.LvlFilterHandler(0, log.StdoutHandler))
	m.Run()
}

func publicKey(priv interface{}) interface{} {
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		return &k.PublicKey
	case *ecdsa.PrivateKey:
		return &k.PublicKey
	default:
		return nil
	}
}

func pemBlockForKey(priv interface{}) *pem.Block {
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		return &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}
	case *ecdsa.PrivateKey:
		b, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Unable to marshal ECDSA private key: %v", err)
			os.Exit(2)
		}
		return &pem.Block{Type: "EC PRIVATE KEY", Bytes: b}
	default:
		return nil
	}
}

func generateCertificate() error {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Acme Co"},
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(time.Hour * 24 * 365 * 3),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	template.DNSNames = append(template.DNSNames, "localhost")

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, publicKey(priv), priv)
	if err != nil {
		return err
	}

	certOut, err := os.OpenFile(filepath.Join(os.TempDir(), "test.cert"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	if err != nil {
		return err
	}
	certOut.Close()

	keyOut, err := os.OpenFile(filepath.Join(os.TempDir(), "test.key"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}

	pem.Encode(keyOut, pemBlockForKey(priv))
	keyOut.Close()

	return nil
}

func Test_logPipe(t *testing.T) {
	logReader, logWriter := io.Pipe()
	go readlogs(logReader)
	logWriter.Write([]byte("test test test test test test\n"))
}

func Test_ServerNoBind(t *testing.T) {
	cfg := &config.Config{}

	s := New(cfg)
	s.Run()
}

func Test_ServerBindFail(t *testing.T) {
	cfg := &config.Config{}

	cfg.TLSCertificate = "cert"
	cfg.TLSPrivateKey = "key"
	cfg.LogLevel = "crit"
	cfg.Bind = "1:1"
	cfg.BindTLS = "1:2"
	cfg.BindDOH = "1:3"

	s := New(cfg)
	s.Run()
}

func Test_Server(t *testing.T) {
	cfg := &config.Config{}
	err := generateCertificate()
	assert.NoError(t, err)

	cert := filepath.Join(os.TempDir(), "test.cert")
	privkey := filepath.Join(os.TempDir(), "test.key")

	cfg.TLSCertificate = cert
	cfg.TLSPrivateKey = privkey
	cfg.LogLevel = "crit"
	cfg.Bind = "127.0.0.1:0"
	cfg.BindTLS = "127.0.0.1:23222"
	cfg.BindDOH = "127.0.0.1:23223"

	s := New(cfg)
	s.Run()

	blocklist := blocklist.New(cfg)
	s.Register(blocklist)

	blocklist.Set("test.com.")

	req := new(dns.Msg)
	req.SetQuestion("test.com.", dns.TypeA)

	mw := mock.NewWriter("udp", "127.0.0.1")
	s.ServeDNS(mw, req)

	assert.Equal(t, true, len(mw.Msg().Answer) > 0)

	request, err := http.NewRequest("GET", "/dns-query?name=test.com", nil)
	assert.NoError(t, err)

	hw := httptest.NewRecorder()

	s.ServeHTTP(hw, request)
	assert.Equal(t, 200, hw.Code)

	time.Sleep(2 * time.Second)

	os.Remove(cert)
	os.Remove(privkey)
}