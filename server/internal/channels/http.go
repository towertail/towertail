package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"
)

// sendTimeout is the time limit of one attempt.
const sendTimeout = 5 * time.Second

// sendError is a failed send. Retry is false when a new attempt cannot
// help (a 4xx reply other than 408 and 429).
type sendError struct {
	err   error
	retry bool
}

func (e *sendError) Error() string { return e.err.Error() }
func (e *sendError) Unwrap() error { return e.err }

// retryable tells if a new attempt can help. A network error can.
func retryable(err error) bool {
	var se *sendError
	if errors.As(err, &se) {
		return se.retry
	}
	return !errors.Is(err, errBlockedTarget)
}

var errBlockedTarget = errors.New("target address is not allowed")

// cgnat is 100.64.0.0/10 (Tailscale and carrier NAT).
var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func blockedIP(ip net.IP) bool {
	return ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || cgnat.Contains(ip)
}

// poster sends JSON to the webhook targets.
type poster struct{ hc *http.Client }

// newPoster makes the HTTP client. With block, the dialer refuses
// private addresses after the DNS lookup, so a DNS change cannot get
// around the check. There is no proxy.
func newPoster(block bool) *poster {
	d := &net.Dialer{Timeout: sendTimeout}
	if block {
		d.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if blockedIP(net.ParseIP(host)) {
				return fmt.Errorf("%w: %s", errBlockedTarget, host)
			}
			return nil
		}
	}
	return &poster{hc: &http.Client{
		Timeout: sendTimeout,
		Transport: &http.Transport{
			DialContext:         d.DialContext,
			TLSHandshakeTimeout: sendTimeout,
			MaxIdleConns:        16,
			IdleConnTimeout:     90 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// post sends body as JSON. headers can be nil.
func (p *poster) post(ctx context.Context, url string, body any, headers map[string]string) error {
	data, err := json.Marshal(body)
	if err != nil {
		return &sendError{err: err}
	}
	return p.postRaw(ctx, url, data, headers)
}

func (p *poster) postRaw(ctx context.Context, url string, data []byte, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return &sendError{err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "towertail-server")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	retry := resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout
	return &sendError{err: fmt.Errorf("%s: %s", resp.Status, bytes.TrimSpace(msg)), retry: retry}
}
