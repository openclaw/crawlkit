package remote

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"slices"
	"testing"
)

func inventoryTestValue(kind, value string) ChangeValue {
	return ChangeValue{Kind: kind, Value: &value}
}

func inventoryTestTables() []InventoryTable {
	return []InventoryTable{
		{Name: "rows", Columns: []InventoryColumn{{Name: "id", Kind: "integer"},
			{Name: "provider", Kind: "text", Nullable: true}, {Name: "body", Kind: "text", Nullable: true}}, KeyColumns: []string{"id"}},
		{Name: "empty", Columns: []InventoryColumn{{Name: "id", Kind: "integer"}}, KeyColumns: []string{"id"}},
	}
}

func inventoryTestCodec(t *testing.T) *InventoryCodec {
	t.Helper()
	c, err := NewInventoryCodec("issue/v1", "native/1", inventoryTestTables())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func inventoryTestNext(entries []InventoryEntry) func(context.Context) (InventoryEntry, error) {
	i := 0
	return func(context.Context) (InventoryEntry, error) {
		if i == len(entries) {
			return InventoryEntry{}, io.EOF
		}
		e := entries[i]
		i++
		return e, nil
	}
}

func inventoryTestEntry(t *testing.T, c *InventoryCodec, table string, key, row []ChangeValue) InventoryEntry {
	t.Helper()
	k, err := c.EncodeKey(table, key)
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.RowDigest(table, key, row)
	if err != nil {
		t.Fatal(err)
	}
	return InventoryEntry{Table: table, Key: k, RowSHA256: h}
}

// Literal vectors were independently framed with big-endian uint64 lengths.
// These are protocol fixtures, not hashes computed from the implementation.
func TestInventoryGolden(t *testing.T) {
	c := inventoryTestCodec(t)
	vectors := []struct {
		id      string
		body    ChangeValue
		keyHex  string
		rowHash string
	}{
		{"2", ChangeValue{Kind: "null"}, "0000000000000016637261776c2d696e76656e746f72792d6b65792d763180906eb26178938a4a8d5be7ada26a6fc51ce7c3870b047da5ca2bea1ed2748d0000000000000004726f7773000000000000000102000000000000000132", "a20205fe34e154b4a0405d1219f2d31b592ca008f1305f3ab955e571d2f53820"},
		{"-1", inventoryTestValue("text", "x\x00é"), "0000000000000016637261776c2d696e76656e746f72792d6b65792d763180906eb26178938a4a8d5be7ada26a6fc51ce7c3870b047da5ca2bea1ed2748d0000000000000004726f777300000000000000010200000000000000022d31", "67d9fb2feaaf4d0b15b19eb7928c8329e2463b0b52f2cba8885e791fb2ec9701"},
		{"10", inventoryTestValue("text", ""), "0000000000000016637261776c2d696e76656e746f72792d6b65792d763180906eb26178938a4a8d5be7ada26a6fc51ce7c3870b047da5ca2bea1ed2748d0000000000000004726f777300000000000000010200000000000000023130", "a58936b7a6eb65f5ff3ab66bd70128bf5a78493984d3058ff7a3f286bda4ec1d"},
	}
	var entries []InventoryEntry
	for _, v := range vectors {
		id := inventoryTestValue("integer", v.id)
		// Duplicate provider IDs do not merge different declared native keys.
		e := inventoryTestEntry(t, c, "rows", []ChangeValue{id}, []ChangeValue{id, inventoryTestValue("text", "duplicate"), v.body})
		if hex.EncodeToString(e.Key) != v.keyHex || hex.EncodeToString(e.RowSHA256[:]) != v.rowHash {
			t.Fatalf("golden %s: key=%x row=%x", v.id, e.Key, e.RowSHA256)
		}
		entries = append(entries, e)
	}
	if bytes.Compare(entries[0].Key, entries[1].Key) >= 0 || bytes.Compare(entries[1].Key, entries[2].Key) >= 0 {
		t.Fatal("encoded byte order must be 2,-1,10, not numeric or JSON lexical order")
	}
	counts := []InventoryCount{{Table: "rows", Rows: 3}, {Table: "empty", Rows: 0}}
	got, err := c.Digest(context.Background(), []byte("scope-A"), counts, InventoryLimits{3, 1024}, inventoryTestNext(entries))
	if err != nil || hex.EncodeToString(got[:]) != "1a0de348e0885b234f8f9be9a5db27a2a62c5c3144259011b9a3a86589662571" {
		t.Fatalf("inventory golden: %x %v", got, err)
	}
	got, err = c.Digest(context.Background(), []byte("scope-A"), []InventoryCount{{Table: "empty"}, {Table: "rows"}},
		InventoryLimits{0, 1024}, inventoryTestNext(nil))
	if err != nil || hex.EncodeToString(got[:]) != "d9d9c8ace2cf85be267555a079b8fbf4b1afbf3f85967da6c9078ec0187477eb" {
		t.Fatalf("explicit empty inventory: %x %v", got, err)
	}
}

func TestInventoryCompoundReusableLayoutGolden(t *testing.T) {
	c, err := NewInventoryCodec("chat/v1", "native/2", []InventoryTable{{Name: "messages",
		Columns: []InventoryColumn{{Name: "channel", Kind: "text", Nullable: true}, {Name: "native", Kind: "integer"},
			{Name: "external", Kind: "text", Nullable: true}, {Name: "body", Kind: "text"}}, KeyColumns: []string{"channel", "native"}}})
	if err != nil {
		t.Fatal(err)
	}
	vectors := []struct {
		channel       ChangeValue
		id, body, key string
		row           string
	}{
		{ChangeValue{Kind: "null"}, "-2", "hi", "0000000000000016637261776c2d696e76656e746f72792d6b65792d7631cd6767c69fa615689625b529ed3fcda17d996253418985eae73b268fee49b2b400000000000000086d657373616765730000000000000002000200000000000000022d32", "f3b7ccfd324309a357c592faaad8422e3c4fa907131317e22e8819c31d5155e0"},
		{inventoryTestValue("text", ""), "2", "", "0000000000000016637261776c2d696e76656e746f72792d6b65792d7631cd6767c69fa615689625b529ed3fcda17d996253418985eae73b268fee49b2b400000000000000086d65737361676573000000000000000201000000000000000002000000000000000132", "e88ca040e95c4355ae9d1696fb36000267cae0041d6ccec23a26cbb1faa7972c"},
		{inventoryTestValue("text", "a\x00"), "10", "hello", "0000000000000016637261776c2d696e76656e746f72792d6b65792d7631cd6767c69fa615689625b529ed3fcda17d996253418985eae73b268fee49b2b400000000000000086d65737361676573000000000000000201000000000000000261000200000000000000023130", "af7ae2e9ed685c2088e043bb787d1bdffdbff500950817884f874580e2a63f15"},
	}
	var entries []InventoryEntry
	for _, v := range vectors {
		id := inventoryTestValue("integer", v.id)
		e := inventoryTestEntry(t, c, "messages", []ChangeValue{v.channel, id},
			[]ChangeValue{v.channel, id, {Kind: "null"}, inventoryTestValue("text", v.body)})
		if hex.EncodeToString(e.Key) != v.key || hex.EncodeToString(e.RowSHA256[:]) != v.row {
			t.Fatalf("compound golden %s: key=%x row=%x", v.id, e.Key, e.RowSHA256)
		}
		entries = append(entries, e)
	}
	got, err := c.Digest(context.Background(), []byte("scope-B"), []InventoryCount{{Table: "messages", Rows: 3}},
		InventoryLimits{3, 1024}, inventoryTestNext(entries))
	if err != nil || hex.EncodeToString(got[:]) != "90e53587aa4f414e044b1310b7fe10c63aff94fcdd71fae7a688627137e9d474" {
		t.Fatalf("compound inventory: %x %v", got, err)
	}
}

func TestInventoryRejectsLayoutsAndRows(t *testing.T) {
	for name, mutate := range map[string]func([]InventoryTable){
		"duplicate table":  func(t []InventoryTable) { t[1].Name = t[0].Name },
		"duplicate column": func(t []InventoryTable) { t[0].Columns[1].Name = "id" },
		"duplicate key":    func(t []InventoryTable) { t[0].KeyColumns = []string{"id", "id"} },
		"unknown key":      func(t []InventoryTable) { t[0].KeyColumns = []string{"absent"} },
		"unsupported type": func(t []InventoryTable) { t[0].Columns[1].Kind = "blob" },
		"invalid metadata": func(t []InventoryTable) { t[0].Name = "bad\x00" },
	} {
		t.Run(name, func(t *testing.T) {
			tables := inventoryTestTables()
			mutate(tables)
			if c, err := NewInventoryCodec("issue/v1", "native/1", tables); err == nil || c != nil {
				t.Fatal("invalid layout accepted")
			}
		})
	}
	c := inventoryTestCodec(t)
	id := inventoryTestValue("integer", "2")
	row := []ChangeValue{id, {Kind: "null"}, inventoryTestValue("text", "")}
	for _, value := range []ChangeValue{inventoryTestValue("integer", "3"), inventoryTestValue("integer", "02"), inventoryTestValue("integer", "-0"),
		inventoryTestValue("integer", "+2"), inventoryTestValue("integer", "9223372036854775808"),
		inventoryTestValue("text", "2"), {Kind: "null"}, {Kind: "integer"}} {
		if got, err := c.RowDigest("rows", []ChangeValue{value}, row); err == nil || got != [32]byte{} {
			t.Fatal("invalid or mismatching key produced digest")
		}
	}
	for _, value := range []ChangeValue{inventoryTestValue("text", "\xff"), inventoryTestValue("blob", "x"),
		inventoryTestValue("null", ""), {Kind: "text"}} {
		bad := slices.Clone(row)
		bad[2] = value
		if got, err := c.RowDigest("rows", []ChangeValue{id}, bad); err == nil || got != [32]byte{} {
			t.Fatal("invalid scalar produced digest")
		}
	}
	for _, table := range []string{"unknown", "empty"} {
		if got, err := c.RowDigest(table, []ChangeValue{id}, row); err == nil || got != [32]byte{} {
			t.Fatal("unknown table or wrong row width produced digest")
		}
	}
	for _, integer := range []string{"-9223372036854775808", "9223372036854775807"} {
		if _, err := c.EncodeKey("rows", []ChangeValue{inventoryTestValue("integer", integer)}); err != nil {
			t.Fatal("valid int64 endpoint rejected", err)
		}
	}
}

func TestInventoryStreamValidation(t *testing.T) {
	c := inventoryTestCodec(t)
	makeEntry := func(id string) InventoryEntry {
		value := inventoryTestValue("integer", id)
		return inventoryTestEntry(t, c, "rows", []ChangeValue{value}, []ChangeValue{value, {Kind: "null"}, {Kind: "null"}})
	}
	a, b := makeEntry("2"), makeEntry("10")
	wrongVersion, wrongInteger := slices.Clone(a.Key), slices.Clone(a.Key)
	wrongVersion[8+len(inventoryKeyDomain)-1] = '9'
	wrongInteger[len(wrongInteger)-1] = 'x'
	counts := []InventoryCount{{Table: "rows", Rows: 2}, {Table: "empty"}}
	for name, entries := range map[string][]InventoryEntry{
		"out of order": {b, a}, "duplicate": {a, a}, "too few": {a}, "too many": {a, b, b},
		"wrong table":             {{Table: "empty", Key: a.Key}, b},
		"trailing key":            {{Table: "rows", Key: append(slices.Clone(a.Key), 0)}, b},
		"truncated key":           {{Table: "rows", Key: a.Key[:20]}, b},
		"unknown codec version":   {{Table: "rows", Key: wrongVersion}, b},
		"invalid encoded integer": {{Table: "rows", Key: wrongInteger}, b},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := c.Digest(context.Background(), []byte("scope"), counts, InventoryLimits{2, 1024}, inventoryTestNext(entries))
			if err == nil || got != [32]byte{} {
				t.Fatalf("invalid stream accepted: %x %v", got, err)
			}
		})
	}
	other, err := NewInventoryCodec("issue/v1", "native/changed", inventoryTestTables())
	if err != nil {
		t.Fatal(err)
	}
	for name, codec := range map[string]*InventoryCodec{"wrong layout": other, "nil codec": nil, "zero codec": {}} {
		t.Run(name, func(t *testing.T) {
			got, err := codec.Digest(context.Background(), []byte("scope"), counts, InventoryLimits{2, 1024}, inventoryTestNext([]InventoryEntry{a, b}))
			if err == nil || got != [32]byte{} {
				t.Fatal("wrong layout accepted")
			}
		})
	}
	for _, count := range [][]InventoryCount{{{Table: "rows", Rows: 2}}, {{Table: "rows", Rows: 2}, {Table: "rows"}},
		{{Table: "rows", Rows: 2}, {Table: "unknown"}}, {{Table: "rows", Rows: ^uint64(0)}, {Table: "empty", Rows: 1}}} {
		calls := 0
		got, err := c.Digest(context.Background(), []byte("scope"), count, InventoryLimits{2, 1024}, func(context.Context) (InventoryEntry, error) {
			calls++
			return InventoryEntry{}, io.EOF
		})
		if err == nil || got != [32]byte{} || calls != 0 {
			t.Fatal("invalid counts consumed stream or produced digest")
		}
	}
	for _, limits := range []InventoryLimits{{1, 1024}, {2, len(a.Key) - 1}, {2, 0}} {
		if got, err := c.Digest(context.Background(), []byte("scope"), counts, limits, inventoryTestNext([]InventoryEntry{a, b})); err == nil || got != [32]byte{} {
			t.Fatal("bounds ignored")
		}
	}
}

func TestInventoryCancellationEOFAndReusedStorage(t *testing.T) {
	c := inventoryTestCodec(t)
	counts := []InventoryCount{{Table: "rows", Rows: 2}, {Table: "empty"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, err := c.Digest(ctx, []byte("scope"), counts, InventoryLimits{2, 1024}, func(context.Context) (InventoryEntry, error) {
		calls++
		return InventoryEntry{}, io.EOF
	})
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("cancelled capture consumed input", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	got, err := c.Digest(ctx, []byte("scope"), counts, InventoryLimits{2, 1024}, func(context.Context) (InventoryEntry, error) {
		cancel()
		return InventoryEntry{}, nil
	})
	if !errors.Is(err, context.Canceled) || got != [32]byte{} {
		t.Fatal("callback cancellation produced digest", err)
	}
	empty := []InventoryCount{{Table: "rows"}, {Table: "empty"}}
	failure := errors.New("iterator failed")
	got, err = c.Digest(context.Background(), []byte("scope"), empty, InventoryLimits{0, 1024}, func(context.Context) (InventoryEntry, error) {
		return InventoryEntry{}, failure
	})
	if !errors.Is(err, failure) || got != [32]byte{} {
		t.Fatal("iterator failure was lost")
	}
	got, err = c.Digest(context.Background(), []byte("scope"), empty, InventoryLimits{0, 1024}, func(context.Context) (InventoryEntry, error) {
		return InventoryEntry{Table: "rows"}, io.EOF
	})
	if err == nil || got != [32]byte{} {
		t.Fatal("nonempty EOF was silently discarded")
	}
	var entries []InventoryEntry
	for _, id := range []string{"2", "10"} {
		value := inventoryTestValue("integer", id)
		entries = append(entries, inventoryTestEntry(t, c, "rows", []ChangeValue{value}, []ChangeValue{value, {Kind: "null"}, {Kind: "null"}}))
	}
	want, err := c.Digest(context.Background(), []byte("scope"), counts, InventoryLimits{2, 1024}, inventoryTestNext(entries))
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 0, 1024)
	calls = 0
	got, err = c.Digest(context.Background(), []byte("scope"), counts, InventoryLimits{2, 1024}, func(context.Context) (InventoryEntry, error) {
		calls++
		if calls == 3 {
			clear(buffer)
			return InventoryEntry{}, io.EOF
		}
		e := entries[calls-1]
		buffer = append(buffer[:0], e.Key...)
		e.Key = buffer
		return e, nil
	})
	if err != nil || got != want || calls != 3 {
		t.Fatal("iterator storage reuse changed result", err)
	}
}

func TestInventoryImmutableLayout(t *testing.T) {
	tables := inventoryTestTables()
	c, err := NewInventoryCodec("issue/v1", "native/1", tables)
	if err != nil {
		t.Fatal(err)
	}
	tables[0].Name = "changed"
	tables[0].Columns[0].Kind = "blob"
	tables[0].KeyColumns[0] = "body"
	for i := 0; i < 4; i++ {
		t.Run("concurrent", func(t *testing.T) {
			t.Parallel()
			id := inventoryTestValue("integer", "2")
			if _, err := c.RowDigest("rows", []ChangeValue{id}, []ChangeValue{id, {Kind: "null"}, {Kind: "null"}}); err != nil {
				t.Fatal("caller mutation changed codec", err)
			}
		})
	}
}
