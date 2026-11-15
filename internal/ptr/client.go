package ptr

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monbooru/monloader/internal/config"
)

type client struct {
	// keyMu guards addr, accessKey and sleep: a save swaps the key and the
	// sleep under a running sync, and the address moves while the sync is off.
	keyMu     sync.RWMutex
	addr      string
	accessKey string
	hc        *http.Client
	tlsErr    error
	// Paces update fetches to stay polite to the donated repository server.
	sleep time.Duration
}

type retryable struct{ err error }

func (r *retryable) Error() string { return r.err.Error() }
func (r *retryable) Unwrap() error { return r.err }

func IsRetryable(err error) bool {
	var r *retryable
	if errors.As(err, &r) {
		return true
	}
	var s *ServerError
	return errors.As(err, &s) && s.Retryable()
}

func newClient(cfg config.PTRConfig) *client {
	tlsConfig, tlsErr := repositoryTLS(cfg.TLSCAFile)
	return &client{
		addr:      cfg.Address,
		accessKey: cfg.EffectiveAccessKey(),
		sleep:     time.Duration(cfg.FetchSleep * float64(time.Second)),
		tlsErr:    tlsErr,
		hc: &http.Client{
			Timeout: 60 * time.Second,
			// Go strips only Authorization and cookies on a redirect, and the
			// Hydrus-Key is the account's only credential.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport:     &http.Transport{TLSClientConfig: tlsConfig},
		},
	}
}

func repositoryTLS(caFile string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile == "" {
		return cfg, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("reading ptr.tls_ca_file: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("ptr.tls_ca_file contains no PEM certificates")
	}
	cfg.RootCAs = roots
	return cfg, nil
}

func (c *client) request(ctx context.Context, method, path string, q url.Values, body io.Reader) (*http.Request, error) {
	if c.tlsErr != nil {
		return nil, c.tlsErr
	}
	c.keyMu.RLock()
	addr, key := c.addr, c.accessKey
	c.keyMu.RUnlock()
	if !config.ValidPTRAddress(addr) {
		return nil, errors.New("the PTR address must use HTTPS, or HTTP with a literal loopback IP")
	}
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Hydrus-Key", key)
	return req, nil
}

func repositoryConnectionError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		err = uerr.Err
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return fmt.Errorf("verifying the PTR certificate: %w (for private trust, set ptr.tls_ca_file and restart Meloader)", err)
	}
	return &retryable{err: fmt.Errorf("reaching the repository: %w", err)}
}

func (c *client) Metadata(ctx context.Context, since uint64) (*metadataSlice, error) {
	q := url.Values{"since": {strconv.FormatUint(since, 10)}}
	raw, err := c.get(ctx, "/metadata", q)
	if err != nil {
		return nil, err
	}
	return parseMetadata(raw)
}

// updateRaw skips the parse: the sync holds an entry's blobs compressed
// and inflates one at a time.
func (c *client) updateRaw(ctx context.Context, hashHex string) ([]byte, error) {
	if sleep := c.fetchSleep(); sleep > 0 {
		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	q := url.Values{"update_hash": {hashHex}}
	raw, err := c.get(ctx, "/update", q)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, hashHex) {
		return nil, fmt.Errorf("update %s failed integrity check (got %s)", hashHex, got)
	}
	return raw, nil
}

func (c *client) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	req, err := c.request(ctx, http.MethodGet, path, q, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, repositoryConnectionError(err)
	}
	defer resp.Body.Close()
	// hydrus answers 509 over its bandwidth limit; other 4xx (bad key,
	// unknown hash) are permanent.
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, &retryable{err: fmt.Errorf("repository returned %s", resp.Status)}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("repository returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUpdateBytes+1))
	if err != nil {
		return nil, &retryable{err: fmt.Errorf("reading the response: %w", err)}
	}
	return body, nil
}
