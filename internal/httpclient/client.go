// Package httpclient constructs a shared *http.Client for the forge
// clients. Keeping the construction in one place means transport
// tuning (timeouts, keepalive, proxy handling) is uniform across
// GitHub, GitLab, and Forgejo/Gitea.
package httpclient

import (
	"net"
	"net/http"
	"time"
)

// New returns an *http.Client with the given total request timeout
// and reasonable transport defaults. The transport respects
// HTTP_PROXY / HTTPS_PROXY / NO_PROXY from the environment so a CLI
// run inside a corporate network just works.
//
// Pool sizing (MaxIdleConns=100, IdleConnTimeout=90s) is tuned for
// bursty per-repo fan-out: a single scan touches dozens of repos,
// each issuing a handful of requests, so a small pool with a healthy
// keepalive window gives good throughput without holding sockets open
// pointlessly.
func New(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}
