package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/remote"
)

func (a *app) runPreservation(ctx context.Context, args []string) error {
	if len(args) == 0 || (args[0] != "upload" && args[0] != "download") {
		return usageError{errors.New("usage: crawlctl preservation <upload|download> [flags]")}
	}
	command := args[0]
	fs := flag.NewFlagSet("preservation "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	endpoint := fs.String("endpoint", "", "preservation service URL")
	appID := fs.String("app", "", "archive application")
	archive := fs.String("archive", "", "archive identifier")
	tokenEnv := fs.String("token-env", "CRAWL_REMOTE_TOKEN", "environment variable containing the admin token")
	timeout := fs.Duration("timeout", 5*time.Minute, "timeout per HTTP request")
	var files, ranges multiFlag
	var chunkSize int64
	var digest, output string
	if command == "upload" {
		fs.Var(&files, "file", "logical NAME=PATH of a closed backup file (repeatable)")
		fs.Var(&ranges, "file-range", "logical NAME=OFFSET:LENGTH:PATH of a closed backup file section (repeatable)")
		fs.Int64Var(&chunkSize, "chunk-size", remote.PreservationChunkBytes, "chunk size in bytes")
	} else {
		fs.StringVar(&digest, "manifest", "", "manifest SHA-256")
		fs.StringVar(&output, "output", "", "new output directory; must not exist")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(a.stdout)
			fs.PrintDefaults()
			return nil
		}
		return usageError{err}
	}
	if fs.NArg() != 0 {
		return usageError{errors.New("preservation commands do not accept positional arguments")}
	}
	if strings.TrimSpace(*endpoint) == "" || strings.TrimSpace(*appID) == "" || strings.TrimSpace(*archive) == "" || strings.TrimSpace(*tokenEnv) == "" || *timeout <= 0 {
		return usageError{errors.New("--endpoint, --app, --archive, --token-env and a positive --timeout are required")}
	}
	var sources []remote.PreservationSource
	if command == "upload" {
		if len(files)+len(ranges) == 0 || chunkSize < 1 || chunkSize > remote.PreservationChunkBytes {
			return usageError{fmt.Errorf("upload requires --file or --file-range and --chunk-size between 1 and %d", remote.PreservationChunkBytes)}
		}
		for _, value := range files {
			name, path, ok := strings.Cut(value, "=")
			if !ok || name == "" || path == "" {
				return usageError{errors.New("--file must be NAME=PATH with a nonempty name and path")}
			}
			sources = append(sources, remote.PreservationSource{Name: name, Path: path})
		}
		for _, value := range ranges {
			name, spec, ok := strings.Cut(value, "=")
			parts := strings.SplitN(spec, ":", 3)
			if !ok || name == "" || len(parts) != 3 || parts[2] == "" {
				return usageError{errors.New("--file-range must be NAME=OFFSET:LENGTH:PATH")}
			}
			offset, offsetErr := strconv.ParseInt(parts[0], 10, 64)
			length, lengthErr := strconv.ParseInt(parts[1], 10, 64)
			if offsetErr != nil || lengthErr != nil || offset < 0 || length <= 0 {
				return usageError{errors.New("--file-range requires a nonnegative offset and positive length")}
			}
			sources = append(sources, remote.PreservationSource{Name: name, Path: parts[2], Offset: offset, Length: length})
		}
	} else {
		if digest == "" || strings.TrimSpace(output) == "" {
			return usageError{errors.New("download requires --manifest SHA and --output DIR")}
		}
		if _, err := os.Lstat(output); err == nil {
			return fmt.Errorf("download output %q already exists", output)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	client, err := remote.NewClient(remote.Options{
		Endpoint:      *endpoint,
		HTTPClient:    &http.Client{Timeout: *timeout},
		TokenProvider: remote.EnvTokenProvider{Name: *tokenEnv},
	})
	if err != nil {
		return usageError{err}
	}
	if command == "upload" {
		manifest, err := remote.PlanPreservation(ctx, *appID, *archive, sources, chunkSize)
		if err != nil {
			return err
		}
		receipt, err := client.UploadPreservation(ctx, manifest, sources)
		if err != nil {
			return err
		}
		return writeJSON(a.stdout, receipt)
	}
	receipt, err := downloadPreservation(ctx, client, *appID, *archive, digest, output)
	if err != nil {
		return err
	}
	return writeJSON(a.stdout, struct {
		remote.PreservationReceipt
		FilesVerified bool `json:"files_verified"`
	}{PreservationReceipt: receipt, FilesVerified: true})
}

func downloadPreservation(ctx context.Context, client *remote.Client, appID, archive, digest, output string) (receipt remote.PreservationReceipt, retErr error) {
	manifest, err := client.FetchPreservationManifest(ctx, appID, archive, digest)
	if err != nil {
		return receipt, err
	}
	if err := os.Mkdir(output, 0o700); err != nil {
		return receipt, err
	}
	defer func() {
		if retErr != nil {
			retErr = fmt.Errorf("partial download retained at %q: %w", output, retErr)
		}
	}()
	root, err := os.OpenRoot(output)
	if err != nil {
		return receipt, err
	}
	defer root.Close()
	for index, entry := range manifest.Files {
		name := filepath.FromSlash(entry.Path)
		if err := root.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			return receipt, err
		}
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return receipt, err
		}
		err = client.DownloadPreservationFile(ctx, appID, archive, digest, index, file)
		if err == nil {
			err = file.Sync()
		}
		if err = errors.Join(err, file.Close()); err != nil {
			return receipt, fmt.Errorf("file %q: %w", entry.Path, err)
		}
	}
	return remote.PreservationReceipt{ManifestSHA256: digest, Manifest: manifest, RestoreQualified: false}, nil
}
