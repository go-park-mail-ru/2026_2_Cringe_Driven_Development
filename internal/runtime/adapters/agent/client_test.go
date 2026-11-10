package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
)

type pki struct {
	caPEM []byte
	ca    *x509.Certificate
	key   *ecdsa.PrivateKey
}

func newCA(t *testing.T, name string) pki {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(der)
	return pki{caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), ca: ca, key: key}
}

func (p pki) issue(t *testing.T, eku x509.ExtKeyUsage, ip bool) (certPEM, keyPEM []byte) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "leaf"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{eku},
	}
	if ip {
		tpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, p.ca, &key.PublicKey, p.key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
}

type seen struct {
	mu     sync.Mutex
	auth   string
	peers  int
	method string
	path   string
	body   string
}

func startServer(t *testing.T, ca, clientCA pki, withIP bool, h http.HandlerFunc) (*httptest.Server, *seen) {
	t.Helper()
	s := &seen{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sb strings.Builder
		buf := make([]byte, 1024)
		for {
			n, err := r.Body.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		s.mu.Lock()
		s.auth, s.peers, s.method, s.path, s.body = r.Header.Get("Authorization"), len(r.TLS.PeerCertificates), r.Method, r.URL.RequestURI(), sb.String()
		s.mu.Unlock()
		h(w, r)
	}))
	cert, key := ca.issue(t, x509.ExtKeyUsageServerAuth, withIP)
	kp, err := tls.X509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(clientCA.caPEM)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{kp}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, s
}

func newClient(t *testing.T, ca pki) *Client { return newClientTrusting(t, ca, ca) }

// newClientTrusting: клиентский сертификат выдан certCA, корнем для проверки сервера служит trustCA.
func newClientTrusting(t *testing.T, trustCA, certCA pki) *Client {
	t.Helper()
	cc, ck := certCA.issue(t, x509.ExtKeyUsageClientAuth, false)
	c, err := New(trustCA.caPEM, cc, ck)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const tok = "secret-token-xyz"

func TestHealthSendsBearerAndClientCert(t *testing.T) {
	ca := newCA(t, "ca")
	srv, s := startServer(t, ca, ca, true, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"session_id":"s1","language":"python","state":"READY","ready":true}`))
	})
	h, status, err := newClient(t, ca).Health(context.Background(), usecase.AgentEndpoint{BaseURL: srv.URL, Token: tok})
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if h != (usecase.AgentHealth{SessionID: "s1", Language: "python", State: "READY", Ready: true}) {
		t.Fatalf("health: %+v", h)
	}
	if s.auth != "Bearer "+tok || s.peers != 1 {
		t.Fatalf("auth=%q peers=%d", s.auth, s.peers)
	}
}

func TestHealthRejectsForeignSession(t *testing.T) {
	ca := newCA(t, "ca")
	srv, _ := startServer(t, ca, ca, true, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"session_id":"other","language":"python","state":"DRAINING","ready":false}`))
	})
	h, status, err := newClient(t, ca).Health(context.Background(), usecase.AgentEndpoint{BaseURL: srv.URL, Token: tok})
	if err != nil || status != 503 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if h != (usecase.AgentHealth{SessionID: "other", Language: "python", State: "DRAINING"}) {
		t.Fatalf("health: %+v", h)
	}
}

func TestServerCertMustMatchCA(t *testing.T) {
	serverCA, otherCA := newCA(t, "server-ca"), newCA(t, "other-ca")
	// сервер принимает клиентский сертификат serverCA, но клиент доверяет только otherCA
	srv, _ := startServer(t, serverCA, serverCA, true, func(w http.ResponseWriter, r *http.Request) {})
	c := newClientTrusting(t, otherCA, serverCA)
	_, _, err := c.Health(context.Background(), usecase.AgentEndpoint{BaseURL: srv.URL, Token: tok})
	var ua x509.UnknownAuthorityError
	if !errors.As(err, &ua) {
		t.Fatalf("want UnknownAuthorityError, got %v", err)
	}
	if strings.Contains(err.Error(), tok) {
		t.Fatal("error leaks token")
	}
}

func TestServerCertWrongIPRejected(t *testing.T) {
	ca := newCA(t, "ca")
	srv, _ := startServer(t, ca, ca, false, func(w http.ResponseWriter, r *http.Request) {})
	_, _, err := newClient(t, ca).Health(context.Background(), usecase.AgentEndpoint{BaseURL: srv.URL, Token: tok})
	var he x509.HostnameError
	if !errors.As(err, &he) {
		t.Fatalf("want HostnameError, got %v", err)
	}
}

func TestDrainStopPost(t *testing.T) {
	ca := newCA(t, "ca")
	srv, s := startServer(t, ca, ca, true, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	c, ep := newClient(t, ca), usecase.AgentEndpoint{BaseURL: srv.URL, Token: tok}
	if err := c.Drain(context.Background(), ep); err != nil || s.method != "POST" || s.path != "/runtime/drain" {
		t.Fatalf("drain: %v %s %s", err, s.method, s.path)
	}
	if err := c.Stop(context.Background(), ep); err != nil || s.method != "POST" || s.path != "/runtime/stop" {
		t.Fatalf("stop: %v %s %s", err, s.method, s.path)
	}
}

func TestStopPendingIsError(t *testing.T) {
	ca := newCA(t, "ca")
	srv, _ := startServer(t, ca, ca, true, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	err := newClient(t, ca).Stop(context.Background(), usecase.AgentEndpoint{BaseURL: srv.URL, Token: tok})
	if err == nil || strings.Contains(err.Error(), tok) {
		t.Fatalf("err=%v", err)
	}
}

func TestExecuteExecutionEvents(t *testing.T) {
	ca := newCA(t, "ca")
	srv, s := startServer(t, ca, ca, true, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"execution_id":"e1","state":"QUEUED"}`))
		case strings.HasPrefix(r.URL.Path, "/executions/"):
			_, _ = w.Write([]byte(`{"execution_id":"e1","state":"SUCCEEDED"}`))
		default:
			_, _ = w.Write([]byte(`{"events":[{"seq":4},{"seq":5}]}`))
		}
	})
	c, ep := newClient(t, ca), usecase.AgentEndpoint{BaseURL: srv.URL, Token: tok}
	ctx := context.Background()
	if err := c.Execute(ctx, ep, "e1", "print(1)"); err != nil {
		t.Fatal(err)
	}
	if s.path != "/executions" || s.body != `{"code":"print(1)","execution_id":"e1"}` {
		t.Fatalf("%s %s", s.path, s.body)
	}
	if st, err := c.Execution(ctx, ep, "e1"); err != nil || st != "SUCCEEDED" {
		t.Fatalf("%q %v", st, err)
	}
	evs, err := c.Events(ctx, ep, 3)
	if err != nil || len(evs) != 2 || s.path != "/events?after=3" {
		t.Fatalf("%v %v %s", evs, err, s.path)
	}
}

func TestHealthCallTimeout(t *testing.T) {
	ca := newCA(t, "ca")
	release := make(chan struct{})
	srv, _ := startServer(t, ca, ca, true, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)
	c := newClient(t, ca)
	if c.healthTimeout != 10*time.Second {
		t.Fatalf("default health timeout = %s", c.healthTimeout)
	}
	c.healthTimeout = 50 * time.Millisecond
	start := time.Now()
	_, _, err := c.Health(context.Background(), usecase.AgentEndpoint{BaseURL: srv.URL, Token: tok})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("hung health call took %s", d)
	}
}

func TestTransportTimeouts(t *testing.T) {
	c := newClient(t, newCA(t, "ca"))
	tr, ok := c.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T", c.http.Transport)
	}
	if tr.Proxy != nil || tr.TLSHandshakeTimeout != 10*time.Second || tr.IdleConnTimeout != 30*time.Second ||
		tr.DialContext == nil || tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs == nil ||
		len(tr.TLSClientConfig.Certificates) != 1 {
		t.Fatalf("transport = %+v", tr)
	}
}
