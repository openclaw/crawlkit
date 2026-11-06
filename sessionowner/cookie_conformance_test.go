package sessionowner

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func b2c1CookiePolicy() CookiePolicy {
	return CookiePolicy{
		PublicSuffixList: fixturePSL{}, MaxCookies: 20, MaxCookieBytes: 4096,
		Allow: func(*url.URL, *http.Cookie) error { return nil },
		AllowTarget: func(u *url.URL) error {
			if u.Scheme != "https" && u.Scheme != "http" {
				return errors.New("scheme")
			}
			h := u.Hostname()
			if h != "provider.test" && h != "sub.provider.test" && h != "other.test" {
				return errors.New("target")
			}
			return nil
		},
		CSRF: func(cookies []*http.Cookie) (string, error) {
			value := ""
			for _, c := range cookies {
				if c.Name == "csrf" {
					if value != "" && value != c.Value {
						return "", errors.New("contradiction")
					}
					value = c.Value
				}
			}
			return value, nil
		},
	}
}
func b2c1CookieBytes(t *testing.T, c *CookieState) []byte {
	t.Helper()
	b, err := json.Marshal(c.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func b2c1CookieValues(t *testing.T, c *CookieState, target string, now time.Time) []string {
	t.Helper()
	cookies, err := c.Select(target, now)
	if err != nil {
		t.Fatal(err)
	}
	values := []string{}
	for _, cookie := range cookies {
		values = append(values, cookie.Name+"="+cookie.Value)
	}
	return values
}

func TestB2C1Cookies(t *testing.T) {
	cases := []string{"host_only", "domain_subdomain", "default_path", "path_order", "secure", "public_suffix_refusal", "cross_target_refusal", "maxage_precedence", "absolute_expiry", "delete_exact_key", "duplicate_replace", "csrf_rollback", "serialized_expiry", "valid_multi_origin_reload", "constructor_records_owned", "snapshot_unparsed_owned", "apply_unparsed_owned", "constructor_unparsed_owned", "count_cap_rollback", "byte_cap_rollback"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			policy := b2c1CookiePolicy()
			now := time.Date(2026, 11, 6, 11, 0, 0, 0, time.UTC)
			state, err := NewCookieState(policy, CookieSnapshot{})
			if err != nil {
				t.Fatal(err)
			}
			apply := func(origin string, cookies ...*http.Cookie) {
				t.Helper()
				if e := state.Apply(origin, cookies, true, now); e != nil {
					t.Fatal(e)
				}
			}
			want := func(target string, at time.Time, expected ...string) {
				t.Helper()
				if expected == nil {
					expected = []string{}
				}
				if got := b2c1CookieValues(t, state, target, at); !reflect.DeepEqual(got, expected) {
					t.Fatalf("selection got=%v want=%v", got, expected)
				}
			}
			unchanged := func(before []byte) {
				t.Helper()
				if string(before) != string(b2c1CookieBytes(t, state)) {
					t.Fatal("records/CSRF/revision changed on refusal or external mutation")
				}
			}
			switch name {
			case "host_only":
				apply("https://provider.test/", &http.Cookie{Name: "auth", Value: "host", Path: "/"})
				want("https://provider.test/", now, "auth=host")
				want("https://sub.provider.test/", now)
			case "domain_subdomain":
				apply("https://provider.test/", &http.Cookie{Name: "auth", Value: "domain", Domain: ".provider.test", Path: "/"})
				want("https://sub.provider.test/", now, "auth=domain")
				want("https://other.test/", now)
			case "default_path":
				apply("https://provider.test/a/b", &http.Cookie{Name: "auth", Value: "default"})
				if state.Snapshot().Records[0].Cookie.Path != "/a" {
					t.Fatal("default path not persisted")
				}
				want("https://provider.test/a/next", now, "auth=default")
				want("https://provider.test/ab", now)
			case "path_order":
				apply("https://provider.test/a/b", &http.Cookie{Name: "auth", Value: "root", Path: "/"}, &http.Cookie{Name: "auth", Value: "deep", Path: "/a"})
				want("https://provider.test/a/b", now, "auth=deep", "auth=root")
			case "secure":
				apply("https://provider.test/", &http.Cookie{Name: "auth", Value: "secure", Secure: true, Path: "/"})
				want("https://provider.test/", now, "auth=secure")
				want("http://provider.test/", now)
			case "public_suffix_refusal", "cross_target_refusal":
				apply("https://provider.test/", &http.Cookie{Name: "csrf", Value: "good", Path: "/"})
				before := b2c1CookieBytes(t, state)
				origin := "https://provider.test/"
				cookie := &http.Cookie{Name: "auth", Value: "bad", Domain: "test", Path: "/"}
				if name == "cross_target_refusal" {
					origin = "https://unregistered.invalid/"
					cookie.Domain = ""
				}
				if state.Apply(origin, []*http.Cookie{cookie}, true, now) == nil {
					t.Fatal("policy mutation accepted")
				}
				unchanged(before)
				if name == "cross_target_refusal" {
					if _, e := state.Select(origin, now); e == nil {
						t.Fatal("redirect target accepted")
					}
					unchanged(before)
				}
			case "maxage_precedence":
				apply("https://provider.test/", &http.Cookie{Name: "auth", Value: "age", Path: "/", MaxAge: 10, Expires: now.Add(-time.Hour)})
				record := state.Snapshot().Records[0]
				if record.Cookie.MaxAge != 0 || !record.Cookie.Expires.Equal(now.Add(10*time.Second)) {
					t.Fatal("relative maxage not normalized exactly once")
				}
				want("https://provider.test/", now.Add(9*time.Second), "auth=age")
				want("https://provider.test/", now.Add(10*time.Second))
			case "absolute_expiry":
				apply("https://provider.test/", &http.Cookie{Name: "auth", Value: "absolute", Path: "/", Expires: now.Add(3 * time.Second)})
				want("https://provider.test/", now.Add(2*time.Second), "auth=absolute")
				want("https://provider.test/", now.Add(3*time.Second))
			case "delete_exact_key":
				apply("https://provider.test/a", &http.Cookie{Name: "auth", Value: "root", Path: "/"}, &http.Cookie{Name: "auth", Value: "deep", Path: "/a"})
				apply("https://provider.test/a", &http.Cookie{Name: "auth", Path: "/a", MaxAge: -1})
				want("https://provider.test/a", now, "auth=root")
				if len(state.Snapshot().Records) != 1 {
					t.Fatal("deletion was not exact")
				}
			case "duplicate_replace":
				apply("https://provider.test/", &http.Cookie{Name: "auth", Value: "old", Path: "/"})
				created := state.Snapshot().Records[0].Created
				apply("https://provider.test/", &http.Cookie{Name: "auth", Value: "middle", Path: "/"}, &http.Cookie{Name: "auth", Value: "last", Path: "/"})
				want("https://provider.test/", now, "auth=last")
				if s := state.Snapshot(); len(s.Records) != 1 || s.Records[0].Created != created || s.Revision != 2 {
					t.Fatal("duplicate replacement order/revision")
				}
			case "csrf_rollback":
				apply("https://provider.test/a", &http.Cookie{Name: "csrf", Value: "one", Path: "/"})
				before := b2c1CookieBytes(t, state)
				if state.Apply("https://provider.test/a", []*http.Cookie{{Name: "auth", Value: "new", Path: "/"}, {Name: "csrf", Value: "two", Path: "/a"}}, true, now) == nil {
					t.Fatal("contradictory CSRF accepted")
				}
				unchanged(before)
			case "serialized_expiry":
				apply("https://provider.test/a", &http.Cookie{Name: "auth", Value: "expiring", Path: "/", MaxAge: 10}, &http.Cookie{Name: "csrf", Value: "stable", Path: "/"})
				before := b2c1CookieBytes(t, state)
				var restored CookieSnapshot
				if e := json.Unmarshal(before, &restored); e != nil {
					t.Fatal(e)
				}
				state, err = NewCookieState(policy, restored)
				if err != nil {
					t.Fatal(err)
				}
				unchanged(before)
				want("https://provider.test/a", now.Add(9*time.Second), "auth=expiring", "csrf=stable")
				want("https://provider.test/a", now.Add(10*time.Second), "csrf=stable")
				if state.Snapshot().CSRF != "stable" {
					t.Fatal("reloaded CSRF changed")
				}
			case "valid_multi_origin_reload":
				apply("https://provider.test/", &http.Cookie{Name: "csrf", Value: "first", Path: "/"})
				apply("https://other.test/", &http.Cookie{Name: "csrf", Value: "second", Path: "/"})
				before := b2c1CookieBytes(t, state)
				var restored CookieSnapshot
				if e := json.Unmarshal(before, &restored); e != nil {
					t.Fatal(e)
				}
				state, err = NewCookieState(policy, restored)
				if err != nil {
					t.Fatal("valid multi-origin snapshot rejected", err)
				}
				unchanged(before)
				want("https://provider.test/", now, "csrf=first")
				want("https://other.test/", now, "csrf=second")
				if state.Snapshot().CSRF != "second" {
					t.Fatal("constructor guessed unknown CSRF origin")
				}
			case "constructor_records_owned", "constructor_unparsed_owned":
				input := CookieSnapshot{Revision: 7, CSRF: "opaque-qualified", Records: []CookieRecord{{Origin: "https://provider.test/", Cookie: http.Cookie{Name: "auth", Value: "owned", Path: "/", Unparsed: []string{"Extension=original"}}, Created: 1}}}
				state, err = NewCookieState(policy, input)
				if err != nil {
					t.Fatal(err)
				}
				before := b2c1CookieBytes(t, state)
				if name == "constructor_records_owned" {
					input.Records[0].Cookie.Value = "mutated"
				} else {
					input.Records[0].Cookie.Unparsed[0] = "Extension=mutated"
				}
				unchanged(before)
			case "snapshot_unparsed_owned", "apply_unparsed_owned":
				input := &http.Cookie{Name: "auth", Value: "owned", Path: "/", Unparsed: []string{"Extension=original"}}
				apply("https://provider.test/", input)
				before := b2c1CookieBytes(t, state)
				if name == "snapshot_unparsed_owned" {
					out := state.Snapshot()
					out.Records[0].Cookie.Unparsed[0] = "Extension=mutated"
					out.Records[0].Cookie.Value = "mutated"
				} else {
					input.Unparsed[0] = "Extension=mutated"
					input.Value = "mutated"
				}
				unchanged(before)
			case "count_cap_rollback", "byte_cap_rollback":
				if name == "count_cap_rollback" {
					policy.MaxCookies = 1
				} else {
					policy.MaxCookieBytes = 8
				}
				state, err = NewCookieState(policy, CookieSnapshot{})
				if err != nil {
					t.Fatal(err)
				}
				apply("https://provider.test/", &http.Cookie{Name: "a", Value: "one", Path: "/"})
				before := b2c1CookieBytes(t, state)
				cookie := &http.Cookie{Name: "b", Value: "two", Path: "/"}
				if name == "byte_cap_rollback" {
					cookie.Value = strings.Repeat("x", 8)
				}
				if state.Apply("https://provider.test/", []*http.Cookie{cookie}, true, now) == nil {
					t.Fatal("cookie cap bypassed")
				}
				unchanged(before)
			default:
				t.Fatal("unhandled case")
			}
		})
	}
}
