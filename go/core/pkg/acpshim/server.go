package acpshim

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WebSocket close codes (4000-4999 is the application-defined range) used so
// the bridge can distinguish why the stream ended.
const (
	// CloseChildExited signals the child agent exited cleanly (status 0).
	CloseChildExited = 4000
	// CloseChildFailed signals the child agent exited with an error.
	CloseChildFailed = 4001
)

// Server is the shim's WebSocket server. It accepts at most one client at a
// time (the kagent bridge owns the stream) and pumps frames between the
// client and the child agent's stdio.
type Server struct {
	cfg      *Config
	upgrader websocket.Upgrader
	httpSrv  *http.Server

	mu         sync.Mutex
	child      *child
	connBusy   bool
	graceTimer *time.Timer
}

// NewServer creates a Server from a validated Config.
func NewServer(cfg *Config) *Server {
	s := &Server{
		cfg: cfg,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  64 * 1024,
			WriteBufferSize: 64 * 1024,
			// Browser clients (e.g. the kagent UI) connect cross-origin. The
			// shim authenticates with an explicit bearer token rather than
			// ambient cookie credentials, so Origin checks add no protection.
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	// Alias used by infrastructure probes (e.g. kagent's substrate actor
	// reachability check hits /health through atenet-router).
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/acp", s.handleACP)
	if cfg.PassthroughURL != "" {
		mux.Handle("/", newPassthroughProxy(cfg.PassthroughURL))
	}
	s.httpSrv = &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// ListenAndServe runs the server until Shutdown is called.
func (s *Server) ListenAndServe() error {
	l, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.cfg.ListenAddr, err)
	}
	return s.Serve(l)
}

// Serve runs the server on the given listener (used by tests to bind an
// ephemeral port).
func (s *Server) Serve(l net.Listener) error {
	log.Printf("acp-shim: listening on %s (policy=%s)", l.Addr(), s.cfg.Policy)
	err := s.httpSrv.Serve(l)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Shutdown stops the HTTP server and terminates any running child.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.httpSrv.Shutdown(ctx)
	s.mu.Lock()
	c := s.child
	s.child = nil
	if s.graceTimer != nil {
		s.graceTimer.Stop()
		s.graceTimer = nil
	}
	s.mu.Unlock()
	if c != nil {
		c.terminate(s.cfg.GracePeriod)
	}
	return err
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// newPassthroughProxy forwards requests the shim does not handle itself to a
// sandbox-local HTTP server (validated by Config.Validate). It preserves
// paths and supports WebSocket upgrades via httputil.ReverseProxy.
func newPassthroughProxy(rawURL string) http.Handler {
	target, err := url.Parse(rawURL)
	if err != nil {
		panic(fmt.Sprintf("acpshim: passthrough URL not validated: %v", err))
	}
	proxy := &httputil.ReverseProxy{
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, proxyErr error) {
			log.Printf("acp-shim: passthrough error for %s: %v", r.URL.Path, proxyErr)
			http.Error(w, "passthrough upstream unavailable", http.StatusBadGateway)
		},
	}
	return proxy
}

// authorized checks the bearer token on the WebSocket handshake. The token
// may arrive in the Authorization header or, for clients that cannot set
// headers, the access_token query parameter.
func (s *Server) authorized(r *http.Request) bool {
	if s.cfg.Token == "" {
		return true // auth disabled (prototype/testing only)
	}
	presented := ""
	if h := r.Header.Get("Authorization"); len(h) > 7 && h[:7] == "Bearer " {
		presented = h[7:]
	} else if q := r.URL.Query().Get("access_token"); q != "" {
		presented = q
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(s.cfg.Token)) == 1
}

func (s *Server) handleACP(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Exactly-one-client: the kagent bridge owns this stream. Reject
	// concurrent connections instead of multiplexing.
	s.mu.Lock()
	if s.connBusy {
		s.mu.Unlock()
		http.Error(w, "another client is connected", http.StatusConflict)
		return
	}
	s.connBusy = true
	if s.graceTimer != nil {
		s.graceTimer.Stop()
		s.graceTimer = nil
	}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.connBusy = false
		s.mu.Unlock()
	}()

	c, err := s.acquireChild()
	if err != nil {
		log.Printf("acp-shim: failed to start child: %v", err)
		http.Error(w, "failed to start agent", http.StatusBadGateway)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("acp-shim: websocket upgrade failed: %v", err)
		s.releaseChild(c)
		return
	}
	log.Printf("acp-shim: client connected from %s", r.RemoteAddr)

	s.pump(conn, c)

	_ = conn.Close()
	s.releaseChild(c)
	log.Printf("acp-shim: client %s disconnected", r.RemoteAddr)
}

// acquireChild returns the child process for a new connection, starting one
// according to the configured policy.
func (s *Server) acquireChild() (*child, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cfg.Policy == ChildPolicyLongLived && s.child != nil && !s.child.exited() {
		return s.child, nil
	}
	c, err := startChild(s.cfg)
	if err != nil {
		return nil, err
	}
	s.child = c
	return c, nil
}

// releaseChild handles child lifecycle when a connection ends: terminate for
// per-connection policy, or arm the reconnect grace timer for long-lived.
func (s *Server) releaseChild(c *child) {
	if s.cfg.Policy == ChildPolicyPerConnection {
		s.mu.Lock()
		if s.child == c {
			s.child = nil
		}
		s.mu.Unlock()
		c.terminate(s.cfg.GracePeriod)
		return
	}
	// Long-lived: keep the child alive so the next connection can resume
	// its sessions, unless a reconnect grace window is configured.
	if s.cfg.ReconnectGrace <= 0 || c.exited() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.graceTimer != nil {
		s.graceTimer.Stop()
	}
	s.graceTimer = time.AfterFunc(s.cfg.ReconnectGrace, func() {
		s.mu.Lock()
		busy := s.connBusy
		if !busy && s.child == c {
			s.child = nil
		}
		s.mu.Unlock()
		if !busy {
			log.Printf("acp-shim: reconnect grace expired, terminating child")
			c.terminate(s.cfg.GracePeriod)
		}
	})
}

// pump moves frames between the WebSocket and the child's stdio until either
// side ends. One WebSocket text frame corresponds to one newline-delimited
// JSON-RPC line; the shim never parses the payload.
func (s *Server) pump(conn *websocket.Conn, c *child) {
	readerDone := make(chan struct{})

	// WebSocket → child stdin.
	go func() {
		defer close(readerDone)
		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
				continue
			}
			if err := c.writeLine(data); err != nil {
				log.Printf("acp-shim: %v", err)
				return
			}
		}
	}()

	// Child stdout → WebSocket.
	for {
		select {
		case line, ok := <-c.out:
			if !ok {
				// Child exited: tell the client why with a distinguishable
				// close code so the bridge can decide whether to restart.
				code := CloseChildExited
				reason := "agent exited"
				if err := c.exitError(); err != nil {
					code = CloseChildFailed
					reason = fmt.Sprintf("agent exited: %v", err)
				}
				msg := websocket.FormatCloseMessage(code, reason)
				_ = conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(5*time.Second))
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, line); err != nil {
				log.Printf("acp-shim: websocket write failed: %v", err)
				return
			}
		case <-readerDone:
			return
		}
	}
}
