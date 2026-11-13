package remote

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func githubClusterSeedFixture(t *testing.T) (GitHubSeedManifest, [][]byte) {
	t.Helper()
	m, bodies := githubSeedFixture(t)
	m.Profile = "gitcrawl.public-durable-cluster-context/v1"
	m.NativeThrough = "gitcrawl-cluster-v1:" + m.ScopeSHA256 + ":" + m.NativeSHA256
	m.Tables = nil
	// Reuse the scalar-fidelity fixture without copying native column policy.
	var chunks [][]byte
	for _, name := range githubClusterSeedTables {
		table := SeedTable{Name: name, Rows: "0", SHA256: seedDigest(nil), Chunks: []SeedChunkInfo{}}
		if name == "selection" || name == "threads" {
			c, err := DecodeGitHubSeedChunk(bodies[len(chunks)])
			if err != nil {
				t.Fatal(err)
			}
			c.Changes = c.Changes[:1]
			c.Changes[0].Table = name
			body, err := EncodeGitHubSeedChunk(c)
			if err != nil {
				t.Fatal(err)
			}
			chunks = append(chunks, body)
			table.Rows = "1"
			table.Chunks = []SeedChunkInfo{{Rows: 1, Bytes: len(body), SHA256: seedDigest(body)}}
			table.SHA256 = seedTableHash(table.Chunks)
		}
		m.Tables = append(m.Tables, table)
	}
	return m, chunks
}

func TestGitHubClusterSeedProfileAndContentAnchor(t *testing.T) {
	m, chunks := githubClusterSeedFixture(t)
	p, _ := githubSeedPlan(t, m)
	for _, body := range chunks {
		if _, err := p.ValidateChunk(body); err != nil {
			t.Fatal(err)
		}
	}
	// Empty graphs carry selection; cardinality and public eligibility remain
	// downstream policy, not an inference made by the wire codec.
	m.Tables[3] = SeedTable{Name: "threads", Rows: "0", SHA256: seedDigest(nil), Chunks: []SeedChunkInfo{}}
	empty, _ := githubSeedPlan(t, m)
	if _, err := empty.ValidateChunk(chunks[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := empty.ValidateComplete(githubSeedCompletion(t, empty)); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*GitHubSeedManifest){
		"schema":         func(m *GitHubSeedManifest) { m.NativeSchema = 15 },
		"profile":        func(m *GitHubSeedManifest) { m.Profile += "-other" },
		"scope mismatch": func(m *GitHubSeedManifest) { m.ScopeSHA256 = strings.Repeat("c", 64) },
		"hash mismatch":  func(m *GitHubSeedManifest) { m.NativeSHA256 = strings.Repeat("c", 64) },
		"uppercase":      func(m *GitHubSeedManifest) { m.NativeThrough = strings.ToUpper(m.NativeThrough) },
		"missing digest": func(m *GitHubSeedManifest) { m.NativeThrough = "gitcrawl-cluster-v1:" + m.ScopeSHA256 },
		"extra field":    func(m *GitHubSeedManifest) { m.NativeThrough += ":0" },
		"table order":    func(m *GitHubSeedManifest) { m.Tables[0], m.Tables[1] = m.Tables[1], m.Tables[0] },
		"private tables": func(m *GitHubSeedManifest) { private, _ := githubSeedFixture(t); m.Tables = private.Tables },
		"private token": func(m *GitHubSeedManifest) {
			private, _ := githubSeedFixture(t)
			m.NativeThrough = private.NativeThrough
		},
	} {
		t.Run(name, func(t *testing.T) {
			m, _ := githubClusterSeedFixture(t)
			mutate(&m)
			if _, err := EncodeGitHubSeedManifest(m); err == nil {
				t.Fatal("invalid cluster manifest encoded")
			}
			body, _ := json.Marshal(m)
			if p, err := DecodeGitHubSeedManifest(body); err == nil || p != nil {
				t.Fatal("invalid cluster manifest decoded")
			}
		})
	}
}

func TestGitHubSeedPlansRejectCrossProfileChunksBeforeCredentials(t *testing.T) {
	private, privateChunks := githubSeedFixture(t)
	cluster, clusterChunks := githubClusterSeedFixture(t)
	for _, tc := range []struct {
		name string
		m    GitHubSeedManifest
		body []byte
	}{{"private", private, clusterChunks[0]}, {"cluster", cluster, privateChunks[0]}} {
		t.Run(tc.name, func(t *testing.T) {
			// Match count/size/hash to the foreign bytes: refusal must prove
			// the profile's table boundary, not just its hash.
			chunk, err := DecodeGitHubSeedChunk(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			tc.m.Tables[0].Rows = strconv.Itoa(len(chunk.Changes))
			tc.m.Tables[0].Chunks = []SeedChunkInfo{{Rows: len(chunk.Changes), Bytes: len(tc.body), SHA256: seedDigest(tc.body)}}
			tc.m.Tables[0].SHA256 = seedTableHash(tc.m.Tables[0].Chunks)
			p, _ := githubSeedPlan(t, tc.m)
			client, err := NewClient(Options{Endpoint: "https://remote.test", TokenProvider: tokenProviderFunc(func(context.Context) (string, error) {
				t.Fatal("cross-profile chunk reached credentials")
				return "", nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.ApplyGitHubSeedChunk(context.Background(), tc.m.Archive, p, tc.body); err == nil {
				t.Fatal("cross-profile chunk accepted")
			}
		})
	}
	cluster.Profile = "gitcrawl-private-report-v1"
	if _, err := EncodeGitHubSeedManifest(cluster); err == nil {
		t.Fatal("cluster content anchor accepted as private journal anchor")
	}
}
