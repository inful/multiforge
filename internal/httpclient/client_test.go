package httpclient

import (
	"net/http"
	"testing"
	"time"
)

func TestNew_RespectsTimeout(t *testing.T) {
	c := New(1234 * time.Millisecond)
	if c.Timeout != 1234*time.Millisecond {
		t.Errorf("Timeout = %v, want 1234ms", c.Timeout)
	}
}

func TestNew_DefaultTransport(t *testing.T) {
	c := New(30 * time.Second)
	if c.Transport == nil {
		t.Fatal("Transport is nil; New must always wire a transport so HTTP_PROXY is honoured")
	}
}

func TestNew_ReturnsDistinctClients(t *testing.T) {
	// Each call must return a fresh *http.Client (and a fresh
	// transport) — we don't want callers sharing a single
	// Transport because per-call Timeout/Proxy overrides would
	// race.
	a := New(30 * time.Second)
	b := New(30 * time.Second)
	if a == b {
		t.Error("New returned the same pointer; each call must allocate")
	}
	if a.Transport == b.Transport {
		t.Error("New shared a Transport between calls; each must allocate")
	}
}

// TestNew_TransportIsHTTPTransport pins the concrete transport
// type so future refactors don't silently substitute one without
// going through the same proxy / keepalive wiring.
func TestNew_TransportIsHTTPTransport(t *testing.T) {
	c := New(30 * time.Second)
	if _, ok := c.Transport.(*http.Transport); !ok {
		t.Errorf("Transport type = %T, want *http.Transport", c.Transport)
	}
}
