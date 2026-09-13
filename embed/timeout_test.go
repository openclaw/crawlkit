package embed

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

type waitingTransport struct{}

func (waitingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	<-req.Context().Done()
	return nil, req.Context().Err()
}

func TestCustomClientRespectsEmbeddingTimeouts(t *testing.T) {
	for _, providerName := range []string{ProviderOllama, ProviderOpenAICompatible} {
		for _, tc := range []struct {
			name                                               string
			clientTimeout, requestTimeout, parentTimeout, want time.Duration
		}{
			{"unbounded client", 0, 2 * time.Second, time.Minute, 2 * time.Second},
			{"longer client", 30 * time.Second, 2 * time.Second, time.Minute, 2 * time.Second},
			{"shorter client", time.Second, 2 * time.Second, time.Minute, time.Second},
			{"shorter context", 30 * time.Second, 2 * time.Second, time.Second, time.Second},
		} {
			t.Run(providerName+"/"+tc.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					client := &http.Client{Transport: waitingTransport{}, Timeout: tc.clientTimeout}
					provider, err := NewProvider(Config{Provider: providerName, BaseURL: "http://127.0.0.1", RequestTimeout: tc.requestTimeout.String()}, WithHTTPClient(client))
					if err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(context.Background(), tc.parentTimeout)
					defer cancel()
					start := time.Now()
					_, err = provider.Embed(ctx, []string{"synthetic"})
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("error = %v, want deadline exceeded", err)
					}
					if elapsed := time.Since(start); elapsed != tc.want {
						t.Fatalf("elapsed = %s, want %s", elapsed, tc.want)
					}
					if client.Timeout != tc.clientTimeout {
						t.Fatal("caller-owned client timeout was mutated")
					}
				})
			})
		}
	}
}
