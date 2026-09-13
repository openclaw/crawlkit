package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Options struct {
	Endpoint      string
	HTTPClient    *http.Client
	TokenProvider TokenProvider
	UserAgent     string
}

type Client struct {
	endpoint      *url.URL
	httpClient    *http.Client
	tokenProvider TokenProvider
	userAgent     string
}

func NewClient(opts Options) (*Client, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(opts.Endpoint), "/")
	if endpoint == "" {
		return nil, errors.New("remote endpoint is required")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid remote endpoint %q", endpoint)
	}
	if opts.TokenProvider != nil && !credentialTransportAllowed(parsed) {
		return nil, fmt.Errorf("remote endpoint %q cannot use bearer auth over %s", endpoint, parsed.Scheme)
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	userAgent := strings.TrimSpace(opts.UserAgent)
	if userAgent == "" {
		userAgent = "crawlkit-remote"
	}
	return &Client{
		endpoint:      parsed,
		httpClient:    client,
		tokenProvider: opts.TokenProvider,
		userAgent:     userAgent,
	}, nil
}

func isLocalHTTPHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func credentialTransportAllowed(endpoint *url.URL) bool {
	return endpoint.Scheme == "https" ||
		(endpoint.Scheme == "http" && isLocalHTTPHost(endpoint.Hostname()))
}

func sameOrigin(left, right *url.URL) bool {
	port := func(u *url.URL) string {
		if explicit := u.Port(); explicit != "" {
			return explicit
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return left.Scheme == right.Scheme &&
		strings.EqualFold(left.Hostname(), right.Hostname()) && port(left) == port(right)
}

type Error struct {
	Status  int    `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	code := strings.TrimSpace(e.Code)
	if code == "" {
		return fmt.Sprintf("remote request failed: status=%d message=%s", e.Status, msg)
	}
	return fmt.Sprintf("remote request failed: status=%d code=%s message=%s", e.Status, code, msg)
}

func (c *Client) do(ctx context.Context, method, route string, input, output any, auth bool) error {
	var body io.Reader
	if input != nil {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(input); err != nil {
			return fmt.Errorf("encode remote request: %w", err)
		}
		body = &buf
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(route), body)
	if err != nil {
		return err
	}
	credentials := auth
	switch input.(type) {
	case GitHubTokenLoginRequest, *GitHubTokenLoginRequest, LoginPollRequest, *LoginPollRequest:
		credentials = true
	}
	return c.doRequest(ctx, req, input != nil, output, auth, credentials)
}

func (c *Client) doRaw(ctx context.Context, method, route string, body io.Reader, size int64, headers http.Header, output any, auth bool) error {
	req, err := http.NewRequestWithContext(ctx, method, c.url(route), body)
	if err != nil {
		return err
	}
	if size >= 0 {
		req.ContentLength = size
	} else {
		req.ContentLength = -1
	}
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	return c.doRequest(ctx, req, true, output, auth, auth)
}

func (c *Client) doRequest(ctx context.Context, req *http.Request, hasBody bool, output any, auth, credentials bool) error {
	if req.URL.User != nil || req.Header.Get("Authorization") != "" {
		credentials = true
	}
	if credentials && !credentialTransportAllowed(req.URL) {
		return errors.New("remote credentials require HTTPS except for loopback HTTP")
	}
	req.Header.Set("accept", "application/json")
	req.Header.Set("user-agent", c.userAgent)
	if hasBody && req.Header.Get("content-type") == "" {
		req.Header.Set("content-type", "application/json")
	}
	if auth {
		if c.tokenProvider == nil {
			return ErrMissingToken
		}
		token, err := c.tokenProvider.Token(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+token)
	}
	httpClient := c.httpClient
	if credentials {
		// Keep the caller's client reusable; this policy belongs to this request.
		client := *httpClient
		policy := client.CheckRedirect
		origin := *req.URL
		checkOrigin := func(next *http.Request) error {
			if !credentialTransportAllowed(next.URL) || !sameOrigin(&origin, next.URL) {
				return errors.New("remote credential redirect must retain the original scheme, host and port")
			}
			return nil
		}
		client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
			if err := checkOrigin(next); err != nil {
				return err
			}
			if policy != nil {
				if err := policy(next, via); err != nil {
					return err
				}
			} else if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			// A caller policy can modify next.URL as well as accept or reject it.
			return checkOrigin(next)
		}
		httpClient = &client
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeRemoteError(resp)
	}
	if output == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(output); err != nil {
		return fmt.Errorf("decode remote response: %w", err)
	}
	return nil
}

func (c *Client) url(route string) string {
	route = "/" + strings.TrimLeft(route, "/")
	u := *c.endpoint
	escapedPath := strings.TrimRight(u.EscapedPath(), "/") + route
	unescapedPath, err := url.PathUnescape(escapedPath)
	if err == nil {
		u.Path = unescapedPath
		if unescapedPath != escapedPath {
			u.RawPath = escapedPath
		}
	} else {
		u.Path = escapedPath
	}
	return u.String()
}

func decodeRemoteError(resp *http.Response) error {
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	errOut := Error{Status: resp.StatusCode}
	var decoded struct {
		Error   string `json:"error"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(payload, &decoded); err == nil {
		errOut.Code = firstNonEmpty(decoded.Code, decoded.Error)
		errOut.Message = decoded.Message
		if errOut.Message == "" && decoded.Error != "" && decoded.Code == "" {
			errOut.Message = decoded.Error
		}
	}
	if errOut.Message == "" {
		errOut.Message = strings.TrimSpace(string(payload))
	}
	return &errOut
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
