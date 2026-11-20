package remote

import (
	"strings"
	"testing"
)

func TestGitHubRetainedSnapshotSeed(t *testing.T) {
	m, chunks := githubSeedFixture(t)
	m.Profile = GitHubRetainedSnapshotProfile
	a := GitHubSnapshotAnchor{FileSHA256: strings.Repeat("c", 64), ScopeSHA256: m.ScopeSHA256, InventorySHA256: m.NativeSHA256}
	var err error
	m.NativeThrough, err = EncodeGitHubSnapshotAnchor(a)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := githubSeedPlan(t, m)
	got, err := ParseGitHubSnapshotAnchor(p.Manifest().NativeThrough)
	if err != nil || got != a {
		t.Fatalf("snapshot anchor changed: %+v %v", got, err)
	}
	for _, chunk := range chunks {
		if _, err := p.ValidateChunk(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.ValidateComplete(githubSeedCompletion(t, p)); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseGitHubNativeAnchor(m.NativeThrough); err == nil {
		t.Fatal("snapshot admitted as a live change anchor")
	}
	for _, mutate := range []func(*GitHubSeedManifest){
		func(m *GitHubSeedManifest) { m.Profile = "gitcrawl-private-report-v1" },
		func(m *GitHubSeedManifest) { m.ScopeSHA256 = strings.Repeat("d", 64) },
		func(m *GitHubSeedManifest) { m.NativeSHA256 = strings.Repeat("d", 64) },
		func(m *GitHubSeedManifest) {
			m.NativeThrough = "gitcrawl-snapshot-v1:bad:" + m.ScopeSHA256 + ":" + m.NativeSHA256
		},
	} {
		bad := m
		mutate(&bad)
		if _, err := EncodeGitHubSeedManifest(bad); err == nil {
			t.Fatal("accepted mismatched snapshot identity")
		}
	}
	other := a
	other.FileSHA256 = strings.Repeat("d", 64)
	m.NativeThrough, _ = EncodeGitHubSnapshotAnchor(other)
	p2, _ := githubSeedPlan(t, m)
	if p2.ManifestSHA256() == p.ManifestSHA256() {
		t.Fatal("different retained files share a manifest identity")
	}
}

func TestGitHubSnapshotAnchorRejectsMalformed(t *testing.T) {
	a := GitHubSnapshotAnchor{FileSHA256: strings.Repeat("a", 64), ScopeSHA256: strings.Repeat("b", 64), InventorySHA256: strings.Repeat("c", 64)}
	token, _ := EncodeGitHubSnapshotAnchor(a)
	for _, bad := range []string{"", token + ":extra", " " + token, strings.ToUpper(token), strings.Replace(token, "snapshot", "native", 1)} {
		if got, err := ParseGitHubSnapshotAnchor(bad); err == nil || got != (GitHubSnapshotAnchor{}) {
			t.Fatalf("accepted %q", bad)
		}
	}
	a.FileSHA256 = "bad"
	if _, err := EncodeGitHubSnapshotAnchor(a); err == nil {
		t.Fatal("encoded invalid file hash")
	}
}
