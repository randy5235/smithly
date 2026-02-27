package gatekeeper

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"

	"smithly.dev/internal/db"
)

// Proxy is an HTTP proxy that enforces domain-level access control via the Gatekeeper.
// It handles both HTTP CONNECT (HTTPS tunneling) and plain HTTP requests.
type Proxy struct {
	gk     *Gatekeeper
	store  db.Store
	bind   string
	port   int
	server *http.Server
}

// NewProxy creates a new gatekeeper proxy.
func NewProxy(gk *Gatekeeper, store db.Store, bind string, port int) *Proxy {
	return &Proxy{
		gk:    gk,
		store: store,
		bind:  bind,
		port:  port,
	}
}

// Addr returns the proxy's listen address.
func (p *Proxy) Addr() string {
	return net.JoinHostPort(p.bind, fmt.Sprintf("%d", p.port))
}

// Start begins listening. Blocks until shut down.
func (p *Proxy) Start() error {
	p.server = &http.Server{
		Addr:    p.Addr(),
		Handler: p,
	}
	err := p.server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Shutdown gracefully stops the proxy.
func (p *Proxy) Shutdown(ctx context.Context) error {
	if p.server == nil {
		return nil
	}
	return p.server.Shutdown(ctx)
}

// ServeHTTP handles both CONNECT tunnels and plain HTTP proxy requests.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
	} else {
		p.handleHTTP(w, r)
	}
}

// handleConnect handles HTTPS tunneling via HTTP CONNECT.
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	domain := extractDomain(r.Host)
	ctx := r.Context()

	if !p.gk.CheckDomain(ctx, domain) {
		p.logAccess(ctx, domain, "deny")
		http.Error(w, "domain denied by gatekeeper", http.StatusForbidden)
		return
	}
	p.logAccess(ctx, domain, "allow")

	// Hijack the connection to establish the tunnel
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}

	targetConn, err := net.Dial("tcp", r.Host)
	if err != nil {
		http.Error(w, fmt.Sprintf("connect to %s: %v", r.Host, err), http.StatusBadGateway)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		targetConn.Close()
		return
	}

	// Send 200 Connection Established
	clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	// Bidirectional copy
	go func() {
		io.Copy(targetConn, clientConn)
		targetConn.Close()
	}()
	go func() {
		io.Copy(clientConn, targetConn)
		clientConn.Close()
	}()
}

// handleHTTP proxies plain HTTP requests.
func (p *Proxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Host == "" {
		http.Error(w, "missing host in proxy request", http.StatusBadRequest)
		return
	}

	domain := extractDomain(r.URL.Host)
	ctx := r.Context()

	if !p.gk.CheckDomain(ctx, domain) {
		p.logAccess(ctx, domain, "deny")
		http.Error(w, "domain denied by gatekeeper", http.StatusForbidden)
		return
	}
	p.logAccess(ctx, domain, "allow")

	// Forward the request
	outReq, err := http.NewRequestWithContext(ctx, r.Method, r.URL.String(), r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	outReq.Header = r.Header.Clone()
	// Remove hop-by-hop headers
	outReq.Header.Del("Proxy-Connection")
	outReq.Header.Del("Proxy-Authorization")

	resp, err := http.DefaultTransport.RoundTrip(outReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (p *Proxy) logAccess(ctx context.Context, domain, status string) {
	if p.store == nil {
		return
	}
	if err := p.store.LogAudit(ctx, &db.AuditEntry{
		Actor:      "gatekeeper",
		Action:     "domain_" + status,
		Target:     domain,
		TrustLevel: "system",
		Domain:     domain,
	}); err != nil {
		log.Printf("gatekeeper audit: %v", err)
	}
}

// extractDomain pulls the hostname from a host:port string, lowercased.
func extractDomain(hostport string) string {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	return strings.ToLower(host)
}
