package parquet

import (
	"io"
	"unsafe"

	"github.com/segmentio/parquet-go/bloom"
	"github.com/segmentio/parquet-go/deprecated"
	"github.com/segmentio/parquet-go/encoding"
	"github.com/segmentio/parquet-go/format"
)

// BloomFilter is an interface allowing applications to test whether a key
// exists in a bloom filter.
type BloomFilter interface {
	// Implement the io.ReaderAt interface as a mechanism to allow reading the
	// raw bits of the filter.
	io.ReaderAt
	// Returns the size of the bloom filter (in bytes).
	Size() int64
	// Tests whether the key is present in the filter.
	Check(key []byte) (bool, error)
}

type bloomFilter struct {
	io.SectionReader
	hash  bloom.Hash
	check func(io.ReaderAt, int64, uint64) (bool, error)
}

func (f *bloomFilter) Check(key []byte) (bool, error) {
	return f.check(&f.SectionReader, f.Size(), f.hash.Sum64(key))
}

func newBloomFilter(file io.ReaderAt, offset int64, header *format.BloomFilterHeader) *bloomFilter {
	if header.Algorithm.Block != nil {
		if header.Hash.XxHash != nil {
			if header.Compression.Uncompressed != nil {
				return &bloomFilter{
					SectionReader: *io.NewSectionReader(file, offset, int64(header.NumBytes)),
					hash:          bloom.XXH64{},
					check:         bloom.CheckSplitBlock,
				}
			}
		}
	}
	return nil
}

// The BloomFilterColumn interface is a declarative representation of bloom filters
// used when configuring filters on a parquet writer.
type BloomFilterColumn interface {
	// Returns the path of the column that the filter applies to.
	Path() []string
	// Returns the hashing algorithm used when inserting keys into a bloom
	// filter.
	Hash() bloom.Hash
	// NewFilter constructs a new bloom filter configured to hold the given
	// number of values and bits of filter per value.
	NewFilter(numValues int64, bitsPerValue uint) bloom.MutableFilter
}

// SplitBlockFilter constructs a split block bloom filter object for the column
// at the given path.
func SplitBlockFilter(path ...string) BloomFilterColumn { return splitBlockFilter(path) }

type splitBlockFilter []string

func (f splitBlockFilter) Path() []string   { return f }
func (f splitBlockFilter) Hash() bloom.Hash { return bloom.XXH64{} }
func (f splitBlockFilter) NewFilter(numValues int64, bitsPerValue uint) bloom.MutableFilter {
	return make(bloom.SplitBlockFilter, bloom.NumSplitBlocksOf(numValues, bitsPerValue))
}

// Creates a header from the given bloom filter.
//
// For now there is only one type of filter supported, but we provide this
// function to suggest a model for extending the implementation if new filters
// are added to the parquet specs.
func bloomFilterHeader(filter BloomFilterColumn) (header format.BloomFilterHeader) {
	switch filter.(type) {
	case splitBlockFilter:
		header.Algorithm.Block = &format.SplitBlockAlgorithm{}
	}
	switch filter.Hash().(type) {
	case bloom.XXH64:
		header.Hash.XxHash = &format.XxHash{}
	}
	header.Compression.Uncompressed = &format.BloomFilterUncompressed{}
	return header
}

func searchBloomFilterColumn(filters []BloomFilterColumn, path columnPath) BloomFilterColumn {
	for _, f := range filters {
		if path.equal(f.Path()) {
			return f
		}
	}
	return nil
}

// bloomFilterEncoder is an adapter type which implements the encoding.Encoder
// interface on top of a bloom filter.
type bloomFilterEncoder struct {
	filter bloom.MutableFilter
	hash   bloom.Hash
	keys   [128]uint64
}

func newBloomFilterEncoder(filter bloom.MutableFilter, hash bloom.Hash) *bloomFilterEncoder {
	return &bloomFilterEncoder{filter: filter, hash: hash}
}

func (e *bloomFilterEncoder) Check(value []byte) bool {
	return e.filter.Check(e.hash.Sum64(value))
}

func (e *bloomFilterEncoder) Bytes() []byte {
	return e.filter.Bytes()
}

func (e *bloomFilterEncoder) Reset(io.Writer) {
	e.filter.Reset()
}

func (e *bloomFilterEncoder) SetBitWidth(int) {
}

func (e *bloomFilterEncoder) EncodeBoolean(data []bool) error {
	return e.insert8(*(*[]uint8)(unsafe.Pointer(&data)))
}

func (e *bloomFilterEncoder) EncodeInt8(data []int8) error {
	return e.insert8(*(*[]uint8)(unsafe.Pointer(&data)))
}

func (e *bloomFilterEncoder) EncodeInt16(data []int16) error {
	return e.insert16(*(*[]uint16)(unsafe.Pointer(&data)))
}

func (e *bloomFilterEncoder) EncodeInt32(data []int32) error {
	return e.insert32(*(*[]uint32)(unsafe.Pointer(&data)))
}

func (e *bloomFilterEncoder) EncodeInt64(data []int64) error {
	return e.insert64(*(*[]uint64)(unsafe.Pointer(&data)))
}

func (e *bloomFilterEncoder) EncodeInt96(data []deprecated.Int96) error {
	return e.EncodeFixedLenByteArray(12, deprecated.Int96ToBytes(data))
}

func (e *bloomFilterEncoder) EncodeFloat(data []float32) error {
	return e.insert32(*(*[]uint32)(unsafe.Pointer(&data)))
}

func (e *bloomFilterEncoder) EncodeDouble(data []float64) error {
	return e.insert64(*(*[]uint64)(unsafe.Pointer(&data)))
}

func (e *bloomFilterEncoder) EncodeByteArray(data encoding.ByteArrayList) error {
	data.Range(func(v []byte) bool { e.insert(v); return true })
	return nil
}

func (e *bloomFilterEncoder) EncodeFixedLenByteArray(size int, data []byte) error {
	if size == 16 {
		return e.insert128(*(*[][16]byte)(unsafe.Pointer(&data)))
	}
	for i, j := 0, size; j <= len(data); {
		e.insert(data[i:j])
		i += size
		j += size
	}
	return nil
}

func (e *bloomFilterEncoder) insert(value []byte) {
	e.filter.Insert(e.hash.Sum64(value))
}

func (e *bloomFilterEncoder) insert8(data []uint8) error {
	k := e.keys[:]
	for i := 0; i < len(data); {
		n := e.hash.MultiSum64Uint8(k, data[i:])
		e.filter.InsertBulk(k[:n:n])
		i += n
	}
	return nil
}

func (e *bloomFilterEncoder) insert16(data []uint16) error {
	k := e.keys[:]
	for i := 0; i < len(data); {
		n := e.hash.MultiSum64Uint16(k, data[i:])
		e.filter.InsertBulk(k[:n:n])
		i += n
	}
	return nil
}

func (e *bloomFilterEncoder) insert32(data []uint32) error {
	k := e.keys[:]
	for i := 0; i < len(data); {
		n := e.hash.MultiSum64Uint32(k, data[i:])
		e.filter.InsertBulk(k[:n:n])
		i += n
	}
	return nil
}

func (e *bloomFilterEncoder) insert64(data []uint64) error {
	k := e.keys[:]
	for i := 0; i < len(data); {
		n := e.hash.MultiSum64Uint64(k, data[i:])
		e.filter.InsertBulk(k[:n:n])
		i += n
	}
	return nil
}

func (e *bloomFilterEncoder) insert128(data [][16]byte) error {
	k := e.keys[:]
	for i := 0; i < len(data); {
		n := e.hash.MultiSum64Uint128(k, data[i:])
		e.filter.InsertBulk(k[:n:n])
		i += n
	}
	return nil
}