package chunk

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/go-kit/kit/log/level"
	"github.com/weaveworks/cortex/pkg/util"
)

// MockStorage is a fake in-memory StorageClient.
type MockStorage struct {
	mtx     sync.RWMutex
	tables  map[string]*mockTable
	objects map[string][]byte
}

type mockTable struct {
	items       map[string][]mockItem
	write, read int64
}

type mockItem struct {
	rangeValue []byte
	value      []byte
}

// NewMockStorage creates a new MockStorage.
func NewMockStorage() *MockStorage {
	return &MockStorage{
		tables:  map[string]*mockTable{},
		objects: map[string][]byte{},
	}
}

// ListTables implements StorageClient.
func (m *MockStorage) ListTables(_ context.Context) ([]string, error) {
	m.mtx.RLock()
	defer m.mtx.RUnlock()

	var tableNames []string
	for tableName := range m.tables {
		func(tableName string) {
			tableNames = append(tableNames, tableName)
		}(tableName)
	}
	return tableNames, nil
}

// CreateTable implements StorageClient.
func (m *MockStorage) CreateTable(_ context.Context, desc TableDesc) error {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	if _, ok := m.tables[desc.Name]; ok {
		return fmt.Errorf("table already exists")
	}

	m.tables[desc.Name] = &mockTable{
		items: map[string][]mockItem{},
		write: desc.ProvisionedWrite,
		read:  desc.ProvisionedRead,
	}

	return nil
}

// DescribeTable implements StorageClient.
func (m *MockStorage) DescribeTable(_ context.Context, name string) (desc TableDesc, status string, err error) {
	m.mtx.RLock()
	defer m.mtx.RUnlock()

	table, ok := m.tables[name]
	if !ok {
		return TableDesc{}, "", fmt.Errorf("not found")
	}

	return TableDesc{
		Name:             name,
		ProvisionedRead:  table.read,
		ProvisionedWrite: table.write,
	}, dynamodb.TableStatusActive, nil
}

// UpdateTable implements StorageClient.
func (m *MockStorage) UpdateTable(_ context.Context, _, desc TableDesc) error {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	table, ok := m.tables[desc.Name]
	if !ok {
		return fmt.Errorf("not found")
	}

	table.read = desc.ProvisionedRead
	table.write = desc.ProvisionedWrite

	return nil
}

// NewWriteBatch implements StorageClient.
func (m *MockStorage) NewWriteBatch() WriteBatch {
	return &mockWriteBatch{}
}

// BatchWrite implements StorageClient.
func (m *MockStorage) BatchWrite(ctx context.Context, batch WriteBatch) error {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	mockBatch := *batch.(*mockWriteBatch)
	seenWrites := map[string]bool{}

	for _, req := range mockBatch {
		table, ok := m.tables[req.tableName]
		if !ok {
			return fmt.Errorf("table not found")
		}

		// Check for duplicate writes by RangeKey in same batch
		key := fmt.Sprintf("%s:%s:%x", req.tableName, req.hashValue, req.rangeValue)
		if _, ok := seenWrites[key]; ok {
			return fmt.Errorf("Dupe write in batch")
		}
		seenWrites[key] = true

		level.Debug(util.WithContext(ctx, util.Logger)).Log("msg", "write", "hash", req.hashValue, "range", req.rangeValue)

		items := table.items[req.hashValue]

		// insert in order
		i := sort.Search(len(items), func(i int) bool {
			return bytes.Compare(items[i].rangeValue, req.rangeValue) >= 0
		})
		if i >= len(items) || !bytes.Equal(items[i].rangeValue, req.rangeValue) {
			items = append(items, mockItem{})
			copy(items[i+1:], items[i:])
		} else {
			// Return error if duplicate write and not metric name entry or series entry
			itemComponents := decodeRangeKey(items[i].rangeValue)
			if !bytes.Equal(itemComponents[3], metricNameRangeKeyV1) &&
				!bytes.Equal(itemComponents[3], seriesRangeKeyV1) {
				return fmt.Errorf("Dupe write")
			}
		}
		items[i] = mockItem{
			rangeValue: req.rangeValue,
			value:      req.value,
		}

		table.items[req.hashValue] = items
	}
	return nil
}

// QueryPages implements StorageClient.
func (m *MockStorage) QueryPages(ctx context.Context, query IndexQuery, callback func(result ReadBatch, lastPage bool) (shouldContinue bool)) error {
	logger := util.WithContext(ctx, util.Logger)
	m.mtx.RLock()
	defer m.mtx.RUnlock()

	table, ok := m.tables[query.TableName]
	if !ok {
		return fmt.Errorf("table not found")
	}

	items, ok := table.items[query.HashValue]
	if !ok {
		return nil
	}

	if query.RangeValuePrefix != nil {
		level.Debug(util.WithContext(ctx, logger)).Log("msg", "lookup prefix", "hash", query.HashValue, "range_prefix", query.RangeValuePrefix, "num_items", len(items))

		// the smallest index i in [0, n) at which f(i) is true
		i := sort.Search(len(items), func(i int) bool {
			if bytes.Compare(items[i].rangeValue, query.RangeValuePrefix) > 0 {
				return true
			}
			return bytes.HasPrefix(items[i].rangeValue, query.RangeValuePrefix)
		})
		j := sort.Search(len(items)-i, func(j int) bool {
			if bytes.Compare(items[i+j].rangeValue, query.RangeValuePrefix) < 0 {
				return false
			}
			return !bytes.HasPrefix(items[i+j].rangeValue, query.RangeValuePrefix)
		})

		level.Debug(util.WithContext(ctx, logger)).Log("msg", "found range", "from_inclusive", i, "to_exclusive", i+j)
		if i > len(items) || j == 0 {
			return nil
		}
		items = items[i : i+j]

	} else if query.RangeValueStart != nil {
		level.Debug(util.WithContext(ctx, logger)).Log("msg", "lookup range", "hash", query.HashValue, "range_start", query.RangeValueStart, "num_items", len(items))

		// the smallest index i in [0, n) at which f(i) is true
		i := sort.Search(len(items), func(i int) bool {
			return bytes.Compare(items[i].rangeValue, query.RangeValueStart) >= 0
		})

		level.Debug(util.WithContext(ctx, logger)).Log("msg", "found range [%d)", "index", i)
		if i > len(items) {
			return nil
		}
		items = items[i:]

	} else {
		level.Debug(util.WithContext(ctx, logger)).Log("msg", "lookup", "hash", query.HashValue, "num_items", len(items))
	}

	// Filters
	if query.ValueEqual != nil {
		level.Debug(util.WithContext(ctx, logger)).Log("msg", "filter by equality", "value_equal", query.ValueEqual)

		filtered := make([]mockItem, 0)
		for _, v := range items {
			if bytes.Equal(v.value, query.ValueEqual) {
				filtered = append(filtered, v)
			}
		}
		items = filtered
	}

	result := mockReadBatch{}
	for _, item := range items {
		result = append(result, item)
	}

	callback(result, true)
	return nil
}

// PutChunks implements StorageClient.
func (m *MockStorage) PutChunks(_ context.Context, chunks []Chunk) error {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	for i := range chunks {
		buf, err := chunks[i].Encode()
		if err != nil {
			return err
		}
		m.objects[chunks[i].ExternalKey()] = buf
	}
	return nil
}

// GetChunks implements StorageClient.
func (m *MockStorage) GetChunks(ctx context.Context, chunkSet []Chunk) ([]Chunk, error) {
	m.mtx.RLock()
	defer m.mtx.RUnlock()

	decodeContext := NewDecodeContext()
	result := []Chunk{}
	for _, chunk := range chunkSet {
		key := chunk.ExternalKey()
		buf, ok := m.objects[key]
		if !ok {
			return nil, fmt.Errorf("%v not found", key)
		}
		if err := chunk.Decode(decodeContext, buf); err != nil {
			return nil, err
		}
		result = append(result, chunk)
	}
	return result, nil
}

type mockWriteBatch []struct {
	tableName, hashValue string
	rangeValue           []byte
	value                []byte
}

func (b *mockWriteBatch) Add(tableName, hashValue string, rangeValue []byte, value []byte) {
	*b = append(*b, struct {
		tableName, hashValue string
		rangeValue           []byte
		value                []byte
	}{tableName, hashValue, rangeValue, value})
}

type mockReadBatch []mockItem

func (b mockReadBatch) Len() int {
	return len(b)
}

func (b mockReadBatch) RangeValue(i int) []byte {
	return b[i].rangeValue
}

func (b mockReadBatch) Value(i int) []byte {
	return b[i].value
}
