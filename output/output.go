package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type Format string

const (
	Text Format = "text"
	JSON Format = "json"
	Log  Format = "log"
)

type UsageError struct {
	Err error
}

func (e UsageError) Error() string {
	if e.Err == nil {
		return "usage error"
	}
	return e.Err.Error()
}

func (e UsageError) Unwrap() error {
	return e.Err
}

func Resolve(format string, jsonFlag bool) (Format, error) {
	if jsonFlag {
		return JSON, nil
	}
	switch Format(format) {
	case "", Text:
		return Text, nil
	case JSON:
		return JSON, nil
	case Log:
		return Log, nil
	default:
		return "", UsageError{Err: fmt.Errorf("unknown output format %q", format)}
	}
}

func Write(w io.Writer, format Format, label string, value any) error {
	switch format {
	case JSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(value)
	case Log:
		if label == "" {
			label = "result"
		}
		if !validLogLabel(label) {
			return UsageError{Err: fmt.Errorf("invalid log label %q", label)}
		}
		body, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "%s=%s\n", label, body)
		return err
	case "", Text:
		_, err := fmt.Fprintln(w, value)
		return err
	default:
		return UsageError{Err: fmt.Errorf("unknown output format %q", format)}
	}
}

func validLogLabel(label string) bool {
	for _, r := range label {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-' {
			continue
		}
		return false
	}
	return label != ""
}

func IsUsage(err error) bool {
	var usage UsageError
	return errors.As(err, &usage)
}
