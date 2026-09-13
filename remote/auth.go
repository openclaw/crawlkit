package remote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
)

type TokenProvider interface {
	Token(context.Context) (string, error)
}

type StaticToken string

func (t StaticToken) Token(context.Context) (string, error) {
	token := strings.TrimSpace(string(t))
	if token == "" {
		return "", ErrMissingToken
	}
	return token, nil
}

type EnvTokenProvider struct {
	Name string
}

func (p EnvTokenProvider) Token(context.Context) (string, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = DefaultTokenEnv
	}
	token := strings.TrimSpace(os.Getenv(name))
	if token == "" {
		return "", fmt.Errorf("%w: %s", ErrMissingToken, name)
	}
	return token, nil
}

type ChainTokenProvider []TokenProvider

func (p ChainTokenProvider) Token(ctx context.Context) (string, error) {
	var lastErr error
	for _, provider := range p {
		if provider == nil {
			continue
		}
		token, err := provider.Token(ctx)
		if err == nil && strings.TrimSpace(token) != "" {
			return token, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", ErrMissingToken
}

var ErrMissingToken = errors.New("remote token is missing")

type Identity struct {
	Owner string   `json:"owner"`
	Org   string   `json:"org"`
	Login string   `json:"login,omitempty"`
	Auth  string   `json:"auth,omitempty"`
	Roles []string `json:"roles,omitempty"`
}

type LoginStartRequest struct {
	PollSecretHash string `json:"pollSecretHash"`
}

type LoginStartResult struct {
	LoginID   string `json:"loginID"`
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

type LoginPollRequest struct {
	LoginID    string `json:"loginID"`
	PollSecret string `json:"pollSecret"`
}

type GitHubTokenLoginRequest struct {
	Token string `json:"token"`
}

type LoginPollResult struct {
	Status string `json:"status"`
	Token  string `json:"token,omitempty"`
	Owner  string `json:"owner,omitempty"`
	Org    string `json:"org,omitempty"`
	Login  string `json:"login,omitempty"`
	Error  string `json:"error,omitempty"`
}

func NewLoginPollSecret() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("create login poll secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func LoginPollSecretHash(secret string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(secret)))
	return fmt.Sprintf("%x", sum[:])
}

func (c *Client) Whoami(ctx context.Context) (Identity, error) {
	var out Identity
	err := c.do(ctx, http.MethodGet, "/v1/whoami", nil, &out, true)
	return out, err
}

func (c *Client) StartGitHubLogin(ctx context.Context, pollSecretHash string) (LoginStartResult, error) {
	var out LoginStartResult
	err := c.do(ctx, http.MethodPost, "/v1/auth/github/start", LoginStartRequest{PollSecretHash: pollSecretHash}, &out, false)
	return out, err
}

func (c *Client) PollGitHubLogin(ctx context.Context, loginID, pollSecret string) (LoginPollResult, error) {
	var out LoginPollResult
	err := c.do(ctx, http.MethodPost, "/v1/auth/github/poll", LoginPollRequest{LoginID: loginID, PollSecret: pollSecret}, &out, false)
	return out, err
}

func (c *Client) LoginWithGitHubToken(ctx context.Context, token string) (LoginPollResult, error) {
	var out LoginPollResult
	err := c.do(ctx, http.MethodPost, "/v1/auth/github/token", GitHubTokenLoginRequest{Token: strings.TrimSpace(token)}, &out, false)
	return out, err
}
