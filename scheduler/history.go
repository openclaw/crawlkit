package scheduler

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

type RunRecord struct {
	ID         string   `json:"id"`
	Job        string   `json:"job"`
	Command    []string `json:"command"`
	Repo       string   `json:"repo,omitempty"`
	StartedAt  string   `json:"started_at"`
	FinishedAt string   `json:"finished_at"`
	DurationMs int64    `json:"duration_ms"`
	ExitCode   int      `json:"exit_code"`
	Status     string   `json:"status"`
	LogPath    string   `json:"log_path"`
	Error      string   `json:"error,omitempty"`
}

func appendHistory(path string, record RunRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// The scheduler lock serializes writes. Avoid O_APPEND: Windows append
	// handles lack the write-data access required for recovery truncation.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	return appendHistoryFile(file, record)
}

type historyFile interface {
	io.Writer
	io.ReaderAt
	io.Seeker
	Stat() (os.FileInfo, error)
	Truncate(int64) error
	Close() error
}

func appendHistoryFile(file historyFile, record RunRecord) (err error) {
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	start := info.Size()
	tailStart, tail, err := readHistoryTail(file, start)
	if err != nil {
		return err
	}
	if len(tail) > 0 {
		var previous RunRecord
		if err := json.Unmarshal(tail, &previous); err != nil {
			if !incompleteHistoryRecord(tail) {
				return err
			}
			if err := file.Truncate(tailStart); err != nil {
				return err
			}
			start = tailStart
			tail = nil
		}
	}
	var buf bytes.Buffer
	if len(tail) > 0 {
		// A valid EOF record may lack only its separator; retain every byte.
		buf.WriteByte('\n')
	}
	if err := json.NewEncoder(&buf).Encode(record); err != nil {
		return err
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return err
	}
	n, err := file.Write(buf.Bytes())
	if err == nil && n != buf.Len() {
		err = io.ErrShortWrite
	}
	if err != nil {
		return errors.Join(err, file.Truncate(start))
	}
	return nil
}

func readHistoryTail(file io.ReaderAt, end int64) (int64, []byte, error) {
	var tail []byte
	for end > 0 {
		start := max(int64(0), end-4096)
		block := make([]byte, end-start)
		if _, err := file.ReadAt(block, start); err != nil {
			return 0, nil, err
		}
		if n := bytes.LastIndexByte(block, '\n'); n >= 0 {
			return start + int64(n) + 1, append(block[n+1:], tail...), nil
		}
		tail = append(block, tail...)
		end = start
	}
	return 0, tail, nil
}

func incompleteHistoryRecord(data []byte) bool {
	var value json.RawMessage
	return errors.Is(json.NewDecoder(bytes.NewReader(data)).Decode(&value), io.ErrUnexpectedEOF)
}

func ReadHistory(path string) ([]RunRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	var records []RunRecord
	scanner := bufio.NewScanner(file)
	terminated := false
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		advance, token, err := bufio.ScanLines(data, atEOF)
		if advance > 0 {
			terminated = data[advance-1] == '\n'
		}
		return advance, token, err
	})
	for scanner.Scan() {
		var record RunRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			if !terminated && incompleteHistoryRecord(scanner.Bytes()) {
				break
			}
			return nil, err
		}
		records = append(records, record)
	}
	return records, scanner.Err()
}

func LastRecords(records []RunRecord) map[string]RunRecord {
	out := map[string]RunRecord{}
	for _, record := range records {
		key := record.Job
		if record.Repo != "" {
			key += ":" + record.Repo
		}
		out[key] = record
	}
	return out
}
