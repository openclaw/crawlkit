package sessionowner

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"time"
)

// CookiePolicy MUST supply a qualified public suffix implementation and explicit
// provider origin/attribute policy. The core has no guessed suffix list, provider
// names or auth-cookie names. Unsupported browser partitioning fails closed.
type CookiePolicy struct {
	PublicSuffixList           cookiejar.PublicSuffixList
	Allow                      func(*url.URL, *http.Cookie) error
	AllowTarget                func(*url.URL) error
	CSRF                       func([]*http.Cookie) (string, error)
	QualifiedSameSite          bool
	MaxCookies, MaxCookieBytes int
}
type CookieRecord struct {
	Origin  string      `json:"origin"`
	Cookie  http.Cookie `json:"cookie"`
	Created uint64      `json:"created,string"`
}
type CookieSnapshot struct {
	Records  []CookieRecord `json:"records"`
	CSRF     string         `json:"csrf"`
	Revision uint64         `json:"revision,string"`
}
type CookieState struct {
	policy   CookiePolicy
	snapshot CookieSnapshot
}

func NewCookieState(p CookiePolicy, s CookieSnapshot) (*CookieState, error) {
	if p.PublicSuffixList == nil || p.Allow == nil || p.AllowTarget == nil || p.CSRF == nil || p.MaxCookies < 1 || p.MaxCookieBytes < 1 {
		return nil, fail(ProtocolMismatch, "cookie_policy")
	}
	if len(s.Records) > p.MaxCookies {
		return nil, fail(UnsafeStorage, "cookie_count")
	}
	// Snapshot metadata is supplied by the qualified owner. Its CSRF scope and
	// capture time cannot be reconstructed here, but mutable input is never kept.
	c := &CookieState{policy: p, snapshot: cloneCookieSnapshot(s)}
	for _, r := range c.snapshot.Records {
		u, e := url.Parse(r.Origin)
		if e != nil || c.validate(u, &r.Cookie) != nil {
			return nil, fail(UnsafeStorage, "cookie_record")
		}
	}
	return c, nil
}
func (c *CookieState) validate(u *url.URL, k *http.Cookie) error {
	if u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Hostname() == "" || k.Valid() != nil || k.Partitioned || k.SameSite > http.SameSiteDefaultMode && !c.policy.QualifiedSameSite || len(k.Name)+len(k.Value) > c.policy.MaxCookieBytes {
		return fail(ProtocolMismatch, "cookie_attributes")
	}
	host := strings.ToLower(u.Hostname())
	domain := strings.TrimPrefix(strings.ToLower(k.Domain), ".")
	if domain != "" {
		if host != domain && !strings.HasSuffix(host, "."+domain) {
			return fail(PermissionDenied, "cookie_domain")
		}
		if c.policy.PublicSuffixList.PublicSuffix(domain) == domain {
			return fail(PermissionDenied, "public_suffix")
		}
	}
	if strings.ContainsAny(host, "\x00\r\n") || strings.ContainsAny(domain, "\x00\r\n") {
		return fail(ProtocolMismatch, "cookie_domain")
	}
	if e := c.policy.AllowTarget(u); e != nil {
		return e
	}
	return c.policy.Allow(u, k)
}
func cookiePath(p string) string {
	if p == "" || p[0] != '/' {
		return "/"
	}
	i := strings.LastIndex(p, "/")
	if i == 0 {
		return "/"
	}
	return p[:i]
}
func cookieKey(r CookieRecord) string {
	u, _ := url.Parse(r.Origin)
	domain := strings.TrimPrefix(strings.ToLower(r.Cookie.Domain), ".")
	if domain == "" {
		domain = strings.ToLower(u.Hostname())
	}
	return domain + "\x00" + r.Cookie.Path + "\x00" + r.Cookie.Name
}
func cloneCookie(k http.Cookie) http.Cookie {
	if k.Unparsed != nil {
		attributes := make([]string, len(k.Unparsed))
		copy(attributes, k.Unparsed)
		k.Unparsed = attributes
	}
	return k
}
func cloneCookieSnapshot(s CookieSnapshot) CookieSnapshot {
	if s.Records != nil {
		records := make([]CookieRecord, len(s.Records))
		for i, r := range s.Records {
			records[i] = r
			records[i].Cookie = cloneCookie(r.Cookie)
		}
		s.Records = records
	}
	return s
}

// Snapshot returns an owned copy, including the cookies' mutable attributes.
func (c *CookieState) Snapshot() CookieSnapshot {
	return cloneCookieSnapshot(c.snapshot)
}

// Apply stages ALL response cookies plus CSRF before publication. Public or
// cookie-free requests cannot update this state. No network request is made.
func (c *CookieState) Apply(origin string, cookies []*http.Cookie, authenticated bool, now time.Time) error {
	if !authenticated {
		return fail(PermissionDenied, "public_cookie_write")
	}
	u, e := url.Parse(origin)
	if e != nil {
		return fail(ProtocolMismatch, "cookie_origin")
	}
	next := c.Snapshot()
	records := map[string]CookieRecord{}
	var creation uint64
	for _, r := range next.Records {
		records[cookieKey(r)] = r
		if r.Created > creation {
			creation = r.Created
		}
	}
	for _, k := range cookies {
		if k == nil {
			return fail(ProtocolMismatch, "cookie")
		}
		copy := cloneCookie(*k)
		if e = c.validate(u, &copy); e != nil {
			return e
		}
		if copy.Path == "" || copy.Path[0] != '/' {
			copy.Path = cookiePath(u.Path)
		}
		if copy.MaxAge > 0 {
			if int64(copy.MaxAge) > int64((1<<63-1)/int64(time.Second)) {
				return fail(ProtocolMismatch, "cookie_max_age")
			}
			copy.Expires = now.Add(time.Duration(copy.MaxAge) * time.Second)
		}
		r := CookieRecord{Origin: origin, Cookie: copy}
		key := cookieKey(r)
		if copy.MaxAge < 0 || !copy.Expires.IsZero() && !copy.Expires.After(now) {
			delete(records, key)
			continue
		}
		if old, ok := records[key]; ok {
			r.Created = old.Created
		} else {
			creation, e = nextRevision(creation)
			if e != nil {
				return e
			}
			r.Created = creation
		}
		r.Cookie.MaxAge = 0
		records[key] = r
	}
	if len(records) > c.policy.MaxCookies {
		return fail(PayloadTooLarge, "cookie_count")
	}
	next.Records = nil
	for _, r := range records {
		next.Records = append(next.Records, r)
	}
	sort.Slice(next.Records, func(i, j int) bool { return next.Records[i].Created < next.Records[j].Created })
	temp := &CookieState{policy: c.policy, snapshot: next}
	selected, e := temp.Select(origin, now)
	if e != nil {
		return e
	}
	csrf, e := c.policy.CSRF(selected)
	if e != nil {
		return fail(Code("rejected_generation"), "csrf_contradiction")
	}
	next.CSRF = csrf
	next.Revision, e = nextRevision(next.Revision)
	if e != nil {
		return e
	}
	c.snapshot = next
	return nil
}
func (c *CookieState) Select(target string, now time.Time) ([]*http.Cookie, error) {
	u, e := url.Parse(target)
	if e != nil || u.User != nil || u.Hostname() == "" {
		return nil, fail(ProtocolMismatch, "cookie_target")
	}
	if e = c.policy.AllowTarget(u); e != nil {
		return nil, e
	}
	jar, e := cookiejar.New(&cookiejar.Options{PublicSuffixList: c.policy.PublicSuffixList})
	if e != nil {
		return nil, fail(ProtocolMismatch, "cookie_policy")
	}
	for _, r := range c.snapshot.Records {
		if !r.Cookie.Expires.IsZero() && !r.Cookie.Expires.After(now) {
			continue
		}
		origin, e := url.Parse(r.Origin)
		if e != nil {
			return nil, fail(UnsafeStorage, "cookie_origin")
		}
		k := r.Cookie
		k.Expires = time.Time{}
		k.MaxAge = 0
		jar.SetCookies(origin, []*http.Cookie{&k})
	}
	return jar.Cookies(u), nil
}
