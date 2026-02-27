package gatekeeper

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"smithly.dev/internal/db"
	"smithly.dev/internal/db/sqlite"
)

func testProxyStore(t *testing.T) *sqlite.Store {
	t.Helper()
	s, err := sqlite.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestProxyHTTPAllowed(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello from target"))
	}))
	defer target.Close()

	s := testProxyStore(t)
	ctx := context.Background()

	targetHost := extractDomain(strings.TrimPrefix(target.URL, "http://"))
	s.SetDomain(ctx, &db.DomainEntry{Domain: targetHost, Status: "allow", GrantedBy: "user"})

	gk := New(s, nil)
	proxy := NewProxy(gk, s, "127.0.0.1", 0)
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	proxyURL, _ := url.Parse(proxyServer.URL)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	client := &http.Client{Transport: transport}

	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from target" {
		t.Errorf("body = %q, want %q", string(body), "hello from target")
	}
}

func TestProxyHTTPDenied(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("target should not be reached")
	}))
	defer target.Close()

	s := testProxyStore(t)

	gk := New(s, nil)
	proxy := NewProxy(gk, s, "127.0.0.1", 0)
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	proxyURL, _ := url.Parse(proxyServer.URL)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	client := &http.Client{Transport: transport}

	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestProxyCONNECTAllowed(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Write([]byte("tunnel ok"))
			conn.Close()
		}
	}()

	s := testProxyStore(t)
	ctx := context.Background()
	s.SetDomain(ctx, &db.DomainEntry{Domain: "127.0.0.1", Status: "allow", GrantedBy: "user"})

	gk := New(s, nil)
	proxy := NewProxy(gk, s, "127.0.0.1", 0)
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	proxyAddr := strings.TrimPrefix(proxyServer.URL, "http://")
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	targetAddr := listener.Addr().String()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", targetAddr, targetAddr)

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("reading CONNECT response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200", resp.StatusCode)
	}

	data, _ := io.ReadAll(reader)
	if string(data) != "tunnel ok" {
		t.Errorf("tunnel data = %q, want %q", string(data), "tunnel ok")
	}
}

func TestProxyCONNECTDenied(t *testing.T) {
	s := testProxyStore(t)

	gk := New(s, nil)
	proxy := NewProxy(gk, s, "127.0.0.1", 0)
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	proxyAddr := strings.TrimPrefix(proxyServer.URL, "http://")
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT evil.com:443 HTTP/1.1\r\nHost: evil.com:443\r\n\r\n")

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("reading CONNECT response: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("CONNECT denied status = %d, want 403", resp.StatusCode)
	}
}

func TestProxyAuditLogging(t *testing.T) {
	s := testProxyStore(t)
	ctx := context.Background()

	s.SetDomain(ctx, &db.DomainEntry{Domain: "logged.com", Status: "allow", GrantedBy: "user"})

	gk := New(s, nil)
	proxy := NewProxy(gk, s, "127.0.0.1", 0)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("CONNECT", "logged.com:443", nil)
	req.Host = "logged.com:443"
	proxy.ServeHTTP(w, req)

	entries, err := s.GetAuditLog(ctx, db.AuditQuery{Domain: "logged.com", Limit: 10})
	if err != nil {
		t.Fatalf("GetAuditLog: %v", err)
	}
	if len(entries) == 0 {
		t.Error("expected audit entry for domain access")
	}
	if len(entries) > 0 && entries[0].Action != "domain_allow" {
		t.Errorf("action = %q, want domain_allow", entries[0].Action)
	}
}
