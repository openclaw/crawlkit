//go:build darwin

package sessionowner

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestB2B1AExactProtectedMetadataKeys(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	files, e := NewProtectedCandidateFiles(dir, "fixture-binding")
	if e != nil {
		t.Fatal(e)
	}
	scope := testCandidateScope()
	payload := []byte(`{"token":"private-metadata"}`)
	expires := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	ref, e := files.Write(context.Background(), scope, payload, expires, 2048)
	if e != nil {
		t.Fatal(e)
	}
	path, _ := files.path(ref.Ref)
	original, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	offset := len(candidateFileMagic) + 4
	n := int(binary.BigEndian.Uint32(original[len(candidateFileMagic):offset]))
	canonical := append([]byte(nil), original[offset:offset+n]...)
	expected, _ := json.Marshal(candidateFileHeader{"candidate-stage/1", scope, expires, len(payload), Hash(payload)})
	if !bytes.Equal(canonical, expected) {
		t.Fatal("canonical Write spelling changed")
	}
	var root map[string]json.RawMessage
	json.Unmarshal(canonical, &root)
	var scoped map[string]json.RawMessage
	json.Unmarshal(root["scope"], &scoped)
	cases := map[string][]byte{"canonical": canonical, "unknown-root": appendJSONMember(canonical, `"extra":true`), "escaped-duplicate-root": appendJSONMember(canonical, `"expires\u0041t":"2099-01-01T00:00:00Z"`)}
	cases["exact-duplicate-root"] = appendJSONMember(canonical, `"expiresAt":"2099-01-01T00:00:00Z"`)
	for key, value := range root {
		alias := strings.ToUpper(key[:1]) + key[1:]
		if alias == key {
			t.Fatal("root alias fixture")
		}
		cases["root-wrong-case-"+key] = bytes.Replace(canonical, []byte(`"`+key+`":`), []byte(`"`+alias+`":`), 1)
		cases["root-alias-"+key] = appendJSONMember(canonical, `"`+alias+`":`+string(value))
		copy := cloneRawMembers(root)
		delete(copy, key)
		cases["root-missing-"+key], _ = json.Marshal(copy)
	}
	for key, value := range scoped {
		alias := strings.ToLower(key)
		wrong := bytes.Replace(root["scope"], []byte(`"`+key+`":`), []byte(`"`+alias+`":`), 1)
		copy := cloneRawMembers(root)
		copy["scope"] = wrong
		cases["scope-wrong-case-"+key], _ = json.Marshal(copy)
		copy["scope"] = appendJSONMember(root["scope"], `"`+alias+`":`+string(value))
		cases["scope-alias-"+key], _ = json.Marshal(copy)
		missing := cloneRawMembers(scoped)
		delete(missing, key)
		copy["scope"], _ = json.Marshal(missing)
		cases["scope-missing-"+key], _ = json.Marshal(copy)
	}
	for label, member := range map[string]string{"unknown-scope": `"extra":true`, "exact-duplicate-scope": `"LeaseID":"lease"`, "escaped-duplicate-scope": `"Lease\u0049D":"lease"`} {
		copy := cloneRawMembers(root)
		copy["scope"] = appendJSONMember(root["scope"], member)
		cases[label], _ = json.Marshal(copy)
	}
	// Reproduce the statically reviewed shape directly: old expiry followed by a
	// new wrong-case alias, and wrong original lease followed by a matching alias.
	old := bytes.Replace(canonical, []byte(`"expiresAt":"2099-01-01T00:00:00Z"`), []byte(`"expiresAt":"2001-01-01T00:00:00Z"`), 1)
	if bytes.Equal(old, canonical) {
		t.Fatal("expiry overwrite fixture not changed")
	}
	cases["expiry-overwrite-alias"] = appendJSONMember(old, `"ExpiresAt":"2099-01-01T00:00:00Z"`)
	copy := cloneRawMembers(root)
	badScope := bytes.Replace(root["scope"], []byte(`"LeaseID":"lease"`), []byte(`"LeaseID":"wrong"`), 1)
	if bytes.Equal(badScope, root["scope"]) {
		t.Fatal("lease overwrite fixture not changed")
	}
	copy["scope"] = appendJSONMember(badScope, `"leaseid":"lease"`)
	cases["lease-overwrite-alias"], _ = json.Marshal(copy)
	for name, metadata := range cases {
		t.Run(name, func(t *testing.T) {
			var length [4]byte
			binary.BigEndian.PutUint32(length[:], uint32(len(metadata)))
			blob := append([]byte(candidateFileMagic), length[:]...)
			blob = append(blob, metadata...)
			blob = append(blob, payload...)
			if e := os.WriteFile(path, blob, 0600); e != nil {
				t.Fatal(e)
			}
			m, e := files.Read(context.Background(), scope, ref.Ref, 2048)
			if name == "canonical" {
				if e != nil || !bytes.Equal(m.Raw, payload) || !m.ExpiresAt.Equal(expires) {
					t.Fatal("canonical roundtrip", e)
				}
			} else if e == nil || len(m.Raw) != 0 {
				t.Fatal("noncanonical/ambiguous metadata accepted", name)
			}
		})
	}
}
func cloneRawMembers(m map[string]json.RawMessage) map[string]json.RawMessage {
	n := map[string]json.RawMessage{}
	for k, v := range m {
		n[k] = append([]byte(nil), v...)
	}
	return n
}
func appendJSONMember(raw []byte, member string) []byte {
	return []byte(strings.TrimSuffix(string(raw), "}") + "," + member + "}")
}
