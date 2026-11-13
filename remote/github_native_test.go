package remote

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
)

func githubNativeFixture(version string) GitHubNativeAnchor {
	return GitHubNativeAnchor{Version: version, Epoch: "0123456789abcdef0123456789abcdef", Generation: 1, SchemaCookie: 83,
		ScopeSHA256: strings.Repeat("a", 64), InventorySHA256: strings.Repeat("b", 64)}
}

func TestGitHubNativeAnchorRoundTrip(t *testing.T) {
	for _, version := range []string{GitHubNativeV1, GitHubNativeV2} {
		a := githubNativeFixture(version)
		want := version + ":0123456789abcdef0123456789abcdef:1:83:0:0:" + strings.Repeat("a", 64) + ":" + strings.Repeat("b", 64)
		encoded, err := EncodeGitHubNativeAnchor(a)
		if err != nil || encoded != want {
			t.Fatalf("canonical H0 token: %q %v", encoded, err)
		}
		got, err := ParseGitHubNativeAnchor(want)
		if err != nil || got != a {
			t.Fatalf("H0 roundtrip: %+v %v", got, err)
		}
		a.Generation, a.SchemaCookie, a.Sequence, a.PrunedThrough = math.MaxInt64, math.MaxInt64, math.MaxInt64, math.MaxInt64
		encoded, err = EncodeGitHubNativeAnchor(a)
		if err != nil {
			t.Fatal(err)
		}
		got, err = ParseGitHubNativeAnchor(encoded)
		if err != nil || got != a {
			t.Fatalf("int64 endpoint roundtrip: %+v %v", got, err)
		}
	}
}

func TestGitHubNativeAnchorRejectsMalformed(t *testing.T) {
	valid, err := EncodeGitHubNativeAnchor(githubNativeFixture(GitHubNativeV1))
	if err != nil {
		t.Fatal(err)
	}
	invalid := []string{"", valid + ":extra", strings.TrimSuffix(valid, ":"+strings.Repeat("b", 64)), " " + valid, valid + "\n", strings.Repeat("x", 513)}
	fields := map[int][]string{
		0: {"gitcrawl-native-v0", "gitcrawl-native-v3", "GITCRAWL-native-v1", ""},
		1: {"", strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("A", 32), strings.Repeat("g", 32)},
		6: {"", strings.Repeat("a", 63), strings.Repeat("A", 64), strings.Repeat("g", 64)},
		7: {"", strings.Repeat("b", 65), strings.Repeat("B", 64), strings.Repeat("b", 63) + "\x00"},
	}
	for i := 2; i <= 5; i++ {
		fields[i] = []string{"", "-1", "-0", "+1", "01", "00", " 1", "1\n", "1.0", "１", "9223372036854775808", "-9223372036854775809"}
		if i <= 3 {
			fields[i] = append(fields[i], "0")
		}
	}
	fields[5] = append(fields[5], "1") // floor cannot exceed H0.
	for i, values := range fields {
		for _, value := range values {
			parts := strings.Split(valid, ":")
			parts[i] = value
			invalid = append(invalid, strings.Join(parts, ":"))
		}
	}
	for i, token := range invalid {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			got, err := ParseGitHubNativeAnchor(token)
			if !errors.Is(err, ErrGitHubNativeAnchor) || got != (GitHubNativeAnchor{}) {
				t.Fatal("invalid token returned an anchor", got, err)
			}
		})
	}
}

func TestGitHubNativeTypedValidation(t *testing.T) {
	mutations := map[string]func(*GitHubNativeAnchor){
		"version":        func(a *GitHubNativeAnchor) { a.Version = "unknown" },
		"epoch":          func(a *GitHubNativeAnchor) { a.Epoch = strings.Repeat("A", 32) },
		"scope":          func(a *GitHubNativeAnchor) { a.ScopeSHA256 = "bad" },
		"inventory":      func(a *GitHubNativeAnchor) { a.InventorySHA256 = "bad" },
		"generation":     func(a *GitHubNativeAnchor) { a.Generation = 0 },
		"cookie":         func(a *GitHubNativeAnchor) { a.SchemaCookie = -1 },
		"sequence":       func(a *GitHubNativeAnchor) { a.Sequence = -1 },
		"floor negative": func(a *GitHubNativeAnchor) { a.PrunedThrough = -1 },
		"floor beyond H": func(a *GitHubNativeAnchor) { a.PrunedThrough = 1 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			bad, valid := githubNativeFixture(GitHubNativeV2), githubNativeFixture(GitHubNativeV2)
			mutate(&bad)
			if token, err := EncodeGitHubNativeAnchor(bad); !errors.Is(err, ErrGitHubNativeAnchor) || token != "" {
				t.Fatal("invalid typed anchor encoded", token, err)
			}
			if !errors.Is(ValidateGitHubNativeRange(bad, valid), ErrGitHubNativeAnchor) ||
				!errors.Is(ValidateGitHubNativeRange(valid, bad), ErrGitHubNativeAnchor) {
				t.Fatal("range accepted an invalid typed endpoint")
			}
		})
	}
}

func TestGitHubNativeRange(t *testing.T) {
	from, through := githubNativeFixture(GitHubNativeV1), githubNativeFixture(GitHubNativeV2)
	through.InventorySHA256 = strings.Repeat("c", 64)
	if err := ValidateGitHubNativeRange(from, through); err != nil {
		t.Fatal("first empty v1->v2 transition must allow a different digest at H0", err)
	}
	from = through
	if err := ValidateGitHubNativeRange(from, through); err != nil {
		t.Fatal("unchanged v2 range refused", err)
	}
	from.Sequence, from.PrunedThrough = 10, 3
	through = from
	through.Sequence, through.PrunedThrough = 20, 10
	if err := ValidateGitHubNativeRange(from, through); err != nil {
		t.Fatal("out-of-scope advance with unchanged inventory refused", err)
	}
	through.Sequence = 10
	if err := ValidateGitHubNativeRange(from, through); err != nil {
		t.Fatal("same-H unchanged inventory with advanced floor refused", err)
	}
	cases := map[string]func(*GitHubNativeAnchor){
		"v2 to v1":                 func(a *GitHubNativeAnchor) { a.Version = GitHubNativeV1 },
		"epoch":                    func(a *GitHubNativeAnchor) { a.Epoch = strings.Repeat("d", 32) },
		"generation":               func(a *GitHubNativeAnchor) { a.Generation++ },
		"cookie":                   func(a *GitHubNativeAnchor) { a.SchemaCookie++ },
		"scope":                    func(a *GitHubNativeAnchor) { a.ScopeSHA256 = strings.Repeat("d", 64) },
		"H regression":             func(a *GitHubNativeAnchor) { a.Sequence = 9 },
		"floor regression":         func(a *GitHubNativeAnchor) { a.PrunedThrough = 2 },
		"journal gap":              func(a *GitHubNativeAnchor) { a.Sequence, a.PrunedThrough = 20, 11 },
		"same H changed inventory": func(a *GitHubNativeAnchor) { a.InventorySHA256 = strings.Repeat("d", 64) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := from
			mutate(&candidate)
			if err := ValidateGitHubNativeRange(from, candidate); !errors.Is(err, ErrGitHubNativeRange) {
				t.Fatal("incompatible range accepted", err)
			}
		})
	}
	through = from
	through.Sequence++
	through.InventorySHA256 = strings.Repeat("d", 64)
	if err := ValidateGitHubNativeRange(from, through); err != nil {
		t.Fatal("ordinary changed-row advance refused", err)
	}
	from.Version, through.Version = GitHubNativeV1, GitHubNativeV1
	if !errors.Is(ValidateGitHubNativeRange(from, through), ErrGitHubNativeRange) {
		t.Fatal("delta range allowed v1 Through")
	}
}

func TestGitHubNativeSeedRemainsV1Only(t *testing.T) {
	m, _ := githubSeedFixture(t)
	body, err := EncodeGitHubSeedManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	p, err := DecodeGitHubSeedManifest(body)
	if err != nil || p.Manifest().NativeThrough != m.NativeThrough {
		t.Fatal("existing v1 seed changed", err)
	}
	for _, version := range []string{GitHubNativeV2, "gitcrawl-native-v3"} {
		m.NativeThrough = strings.Replace(m.NativeThrough, strings.Split(m.NativeThrough, ":")[0], version, 1)
		if _, err := EncodeGitHubSeedManifest(m); err == nil {
			t.Fatal("new-version seed encoded")
		}
		body, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if p, err := DecodeGitHubSeedManifest(body); err == nil || p != nil {
			t.Fatal("new-version seed decoded")
		}
	}
}
