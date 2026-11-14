package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const (
	PreservationChunkBytes    = 64 * 1024 * 1024
	preservationManifestBytes = 1024 * 1024
	preservationMaxChunks     = 256
)

type PreservationChunk struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type PreservationFile struct {
	Path   string              `json:"path"`
	SHA256 string              `json:"sha256"`
	Size   int64               `json:"size"`
	Chunks []PreservationChunk `json:"chunks"`
}

// PreservationManifest describes opaque original bytes, not a query projection.
type PreservationManifest struct {
	Version int                `json:"version"`
	App     string             `json:"app"`
	Archive string             `json:"archive"`
	Files   []PreservationFile `json:"files"`
}

// PreservationSource maps one regular source file to its portable manifest name.
// Sources must be closed, coherent backups; this API does not snapshot live databases.
type PreservationSource struct {
	Name, Path string
	// A positive Length selects exactly these bytes of the original file.
	// Zero Offset and Length select the entire file.
	Offset, Length int64
}

func preservationSourceBounds(source PreservationSource, size int64) (int64, int64, error) {
	if source.Offset < 0 || source.Length < 0 || source.Offset > size {
		return 0, 0, errors.New("invalid preservation source range")
	}
	if source.Length == 0 {
		if source.Offset != 0 {
			return 0, 0, errors.New("preservation source range requires a positive length")
		}
		return 0, size, nil
	}
	if source.Length > size-source.Offset {
		return 0, 0, errors.New("preservation source range exceeds file size")
	}
	return source.Offset, source.Length, nil
}

// PreservationReceipt proves upload identities, not database restore fidelity.
type PreservationReceipt struct {
	ManifestSHA256   string               `json:"manifest_sha256"`
	Manifest         PreservationManifest `json:"manifest"`
	RestoreQualified bool                 `json:"restore_qualified"`
}

var preservationApp = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var preservationPath = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)

func validatePreservationManifest(m PreservationManifest, app, archive string) error {
	if m.Version != 1 || m.App != app || m.Archive != archive || !preservationApp.MatchString(app) || archive == "" || len(archive) > 512 || strings.TrimSpace(archive) != archive || len(m.Files) < 1 || len(m.Files) > 64 {
		return errors.New("invalid preservation manifest scope or file count")
	}
	paths := make([]string, 0, len(m.Files))
	count := 0
	for _, file := range m.Files {
		if !validPreservationPath(file.Path) || file.Size < 1 || !validSQLiteBundleSHA256(file.SHA256, true) || len(file.Chunks) < 1 {
			return errors.New("invalid preservation file")
		}
		// Restore names must also be unambiguous on case-insensitive filesystems.
		name := strings.ToLower(file.Path)
		for _, prior := range paths {
			if name == prior || strings.HasPrefix(name, prior+"/") || strings.HasPrefix(prior, name+"/") {
				return errors.New("preservation paths overlap")
			}
		}
		paths = append(paths, name)
		count += len(file.Chunks)
		if count > preservationMaxChunks {
			return errors.New("preservation manifest exceeds 256 chunks")
		}
		var size int64
		for _, chunk := range file.Chunks {
			if chunk.Size < 1 || chunk.Size > PreservationChunkBytes || !validSQLiteBundleSHA256(chunk.SHA256, true) {
				return errors.New("invalid preservation chunk")
			}
			size += chunk.Size
		}
		if size != file.Size {
			return errors.New("preservation chunk sizes do not match file")
		}
	}
	return nil
}

func validPreservationPath(name string) bool {
	if len(name) > 240 || !preservationPath.MatchString(name) {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasSuffix(part, ".") {
			return false
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" ||
			(len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
			return false
		}
	}
	return true
}

// PlanPreservation hashes existing files without modifying, decompressing or copying them.
// chunkSize may be smaller than PreservationChunkBytes, but a plan cannot exceed 256 chunks.
func PlanPreservation(ctx context.Context, app, archive string, sources []PreservationSource, chunkSize int64) (PreservationManifest, error) {
	m := PreservationManifest{Version: 1, App: app, Archive: archive}
	if chunkSize < 1 || chunkSize > PreservationChunkBytes || len(sources) < 1 || len(sources) > 64 || !preservationApp.MatchString(app) || archive == "" || len(archive) > 512 || strings.TrimSpace(archive) != archive {
		return m, errors.New("invalid preservation source count or chunk size")
	}
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return m, err
		}
		file, err := planPreservationFile(ctx, source, chunkSize)
		if err != nil {
			return m, err
		}
		m.Files = append(m.Files, file)
		// Check accumulated bounds before hashing the next source.
		if err := validatePreservationManifest(m, app, archive); err != nil {
			return m, err
		}
	}
	if _, err := encodePreservationManifest(m); err != nil {
		return m, err
	}
	return m, nil
}

func planPreservationFile(ctx context.Context, source PreservationSource, chunkSize int64) (PreservationFile, error) {
	result := PreservationFile{Path: source.Name}
	if !validPreservationPath(source.Name) {
		return result, errors.New("invalid preservation source name")
	}
	f, err := os.Open(source.Path)
	if err != nil {
		return result, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 {
		return result, errors.New("preservation source must be a nonempty regular file")
	}
	offset, length, err := preservationSourceBounds(source, info.Size())
	if err != nil {
		return result, err
	}
	if (length-1)/chunkSize+1 > preservationMaxChunks {
		return result, errors.New("preservation source must be a nonempty regular file within the chunk limit")
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return result, err
	}
	whole := sha256.New()
	result.Size = length
	for remaining := length; remaining > 0; {
		size := min(remaining, chunkSize)
		part := sha256.New()
		n, err := copyWithContext(ctx, io.MultiWriter(whole, part), io.LimitReader(f, size))
		if err != nil {
			return result, err
		}
		if n != size {
			return result, errors.New("preservation source changed while planning")
		}
		result.Chunks = append(result.Chunks, PreservationChunk{SHA256: hex.EncodeToString(part.Sum(nil)), Size: size})
		remaining -= size
	}
	end, err := f.Stat()
	if err != nil {
		return result, err
	}
	if info.Size() != end.Size() || !info.ModTime().Equal(end.ModTime()) {
		return result, errors.New("preservation source changed while planning")
	}
	result.SHA256 = hex.EncodeToString(whole.Sum(nil))
	return result, nil
}

func encodePreservationManifest(m PreservationManifest) ([]byte, error) {
	if err := validatePreservationManifest(m, m.App, m.Archive); err != nil {
		return nil, err
	}
	body, err := json.Marshal(m)
	if err == nil && len(body) > preservationManifestBytes {
		err = errors.New("preservation manifest exceeds 1 MiB")
	}
	return body, err
}

func preservationDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// UploadPreservation verifies each bounded chunk again before upload and never publishes
// a manifest after changed source bytes or an unverified receipt. Repeating it is safe.
func (c *Client) UploadPreservation(ctx context.Context, m PreservationManifest, sources []PreservationSource) (PreservationReceipt, error) {
	body, err := encodePreservationManifest(m)
	if err != nil {
		return PreservationReceipt{}, err
	}
	if len(sources) != len(m.Files) {
		return PreservationReceipt{}, errors.New("preservation sources do not match manifest")
	}
	for i, source := range sources {
		if source.Name != m.Files[i].Path {
			return PreservationReceipt{}, errors.New("preservation source name does not match manifest")
		}
	}
	for i, source := range sources {
		if err := c.uploadPreservationFile(ctx, m, m.Files[i], source); err != nil {
			return PreservationReceipt{}, err
		}
	}
	digest := preservationDigest(body)
	resp, err := c.preservationRequest(ctx, m.App, m.Archive, "preservation-manifest", url.Values{"sha256": {digest}}, body)
	if err != nil {
		return PreservationReceipt{}, err
	}
	defer resp.Body.Close()
	var receipt struct {
		SHA256           string `json:"sha256"`
		Verification     string `json:"verification"`
		RestoreQualified *bool  `json:"restore_qualified"`
	}
	if err := decodePreservationResponse(resp.Body, &receipt); err != nil {
		return PreservationReceipt{}, err
	}
	if receipt.SHA256 != digest || receipt.Verification != "chunk-checksums" || receipt.RestoreQualified == nil || *receipt.RestoreQualified {
		return PreservationReceipt{}, errors.New("invalid preservation manifest receipt")
	}
	return PreservationReceipt{ManifestSHA256: digest, Manifest: m}, nil
}

func (c *Client) uploadPreservationFile(ctx context.Context, m PreservationManifest, file PreservationFile, source PreservationSource) error {
	f, err := os.Open(source.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("preservation source must be a regular file")
	}
	offset, length, err := preservationSourceBounds(source, info.Size())
	if err != nil {
		return err
	}
	if length != file.Size {
		return errors.New("preservation source size changed")
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	whole := sha256.New()
	for _, chunk := range file.Chunks {
		if err := ctx.Err(); err != nil {
			return err
		}
		// A bounded in-memory chunk makes bytes immutable across HTTP redirects/retries.
		body := make([]byte, int(chunk.Size))
		if _, err := io.ReadFull(f, body); err != nil {
			return err
		}
		if preservationDigest(body) != chunk.SHA256 {
			return errors.New("preservation source chunk changed")
		}
		_, _ = whole.Write(body)
		resp, err := c.preservationRequest(ctx, m.App, m.Archive, "preservation-chunk", url.Values{"sha256": {chunk.SHA256}}, body)
		if err != nil {
			return err
		}
		var receipt PreservationChunk
		err = decodePreservationResponse(resp.Body, &receipt)
		_ = resp.Body.Close()
		if err != nil {
			return err
		}
		if receipt != chunk {
			return errors.New("invalid preservation chunk receipt")
		}
	}
	end, err := f.Stat()
	if err != nil {
		return err
	}
	if end.Size() != info.Size() || !end.ModTime().Equal(info.ModTime()) || hex.EncodeToString(whole.Sum(nil)) != file.SHA256 {
		return errors.New("preservation source file changed")
	}
	return nil
}

func decodePreservationResponse(r io.Reader, dst any) error {
	body, err := io.ReadAll(io.LimitReader(r, preservationManifestBytes+1))
	if err != nil {
		return err
	}
	if len(body) > preservationManifestBytes {
		return errors.New("preservation response exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trailing preservation response data")
	}
	return nil
}

func (c *Client) preservationRequest(ctx context.Context, app, archive, action string, query url.Values, body []byte) (*http.Response, error) {
	u, err := url.Parse(c.url(archivePath(app, archive, action)))
	if err != nil {
		return nil, err
	}
	u.RawQuery = query.Encode()
	method := http.MethodGet
	if body != nil {
		method = http.MethodPut
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/octet-stream")
	resp, err := c.sendRequest(ctx, req, body != nil, true, true)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, decodeRemoteError(resp)
	}
	return resp, nil
}

// FetchPreservationManifest verifies the exact stored JSON digest before using paths.
func (c *Client) FetchPreservationManifest(ctx context.Context, app, archive, digest string) (PreservationManifest, error) {
	var m PreservationManifest
	if !validSQLiteBundleSHA256(digest, true) {
		return m, errors.New("invalid preservation manifest digest")
	}
	resp, err := c.preservationRequest(ctx, app, archive, "preservation-manifest", url.Values{"sha256": {digest}}, nil)
	if err != nil {
		return m, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, preservationManifestBytes+1))
	if err != nil {
		return m, err
	}
	if len(body) > preservationManifestBytes || preservationDigest(body) != digest {
		return m, errors.New("preservation manifest digest or size mismatch")
	}
	if err := decodePreservationResponse(bytes.NewReader(body), &m); err != nil {
		return m, err
	}
	return m, validatePreservationManifest(m, app, archive)
}

// DownloadPreservationFile writes the exact opaque file and verifies every chunk
// plus the whole-file digest. On failure dst can contain partial bytes; discard it.
// A verified file is not proof that its native database can be restored.
func (c *Client) DownloadPreservationFile(ctx context.Context, app, archive, digest string, fileIndex int, dst io.Writer) error {
	m, err := c.FetchPreservationManifest(ctx, app, archive, digest)
	if err != nil {
		return err
	}
	if fileIndex < 0 || fileIndex >= len(m.Files) || dst == nil {
		return errors.New("invalid preservation file index or destination")
	}
	file := m.Files[fileIndex]
	whole := sha256.New()
	for i, chunk := range file.Chunks {
		resp, err := c.preservationRequest(ctx, app, archive, "preservation-chunk", url.Values{"sha256": {digest}, "file": {strconv.Itoa(fileIndex)}, "chunk": {strconv.Itoa(i)}}, nil)
		if err != nil {
			return err
		}
		part := sha256.New()
		n, err := copyWithContext(ctx, io.MultiWriter(dst, whole, part), io.LimitReader(resp.Body, chunk.Size))
		if err == nil {
			var extra [1]byte
			count, tailErr := resp.Body.Read(extra[:])
			if count != 0 || tailErr != io.EOF {
				err = errors.New("preservation chunk has trailing bytes or incomplete stream")
			}
		}
		_ = resp.Body.Close()
		if err != nil {
			return err
		}
		if n != chunk.Size || hex.EncodeToString(part.Sum(nil)) != chunk.SHA256 {
			return errors.New("preservation chunk digest or size mismatch")
		}
	}
	if hex.EncodeToString(whole.Sum(nil)) != file.SHA256 {
		return fmt.Errorf("preservation file %d digest mismatch", fileIndex)
	}
	return nil
}
