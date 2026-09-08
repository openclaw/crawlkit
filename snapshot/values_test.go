package snapshot

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func rawValuePack(t *testing.T, lines string) string {
	t.Helper()
	root := t.TempDir()
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	if _, err := gz.Write([]byte(lines)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "rows.jsonl.gz"), data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(root, Manifest{Version: 1, Tables: []TableManifest{{Name: "things", Files: []string{"rows.jsonl.gz"}}}}); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSnapshotNumberCallbackTypes(t *testing.T) {
	for _, tc := range []struct {
		token string
		want  any
	}{
		{"0", float64(0)},
		{"-0.0", math.Copysign(0, -1)},
		{"0e999999999", float64(0)},
		{"1", float64(1)},
		{"0.1", 0.1},
		{"1e3", float64(1000)},
		{"1e-100", 1e-100},
		{"5e-324", math.SmallestNonzeroFloat64},
		{"9007199254740991", float64(9007199254740991)},
		{"-9007199254740991", float64(-9007199254740991)},
		{"9007199254740992", int64(9007199254740992)},
		{"9007199254740993", int64(9007199254740993)},
		{"9007199254740993.0", int64(9007199254740993)},
		{"9.007199254740993e15", int64(9007199254740993)},
		{"-9.007199254740993E+15", int64(-9007199254740993)},
		{"9223372036854775807", int64(math.MaxInt64)},
		{"9223372036854775807.000", int64(math.MaxInt64)},
		{"-9223372036854775808", int64(math.MinInt64)},
	} {
		t.Run(tc.token, func(t *testing.T) {
			row, err := decodeSnapshotRow([]byte(`{"value":`+tc.token+`}`), false)
			if err != nil || !reflect.DeepEqual(row["value"], tc.want) {
				t.Fatalf("got %T(%v), err=%v; want %T(%v)", row["value"], row["value"], err, tc.want, tc.want)
			}
			if f, ok := tc.want.(float64); ok && f == 0 && math.Signbit(row["value"].(float64)) != math.Signbit(f) {
				t.Fatal("zero sign changed")
			}
		})
	}
	row, err := decodeSnapshotRow([]byte(`{"nested":[1,{"id":9007199254740993}],"text":"9007199254740993"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	nested := row["nested"].([]any)
	if nested[0] != float64(1) || nested[1].(map[string]any)["id"] != int64(9007199254740993) || row["text"] != "9007199254740993" {
		t.Fatalf("nested callback types: %#v", row)
	}
}

func TestImportExactIntegerBindings(t *testing.T) {
	root := rawValuePack(t, `{"id":1,"value":7}
{"id":9007199254740992,"value":9223372036854775807}
{"id":9007199254740993.0,"value":-9223372036854775808}
`)
	db := exportTestDB(t, `create table things(id integer primary key, value integer);`)
	calls := 0
	_, err := Import(context.Background(), ImportOptions{DB: db, RootDir: root,
		Filter: func(_ string, row map[string]any) (bool, error) {
			if row["id"] == float64(1) && row["value"] != float64(7) {
				t.Fatal("ordinary filter callback changed")
			}
			return true, nil
		},
		ImportRow: func(ctx context.Context, tx *sql.Tx, table string, row map[string]any) error {
			calls++
			if _, ok := row["id"].(json.Number); ok {
				t.Fatal("json.Number escaped into caller")
			}
			return insertRow(ctx, tx, table, row)
		}})
	if err != nil || calls != 3 {
		t.Fatalf("import calls=%d: %v", calls, err)
	}
	for id, expected := range map[int64]int64{1: 7, 9007199254740992: math.MaxInt64, 9007199254740993: math.MinInt64} {
		var got int64
		if err := db.QueryRow(`select value from things where id=?`, id).Scan(&got); err != nil || got != expected {
			t.Fatalf("binding id=%d value=%d: %v", id, got, err)
		}
	}
}

func TestImportInvalidValuesRollBackCallbacksAndDeletes(t *testing.T) {
	for _, value := range []string{
		"9223372036854775808", "-9223372036854775809", "100000000000000000000",
		"9007199254740993.1", "1e100", "1e9999999", "1e-10000",
		`"\ud800"`, `"\udc00"`, `"\ud800x"`, `"\ud800\u0041"`,
		"\"invalid\xfftext\"", "NaN", "Infinity", "01", "1 2",
	} {
		t.Run(value, func(t *testing.T) {
			root := rawValuePack(t, "{\"id\":1,\"value\":7}\n{\"id\":2,\"value\":"+value+"}\n")
			db := exportTestDB(t, `create table things(id integer primary key, value);
create table hooks(value text); insert into things values(5,'retained');`)
			_, err := Import(context.Background(), ImportOptions{DB: db, RootDir: root,
				BeforeImport: func(ctx context.Context, tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `insert into hooks values('before')`)
					return err
				},
				ImportRow: func(ctx context.Context, tx *sql.Tx, table string, row map[string]any) error {
					return insertRow(ctx, tx, table, row)
				}})
			if err == nil {
				t.Fatal("invalid/unrepresentable value imported")
			}
			var value string
			var count, hooks int
			if err := db.QueryRow(`select count(*), max(value) from things`).Scan(&count, &value); err != nil || count != 1 || value != "retained" {
				t.Fatalf("destination not rolled back: count=%d value=%q err=%v", count, value, err)
			}
			if err := db.QueryRow(`select count(*) from hooks`).Scan(&hooks); err != nil || hooks != 0 {
				t.Fatalf("callback transaction not rolled back: %d %v", hooks, err)
			}
		})
	}
}

func TestSnapshotTextValidation(t *testing.T) {
	for _, text := range []string{`"plain"`, `"valid\u00e9"`, `"\ud83d\ude00"`, `"\\ud800"`, `"\\\""`} {
		if _, err := decodeSnapshotRow([]byte(`{"value":`+text+`}`), false); err != nil {
			t.Fatalf("valid text %s: %v", text, err)
		}
	}
	for _, line := range []string{`{} {}`, `[]`, `1`, `{"value":1,}`, `{"value":1e}`} {
		if _, err := decodeSnapshotRow([]byte(line), false); err == nil {
			t.Fatalf("invalid row accepted: %s", line)
		}
	}
	if _, err := decodeSnapshotRow([]byte(`{"value":0.`+strings.Repeat("0", 4096)+`1}`), false); err == nil {
		t.Fatal("unbounded number accepted")
	}
}

func TestExportUnsupportedValuesPreservePack(t *testing.T) {
	for name, value := range map[string]any{
		"large integer":    int64(9007199254740993),
		"negative integer": int64(-9007199254740992),
		"large real":       float64(1e20),
		"blob":             []byte{0xff, 0},
		"empty blob":       []byte{},
		"text blob":        []byte("ascii"),
		"text":             "invalid\xfftext",
	} {
		t.Run(name, func(t *testing.T) {
			db := exportTestDB(t, `create table things(id integer primary key, value); insert into things values(1,'old');`)
			opts := ExportOptions{DB: db, RootDir: t.TempDir(), Tables: []string{"things"}}
			first, err := Export(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			before := packBytes(t, opts.RootDir, first)
			if _, err := db.Exec(`insert into things values(2,?)`, value); err != nil {
				t.Fatal(err)
			}
			if _, err := Export(context.Background(), opts); err == nil {
				t.Fatal("unsupported legacy value published")
			}
			if !reflect.DeepEqual(before, packBytes(t, opts.RootDir, first)) {
				t.Fatal("refused value damaged prior pack")
			}
			opts.Filter = func(_ string, row map[string]any) (bool, error) { return row["id"] == int64(1), nil }
			if _, err := Export(context.Background(), opts); err != nil {
				t.Fatalf("excluded value should not fail: %v", err)
			}
		})
	}
}

func TestExportExplicitFilterTransformations(t *testing.T) {
	db := exportTestDB(t, `create table things(id integer, blob, large integer, text);`)
	if _, err := db.Exec(`insert into things values(1,?,?,?)`, []byte{0xff, 0}, int64(9007199254740993), "bad\xff"); err != nil {
		t.Fatal(err)
	}
	opts := ExportOptions{DB: db, RootDir: t.TempDir(), Tables: []string{"things"},
		Filter: func(_ string, row map[string]any) (bool, error) {
			if _, ok := row["blob"].(string); !ok {
				t.Fatal("legacy export BLOB callback type changed")
			}
			row["blob"] = "base64:" + base64.StdEncoding.EncodeToString([]byte(row["blob"].(string)))
			row["large"] = fmt.Sprint(row["large"])
			row["text"] = "explicit replacement"
			return true, nil
		}}
	if _, err := Export(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	dst := exportTestDB(t, `create table things(id integer, blob, large, text);`)
	if _, err := Import(context.Background(), ImportOptions{DB: dst, RootDir: opts.RootDir}); err != nil {
		t.Fatal(err)
	}
	var blob, large, text string
	if err := dst.QueryRow(`select blob,large,text from things`).Scan(&blob, &large, &text); err != nil || blob != "base64:/wA=" || large != "9007199254740993" || text != "explicit replacement" {
		t.Fatalf("explicit representation: %q %q %q %v", blob, large, text, err)
	}
	opts.Filter = nil
	opts.FilterTx = func(context.Context, *sql.Tx, string, map[string]any) (bool, error) { return false, nil }
	if manifest, err := Export(context.Background(), opts); err != nil || manifest.Tables[0].Rows != 0 {
		t.Fatalf("transaction filter exclusion: %+v %v", manifest, err)
	}
	opts.FilterTx = func(_ context.Context, _ *sql.Tx, _ string, row map[string]any) (bool, error) {
		delete(row, "blob")
		delete(row, "large")
		delete(row, "text")
		return true, nil
	}
	if _, err := Export(context.Background(), opts); err != nil {
		t.Fatalf("transaction filter removal: %v", err)
	}
}

func TestSnapshotWriterReaderNumericAgreement(t *testing.T) {
	for _, value := range []any{float64(0), math.Copysign(0, -1), float64(7), 0.1, 1e-100, math.SmallestNonzeroFloat64, int64(maxExactJSONInteger)} {
		data, err := encodeSnapshotRow(map[string]any{"value": value}, nil)
		if err != nil {
			t.Fatalf("encode %v: %v", value, err)
		}
		row, err := decodeSnapshotRow(data, false)
		if err != nil {
			t.Fatalf("reader rejected writer output: %v", err)
		}
		got, ok := row["value"].(float64)
		want := reflect.ValueOf(value).Convert(reflect.TypeFor[float64]()).Float()
		if !ok || got != want || math.Signbit(got) != math.Signbit(want) {
			t.Fatalf("roundtrip %T(%v) -> %T(%v)", value, value, row["value"], row["value"])
		}
	}
	for _, value := range []any{
		int64(maxExactJSONInteger + 1), uint64(math.MaxUint64), float64(1e20),
		float64(1e100), math.MaxFloat64, math.Inf(1), math.NaN(),
		json.Number("9007199254740993.0"), json.Number("1e-10000"),
		[]byte{}, "bad\xff", []any{"bad\xff"}, map[string]any{"bad\xff": 1},
		struct{ Text string }{"bad\xff"}, json.RawMessage(`"\ud800"`),
	} {
		if _, err := encodeSnapshotRow(map[string]any{"value": value}, nil); err == nil {
			t.Fatalf("writer accepted unsupported %T(%v)", value, value)
		}
	}
}

func TestSnapshotFractionalBoundary(t *testing.T) {
	for _, token := range []string{
		"9007199254740991.1", "-9007199254740991.1",
		"9.0071992547409911e15", "-9.0071992547409911E+15",
		"90071992547409911e-1", "-90071992547409911e-1",
	} {
		t.Run(token, func(t *testing.T) {
			if _, err := decodeSnapshotRow([]byte(`{"value":`+token+`}`), false); err == nil {
				t.Error("reader checked rounded magnitude instead of exact fractional bound")
			}
			if _, err := encodeSnapshotRow(map[string]any{"value": json.Number(token)}, nil); err == nil {
				t.Error("writer checked rounded magnitude instead of exact fractional bound")
			}
		})
	}
	for token, want := range map[string]float64{
		"9007199254740990.9":      9007199254740991,
		"-9007199254740990.9":     -9007199254740991,
		"9.0071992547409909e15":   9007199254740991,
		"-9.0071992547409909E+15": -9007199254740991,
	} {
		data, err := encodeSnapshotRow(map[string]any{"value": json.Number(token)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		row, err := decodeSnapshotRow(data, false)
		if err != nil || row["value"] != want {
			t.Fatalf("ordinary in-range fractional float64 contract changed: %T(%v), %v", row["value"], row["value"], err)
		}
	}
}
