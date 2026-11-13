package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"slices"
)

// InventoryDigestVersion identifies this binary codec, not a native token or
// an advertised receiver capability. Existing v1 seed hashes are unchanged.
const InventoryDigestVersion = "crawl-inventory-v1"

const (
	inventoryLayoutDomain = "crawl-inventory-layout-v1"
	inventoryKeyDomain    = "crawl-inventory-key-v1"
	inventoryRowDomain    = "crawl-inventory-row-v1"
	inventorySetDomain    = "crawl-inventory-set-v1"
)

var ErrInventory = errors.New("invalid inventory layout, values, order or bounds")

// InventoryColumn declares a column's scalar type (text or integer) and whether
// SQL NULL is allowed. Applications own the layout and incremental eligibility.
type InventoryColumn struct {
	Name, Kind string
	Nullable   bool
}

// InventoryTable preserves declared column and primary-key column order. Keys
// name columns, not provider IDs inferred from row contents.
type InventoryTable struct {
	Name       string
	Columns    []InventoryColumn
	KeyColumns []string
}

type inventoryTable struct {
	InventoryTable
	keys []int
}

// InventoryCodec owns a validated immutable layout. It has no database, source
// policy, token migration or delivery state. Its methods may run concurrently.
type InventoryCodec struct {
	layout [sha256.Size]byte
	tables []inventoryTable
}

// NewInventoryCodec binds explicit profile/layout identities and the entire
// declared table/column/key layout. Metadata uses the remote contract's 128-byte
// nonempty UTF-8, NUL-free limit. Table declaration order is not significant.
func NewInventoryCodec(profile, layout string, tables []InventoryTable) (*InventoryCodec, error) {
	if !changeMetadata(profile, maxChangeIDBytes, false) || !changeMetadata(layout, maxChangeIDBytes, false) || len(tables) == 0 {
		return nil, ErrInventory
	}
	c := &InventoryCodec{}
	for _, table := range tables {
		if !changeMetadata(table.Name, maxChangeIDBytes, false) || len(table.Columns) == 0 || len(table.KeyColumns) == 0 {
			return nil, ErrInventory
		}
		t := inventoryTable{InventoryTable: InventoryTable{Name: table.Name,
			Columns: slices.Clone(table.Columns), KeyColumns: slices.Clone(table.KeyColumns)}}
		columns := make(map[string]int, len(t.Columns))
		for i, column := range t.Columns {
			if !changeMetadata(column.Name, maxChangeIDBytes, false) || (column.Kind != "text" && column.Kind != "integer") {
				return nil, ErrInventory
			}
			if _, exists := columns[column.Name]; exists {
				return nil, ErrInventory
			}
			columns[column.Name] = i
		}
		for _, key := range t.KeyColumns {
			i, exists := columns[key]
			if !exists || slices.Contains(t.keys, i) {
				return nil, ErrInventory
			}
			t.keys = append(t.keys, i)
		}
		c.tables = append(c.tables, t)
	}
	slices.SortFunc(c.tables, func(a, b inventoryTable) int { return bytes.Compare([]byte(a.Name), []byte(b.Name)) })
	h := sha256.New()
	inventoryFrame(h, []byte(inventoryLayoutDomain))
	inventoryFrame(h, []byte(profile))
	inventoryFrame(h, []byte(layout))
	inventoryUint(h, uint64(len(c.tables)))
	for i, t := range c.tables {
		if i > 0 && t.Name == c.tables[i-1].Name {
			return nil, ErrInventory
		}
		inventoryFrame(h, []byte(t.Name))
		inventoryUint(h, uint64(len(t.Columns)))
		for _, column := range t.Columns {
			inventoryFrame(h, []byte(column.Name))
			tag := byte(1)
			if column.Kind == "integer" {
				tag = 2
			}
			nullable := byte(0)
			if column.Nullable {
				nullable = 1
			}
			_, _ = h.Write([]byte{tag, nullable})
		}
		inventoryUint(h, uint64(len(t.keys)))
		for _, index := range t.keys {
			inventoryUint(h, uint64(index))
		}
	}
	copy(c.layout[:], h.Sum(nil))
	return c, nil
}

func (c *InventoryCodec) table(name string) (*inventoryTable, error) {
	if c == nil {
		return nil, ErrInventory
	}
	i, found := slices.BinarySearchFunc(c.tables, name, func(t inventoryTable, s string) int {
		return bytes.Compare([]byte(t.Name), []byte(s))
	})
	if !found {
		return nil, ErrInventory
	}
	return &c.tables[i], nil
}

// EncodeKey returns canonical bytes for caller-owned indexing and unsigned byte
// ordering. It validates declared types and binds the exact layout and table.
// Callers bound individual key/row inputs before materializing them.
func (c *InventoryCodec) EncodeKey(table string, key []ChangeValue) ([]byte, error) {
	t, err := c.table(table)
	if err != nil || len(key) != len(t.keys) {
		return nil, ErrInventory
	}
	for i, value := range key {
		if !inventoryValue(value, t.Columns[t.keys[i]]) {
			return nil, ErrInventory
		}
	}
	var b bytes.Buffer
	inventoryFrame(&b, []byte(inventoryKeyDomain))
	_, _ = b.Write(c.layout[:])
	inventoryFrame(&b, []byte(table))
	inventoryUint(&b, uint64(len(key)))
	for _, value := range key {
		inventoryCell(&b, value)
	}
	return b.Bytes(), nil
}

// RowDigest hashes one full row, including its declared key. It rejects key/row
// disagreement and type/layout mismatches. NULL and empty text remain distinct;
// text is exact valid UTF-8 (including NUL), integers are canonical signed int64.
func (c *InventoryCodec) RowDigest(table string, key, row []ChangeValue) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	t, err := c.table(table)
	if err != nil || len(row) != len(t.Columns) {
		return zero, ErrInventory
	}
	encoded, err := c.EncodeKey(table, key)
	if err != nil {
		return zero, err
	}
	for i, value := range row {
		if !inventoryValue(value, t.Columns[i]) {
			return zero, ErrInventory
		}
	}
	for i, column := range t.keys {
		if key[i].Kind != row[column].Kind || (key[i].Value == nil) != (row[column].Value == nil) ||
			(key[i].Value != nil && *key[i].Value != *row[column].Value) {
			return zero, ErrInventory
		}
	}
	h := sha256.New()
	inventoryFrame(h, []byte(inventoryRowDomain))
	_, _ = h.Write(c.layout[:])
	inventoryFrame(h, []byte(table))
	inventoryFrame(h, encoded)
	inventoryUint(h, uint64(len(row)))
	for _, value := range row {
		inventoryCell(h, value)
	}
	copy(zero[:], h.Sum(nil))
	return zero, nil
}

// InventoryCount explicitly declares each table, including empty tables.
type InventoryCount struct {
	Table string
	Rows  uint64
}

// InventoryEntry is compact derived evidence. Digest validates Key, not the
// provenance of RowSHA256; callers must compute it with RowDigest under the same
// source snapshot/layout and retain that binding in their own index.
type InventoryEntry struct {
	Table     string
	Key       []byte
	RowSHA256 [sha256.Size]byte
}

// InventoryLimits bounds the compact stream, not caller SQL time or row bodies.
// MaxEntries may be zero for an empty inventory; MaxKeyBytes must be positive.
type InventoryLimits struct {
	MaxEntries  uint64
	MaxKeyBytes int
}

// Digest consumes exactly counts plus EOF from next. Tables and encoded keys
// must be strictly increasing in unsigned byte order. It never sorts entries,
// reads row bodies, or returns a partial digest. Memory is O(layout + one key).
// next is synchronous, owns its source/query bounds, must honor ctx, and may reuse
// an entry's storage on its next invocation, but must not mutate it concurrently.
// Cancellation is checked before and after callbacks; a blocking callback must
// honor ctx itself.
// Scope is an explicit nonempty opaque byte identity; callers own its encoding.
func (c *InventoryCodec) Digest(ctx context.Context, scope []byte, counts []InventoryCount, limits InventoryLimits,
	next func(context.Context) (InventoryEntry, error),
) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if c == nil || len(c.tables) == 0 || len(scope) == 0 || len(counts) != len(c.tables) || limits.MaxKeyBytes <= 0 || next == nil {
		return zero, ErrInventory
	}
	byTable := make(map[string]uint64, len(counts))
	var total uint64
	for _, count := range counts {
		if _, err := c.table(count.Table); err != nil {
			return zero, err
		}
		if _, exists := byTable[count.Table]; exists || count.Rows > limits.MaxEntries-total {
			return zero, ErrInventory
		}
		byTable[count.Table] = count.Rows
		total += count.Rows
	}
	h := sha256.New()
	inventoryFrame(h, []byte(inventorySetDomain))
	_, _ = h.Write(c.layout[:])
	inventoryFrame(h, scope)
	inventoryUint(h, uint64(len(c.tables)))
	for _, table := range c.tables {
		inventoryFrame(h, []byte(table.Name))
		inventoryUint(h, byTable[table.Name])
		var previous []byte
		for i := uint64(0); i < byTable[table.Name]; i++ {
			if err := ctx.Err(); err != nil {
				return zero, err
			}
			entry, err := next(ctx)
			if err != nil {
				return zero, err
			}
			if err := ctx.Err(); err != nil {
				return zero, err
			}
			if entry.Table != table.Name || len(entry.Key) > limits.MaxKeyBytes || !c.validKey(table, entry.Key) ||
				(previous != nil && bytes.Compare(previous, entry.Key) >= 0) {
				return zero, ErrInventory
			}
			inventoryFrame(h, entry.Key)
			_, _ = h.Write(entry.RowSHA256[:])
			previous = append(previous[:0], entry.Key...)
		}
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	entry, err := next(ctx)
	if err != io.EOF {
		if err != nil {
			return zero, err
		}
		return zero, ErrInventory
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if entry.Table != "" || len(entry.Key) != 0 || entry.RowSHA256 != zero {
		return zero, ErrInventory
	}
	inventoryUint(h, total)
	copy(zero[:], h.Sum(nil))
	return zero, nil
}

func inventoryValue(value ChangeValue, column InventoryColumn) bool {
	return validateChangeValue(value) == nil && ((value.Kind == "null" && column.Nullable) || value.Kind == column.Kind)
}

func inventoryUint(w io.Writer, n uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], n)
	_, _ = w.Write(b[:])
}

func inventoryFrame(w io.Writer, b []byte) {
	inventoryUint(w, uint64(len(b)))
	_, _ = w.Write(b)
}

func inventoryCell(w io.Writer, value ChangeValue) {
	if value.Kind == "null" {
		_, _ = w.Write([]byte{0})
		return
	}
	tag := byte(1)
	if value.Kind == "integer" {
		tag = 2
	}
	_, _ = w.Write([]byte{tag})
	inventoryUint(w, uint64(len(*value.Value)))
	_, _ = io.WriteString(w, *value.Value)
}

func inventoryTake(b *[]byte, n uint64) ([]byte, bool) {
	if n > uint64(len(*b)) {
		return nil, false
	}
	value := (*b)[:int(n)]
	*b = (*b)[int(n):]
	return value, true
}

func inventoryReadUint(b *[]byte) (uint64, bool) {
	value, ok := inventoryTake(b, 8)
	if !ok {
		return 0, false
	}
	return binary.BigEndian.Uint64(value), true
}

func inventoryReadFrame(b *[]byte) ([]byte, bool) {
	n, ok := inventoryReadUint(b)
	if !ok {
		return nil, false
	}
	return inventoryTake(b, n)
}

func (c *InventoryCodec) validKey(table inventoryTable, b []byte) bool {
	domain, ok := inventoryReadFrame(&b)
	if !ok || string(domain) != inventoryKeyDomain {
		return false
	}
	layout, ok := inventoryTake(&b, sha256.Size)
	if !ok || !bytes.Equal(layout, c.layout[:]) {
		return false
	}
	name, ok := inventoryReadFrame(&b)
	if !ok || string(name) != table.Name {
		return false
	}
	n, ok := inventoryReadUint(&b)
	if !ok || n != uint64(len(table.keys)) {
		return false
	}
	for _, index := range table.keys {
		tag, ok := inventoryTake(&b, 1)
		if !ok || tag[0] > 2 {
			return false
		}
		value := ChangeValue{Kind: "null"}
		if tag[0] != 0 {
			raw, ok := inventoryReadFrame(&b)
			if !ok {
				return false
			}
			text := string(raw)
			value = ChangeValue{Kind: "text", Value: &text}
			if tag[0] == 2 {
				value.Kind = "integer"
			}
		}
		if !inventoryValue(value, table.Columns[index]) {
			return false
		}
	}
	return len(b) == 0
}
