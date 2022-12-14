package parquet

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"math/bits"
	"sort"

	"github.com/segmentio/encoding/thrift"
	"github.com/segmentio/parquet-go/compress"
	"github.com/segmentio/parquet-go/encoding"
	"github.com/segmentio/parquet-go/encoding/plain"
	"github.com/segmentio/parquet-go/format"
	"github.com/segmentio/parquet-go/internal/ioext"
)

// Deprecated: A Writer uses a parquet schema and sequence of Go values to
// produce a parquet file to an io.Writer.
//
// This example showcases a typical use of parquet writers:
//
//	writer := parquet.NewWriter(output)
//
//	for _, row := range rows {
//		if err := writer.Write(row); err != nil {
//			...
//		}
//	}
//
//	if err := writer.Close(); err != nil {
//		...
//	}
//
// The Writer type optimizes for minimal memory usage, each page is written as
// soon as it has been filled so only a single page per column needs to be held
// in memory and as a result, there are no opportunities to sort rows within an
// entire row group. Programs that need to produce parquet files with sorted
// row groups should use the Buffer type to buffer and sort the rows prior to
// writing them to a Writer.
//
// For programs building with Go 1.18 or later, the GenericWriter[T] type
// supersedes this one.
type Writer struct {
	output io.Writer
	config *WriterConfig
	schema *Schema
	writer *writer
	rowbuf []Row
}

// NewWriter constructs a parquet writer writing a file to the given io.Writer.
//
// The function panics if the writer configuration is invalid. Programs that
// cannot guarantee the validity of the options passed to NewWriter should
// construct the writer configuration independently prior to calling this
// function:
//
//	config, err := parquet.NewWriterConfig(options...)
//	if err != nil {
//		// handle the configuration error
//		...
//	} else {
//		// this call to create a writer is guaranteed not to panic
//		writer := parquet.NewWriter(output, config)
//		...
//	}
func NewWriter(output io.Writer, options ...WriterOption) *Writer {
	config, err := NewWriterConfig(options...)
	if err != nil {
		panic(err)
	}
	w := &Writer{
		output: output,
		config: config,
	}
	if config.Schema != nil {
		w.configure(config.Schema)
	}
	return w
}

func (w *Writer) configure(schema *Schema) {
	if schema != nil {
		w.config.Schema = schema
		w.schema = schema
		w.writer = newWriter(w.output, w.config)
	}
}

// Close must be called after all values were produced to the writer in order to
// flush all buffers and write the parquet footer.
func (w *Writer) Close() error {
	if w.writer != nil {
		return w.writer.close()
	}
	return nil
}

// Flush flushes all buffers into a row group to the underlying io.Writer.
//
// Flush is called automatically on Close, it is only useful to call explicitly
// if the application needs to limit the size of row groups or wants to produce
// multiple row groups per file.
//
// If the writer attempts to create more than MaxRowGroups row groups the method
// returns ErrTooManyRowGroups.
func (w *Writer) Flush() error {
	if w.writer != nil {
		return w.writer.flush()
	}
	return nil
}

// Reset clears the state of the writer without flushing any of the buffers,
// and setting the output to the io.Writer passed as argument, allowing the
// writer to be reused to produce another parquet file.
//
// Reset may be called at any time, including after a writer was closed.
func (w *Writer) Reset(output io.Writer) {
	if w.output = output; w.writer != nil {
		w.writer.reset(w.output)
	}
}

// Write is called to write another row to the parquet file.
//
// The method uses the parquet schema configured on w to traverse the Go value
// and decompose it into a set of columns and values. If no schema were passed
// to NewWriter, it is deducted from the Go type of the row, which then have to
// be a struct or pointer to struct.
func (w *Writer) Write(row interface{}) error {
	if w.schema == nil {
		w.configure(SchemaOf(row))
	}
	if cap(w.rowbuf) == 0 {
		w.rowbuf = make([]Row, 1)
	} else {
		w.rowbuf = w.rowbuf[:1]
	}
	defer clearRows(w.rowbuf)
	w.rowbuf[0] = w.schema.Deconstruct(w.rowbuf[0][:0], row)
	_, err := w.WriteRows(w.rowbuf)
	return err
}

// WriteRows is called to write rows to the parquet file.
//
// The Writer must have been given a schema when NewWriter was called, otherwise
// the structure of the parquet file cannot be determined from the row only.
//
// The row is expected to contain values for each column of the writer's schema,
// in the order produced by the parquet.(*Schema).Deconstruct method.
func (w *Writer) WriteRows(rows []Row) (int, error) {
	return w.writer.WriteRows(rows)
}

// WriteRowGroup writes a row group to the parquet file.
//
// Buffered rows will be flushed prior to writing rows from the group, unless
// the row group was empty in which case nothing is written to the file.
//
// The content of the row group is flushed to the writer; after the method
// returns successfully, the row group will be empty and in ready to be reused.
func (w *Writer) WriteRowGroup(rowGroup RowGroup) (int64, error) {
	rowGroupSchema := rowGroup.Schema()
	switch {
	case rowGroupSchema == nil:
		return 0, ErrRowGroupSchemaMissing
	case w.schema == nil:
		w.configure(rowGroupSchema)
	case !nodesAreEqual(w.schema, rowGroupSchema):
		return 0, ErrRowGroupSchemaMismatch
	}
	if err := w.writer.flush(); err != nil {
		return 0, err
	}
	w.writer.configureBloomFilters(rowGroup.ColumnChunks())
	rows := rowGroup.Rows()
	defer rows.Close()
	n, err := CopyRows(w.writer, rows)
	if err != nil {
		return n, err
	}
	return w.writer.writeRowGroup(rowGroup.Schema(), rowGroup.SortingColumns())
}

// ReadRowsFrom reads rows from the reader passed as arguments and writes them
// to w.
//
// This is similar to calling WriteRow repeatedly, but will be more efficient
// if optimizations are supported by the reader.
func (w *Writer) ReadRowsFrom(rows RowReader) (written int64, err error) {
	if w.schema == nil {
		if r, ok := rows.(RowReaderWithSchema); ok {
			w.configure(r.Schema())
		}
	}
	if cap(w.rowbuf) < defaultRowBufferSize {
		w.rowbuf = make([]Row, defaultRowBufferSize)
	} else {
		w.rowbuf = w.rowbuf[:cap(w.rowbuf)]
	}
	return copyRows(w.writer, rows, w.rowbuf)
}

// Schema returns the schema of rows written by w.
//
// The returned value will be nil if no schema has yet been configured on w.
func (w *Writer) Schema() *Schema { return w.schema }

// SetKeyValueMetadata sets a key/value pair in the Parquet file metadata.
//
// Keys are assumed to be unique, if the same key is repeated multiple times the
// last value is retained. While the parquet format does not require unique keys,
// this design decision was made to optimize for the most common use case where
// applications leverage this extension mechanism to associate single values to
// keys. This may create incompatibilities with other parquet libraries, or may
// cause some key/value pairs to be lost when open parquet files written with
// repeated keys. We can revisit this decision if it ever becomes a blocker.
func (w *Writer) SetKeyValueMetadata(key, value string) {
	for i, kv := range w.writer.metadata {
		if kv.Key == key {
			kv.Value = value
			w.writer.metadata[i] = kv
			return
		}
	}
	w.writer.metadata = append(w.writer.metadata, format.KeyValue{
		Key:   key,
		Value: value,
	})
}

type writer struct {
	buffer  *bufio.Writer
	writer  ioext.OffsetTrackingWriter
	values  [][]Value
	numRows int64
	maxRows int64

	createdBy string
	metadata  []format.KeyValue

	columns     []*writerColumn
	columnChunk []format.ColumnChunk
	columnIndex []format.ColumnIndex
	offsetIndex []format.OffsetIndex

	columnOrders   []format.ColumnOrder
	schemaElements []format.SchemaElement
	rowGroups      []format.RowGroup
	columnIndexes  [][]format.ColumnIndex
	offsetIndexes  [][]format.OffsetIndex
	sortingColumns []format.SortingColumn
}

func newWriter(output io.Writer, config *WriterConfig) *writer {
	w := new(writer)
	if config.WriteBufferSize <= 0 {
		w.writer.Reset(output)
	} else {
		w.buffer = bufio.NewWriterSize(output, config.WriteBufferSize)
		w.writer.Reset(w.buffer)
	}
	w.maxRows = config.MaxRowsPerRowGroup
	w.createdBy = config.CreatedBy
	w.metadata = make([]format.KeyValue, 0, len(config.KeyValueMetadata))
	for k, v := range config.KeyValueMetadata {
		w.metadata = append(w.metadata, format.KeyValue{Key: k, Value: v})
	}
	format.SortKeyValueMetadata(w.metadata)
	w.sortingColumns = make([]format.SortingColumn, len(config.Sorting.SortingColumns))

	config.Schema.forEachNode(func(name string, node Node) {
		nodeType := node.Type()

		repetitionType := (*format.FieldRepetitionType)(nil)
		if node != config.Schema { // the root has no repetition type
			repetitionType = fieldRepetitionTypePtrOf(node)
		}

		// For backward compatibility with older readers, the parquet specification
		// recommends to set the scale and precision on schema elements when the
		// column is of logical type decimal.
		logicalType := nodeType.LogicalType()
		scale, precision := (*int32)(nil), (*int32)(nil)
		if logicalType != nil && logicalType.Decimal != nil {
			scale = &logicalType.Decimal.Scale
			precision = &logicalType.Decimal.Precision
		}

		typeLength := (*int32)(nil)
		if n := int32(nodeType.Length()); n > 0 {
			typeLength = &n
		}

		w.schemaElements = append(w.schemaElements, format.SchemaElement{
			Type:           nodeType.PhysicalType(),
			TypeLength:     typeLength,
			RepetitionType: repetitionType,
			Name:           name,
			NumChildren:    int32(len(node.Fields())),
			ConvertedType:  nodeType.ConvertedType(),
			Scale:          scale,
			Precision:      precision,
			LogicalType:    logicalType,
		})
	})

	dataPageType := format.DataPage
	if config.DataPageVersion == 2 {
		dataPageType = format.DataPageV2
	}

	defaultCompression := config.Compression
	if defaultCompression == nil {
		defaultCompression = &Uncompressed
	}

	// Those buffers are scratch space used to generate the page header and
	// content, they are shared by all column chunks because they are only
	// used during calls to writeDictionaryPage or writeDataPage, which are
	// not done concurrently.
	buffers := new(writerBuffers)

	forEachLeafColumnOf(config.Schema, func(leaf leafColumn) {
		encoding := encodingOf(leaf.node)
		dictionary := Dictionary(nil)
		columnType := leaf.node.Type()
		columnIndex := int(leaf.columnIndex)
		compression := leaf.node.Compression()

		if compression == nil {
			compression = defaultCompression
		}

		if isDictionaryEncoding(encoding) {
			dictBuffer := columnType.NewValues(
				make([]byte, 0, defaultDictBufferSize),
				nil,
			)
			dictionary = columnType.NewDictionary(columnIndex, 0, dictBuffer)
			columnType = dictionary.Type()
		}

		c := &writerColumn{
			buffers:            buffers,
			pool:               config.ColumnPageBuffers,
			columnPath:         leaf.path,
			columnType:         columnType,
			columnIndex:        columnType.NewColumnIndexer(config.ColumnIndexSizeLimit),
			columnFilter:       searchBloomFilterColumn(config.BloomFilters, leaf.path),
			compression:        compression,
			dictionary:         dictionary,
			dataPageType:       dataPageType,
			maxRepetitionLevel: leaf.maxRepetitionLevel,
			maxDefinitionLevel: leaf.maxDefinitionLevel,
			bufferIndex:        int32(leaf.columnIndex),
			bufferSize:         int32(float64(config.PageBufferSize) * 0.98),
			writePageStats:     config.DataPageStatistics,
			encodings:          make([]format.Encoding, 0, 3),
			// Data pages in version 2 can omit compression when dictionary
			// encoding is employed; only the dictionary page needs to be
			// compressed, the data pages are encoded with the hybrid
			// RLE/Bit-Pack encoding which doesn't benefit from an extra
			// compression layer.
			isCompressed: isCompressed(compression) && (dataPageType != format.DataPageV2 || dictionary == nil),
		}

		c.header.encoder.Reset(c.header.protocol.NewWriter(&buffers.header))

		if leaf.maxDefinitionLevel > 0 {
			c.encodings = addEncoding(c.encodings, format.RLE)
		}

		if isDictionaryEncoding(encoding) {
			c.encodings = addEncoding(c.encodings, format.Plain)
		}

		c.encoding = encoding
		c.encodings = addEncoding(c.encodings, c.encoding.Encoding())
		sortPageEncodings(c.encodings)

		w.columns = append(w.columns, c)

		if sortingIndex := searchSortingColumn(config.Sorting.SortingColumns, leaf.path); sortingIndex < len(w.sortingColumns) {
			w.sortingColumns[sortingIndex] = format.SortingColumn{
				ColumnIdx:  int32(leaf.columnIndex),
				Descending: config.Sorting.SortingColumns[sortingIndex].Descending(),
				NullsFirst: config.Sorting.SortingColumns[sortingIndex].NullsFirst(),
			}
		}
	})

	// Pre-allocate the backing array so that in most cases where the rows
	// contain a single value we will hit collocated memory areas when writing
	// rows to the writer. This won't benefit repeated columns much but in that
	// case we would just waste a bit of memory which we can afford.
	values := make([]Value, len(w.columns))
	w.values = make([][]Value, len(w.columns))
	for i := range values {
		w.values[i] = values[i : i : i+1]
	}

	w.columnChunk = make([]format.ColumnChunk, len(w.columns))
	w.columnIndex = make([]format.ColumnIndex, len(w.columns))
	w.offsetIndex = make([]format.OffsetIndex, len(w.columns))
	w.columnOrders = make([]format.ColumnOrder, len(w.columns))

	for i, c := range w.columns {
		w.columnChunk[i] = format.ColumnChunk{
			MetaData: format.ColumnMetaData{
				Type:             format.Type(c.columnType.Kind()),
				Encoding:         c.encodings,
				PathInSchema:     c.columnPath,
				Codec:            c.compression.CompressionCodec(),
				KeyValueMetadata: nil, // TODO
			},
		}
	}

	for i, c := range w.columns {
		c.columnChunk = &w.columnChunk[i]
		c.offsetIndex = &w.offsetIndex[i]
	}

	for i, c := range w.columns {
		w.columnOrders[i] = *c.columnType.ColumnOrder()
	}

	return w
}

func (w *writer) reset(writer io.Writer) {
	if w.buffer == nil {
		w.writer.Reset(writer)
	} else {
		w.buffer.Reset(writer)
		w.writer.Reset(w.buffer)
	}
	for _, c := range w.columns {
		c.reset()
	}
	for i := range w.rowGroups {
		w.rowGroups[i] = format.RowGroup{}
	}
	for i := range w.columnIndexes {
		w.columnIndexes[i] = nil
	}
	for i := range w.offsetIndexes {
		w.offsetIndexes[i] = nil
	}
	w.rowGroups = w.rowGroups[:0]
	w.columnIndexes = w.columnIndexes[:0]
	w.offsetIndexes = w.offsetIndexes[:0]
}

func (w *writer) close() error {
	if err := w.writeFileHeader(); err != nil {
		return err
	}
	if err := w.flush(); err != nil {
		return err
	}
	if err := w.writeFileFooter(); err != nil {
		return err
	}
	if w.buffer != nil {
		return w.buffer.Flush()
	}
	return nil
}

func (w *writer) flush() error {
	_, err := w.writeRowGroup(nil, nil)
	return err
}

func (w *writer) writeFileHeader() error {
	if w.writer.Writer() == nil {
		return io.ErrClosedPipe
	}
	if w.writer.Offset() == 0 {
		_, err := w.writer.WriteString("PAR1")
		return err
	}
	return nil
}

func (w *writer) configureBloomFilters(columnChunks []ColumnChunk) {
	for i, c := range w.columns {
		if c.columnFilter != nil {
			c.resizeBloomFilter(columnChunks[i].NumValues())
		}
	}
}

func (w *writer) writeFileFooter() error {
	// The page index is composed of two sections: column and offset indexes.
	// They are written after the row groups, right before the footer (which
	// is written by the parent Writer.Close call).
	//
	// This section both writes the page index and generates the values of
	// ColumnIndexOffset, ColumnIndexLength, OffsetIndexOffset, and
	// OffsetIndexLength in the corresponding columns of the file metadata.
	//
	// Note: the page index is always written, even if we created data pages v1
	// because the parquet format is backward compatible in this case. Older
	// readers will simply ignore this section since they do not know how to
	// decode its content, nor have loaded any metadata to reference it.
	protocol := new(thrift.CompactProtocol)
	encoder := thrift.NewEncoder(protocol.NewWriter(&w.writer))

	for i, columnIndexes := range w.columnIndexes {
		rowGroup := &w.rowGroups[i]
		for j := range columnIndexes {
			column := &rowGroup.Columns[j]
			column.ColumnIndexOffset = w.writer.Offset()
			if err := encoder.Encode(&columnIndexes[j]); err != nil {
				return err
			}
			column.ColumnIndexLength = int32(w.writer.Offset() - column.ColumnIndexOffset)
		}
	}

	for i, offsetIndexes := range w.offsetIndexes {
		rowGroup := &w.rowGroups[i]
		for j := range offsetIndexes {
			column := &rowGroup.Columns[j]
			column.OffsetIndexOffset = w.writer.Offset()
			if err := encoder.Encode(&offsetIndexes[j]); err != nil {
				return err
			}
			column.OffsetIndexLength = int32(w.writer.Offset() - column.OffsetIndexOffset)
		}
	}

	numRows := int64(0)
	for rowGroupIndex := range w.rowGroups {
		numRows += w.rowGroups[rowGroupIndex].NumRows
	}

	footer, err := thrift.Marshal(new(thrift.CompactProtocol), &format.FileMetaData{
		Version:          1,
		Schema:           w.schemaElements,
		NumRows:          numRows,
		RowGroups:        w.rowGroups,
		KeyValueMetadata: w.metadata,
		CreatedBy:        w.createdBy,
		ColumnOrders:     w.columnOrders,
	})
	if err != nil {
		return err
	}

	length := len(footer)
	footer = append(footer, 0, 0, 0, 0)
	footer = append(footer, "PAR1"...)
	binary.LittleEndian.PutUint32(footer[length:], uint32(length))

	_, err = w.writer.Write(footer)
	return err
}

func (w *writer) writeRowGroup(rowGroupSchema *Schema, rowGroupSortingColumns []SortingColumn) (int64, error) {
	numRows := w.columns[0].totalRowCount()
	if numRows == 0 {
		return 0, nil
	}

	if len(w.rowGroups) == MaxRowGroups {
		return 0, ErrTooManyRowGroups
	}

	defer func() {
		w.numRows = 0
		for _, c := range w.columns {
			c.reset()
		}
		for i := range w.columnIndex {
			w.columnIndex[i] = format.ColumnIndex{}
		}
	}()

	for _, c := range w.columns {
		if err := c.flush(); err != nil {
			return 0, err
		}
		if err := c.flushFilterPages(); err != nil {
			return 0, err
		}
	}

	if err := w.writeFileHeader(); err != nil {
		return 0, err
	}
	fileOffset := w.writer.Offset()

	for _, c := range w.columns {
		if len(c.filter) > 0 {
			c.columnChunk.MetaData.BloomFilterOffset = w.writer.Offset()
			if err := c.writeBloomFilter(&w.writer); err != nil {
				return 0, err
			}
		}
	}

	for i, c := range w.columns {
		w.columnIndex[i] = format.ColumnIndex(c.columnIndex.ColumnIndex())

		if c.dictionary != nil {
			c.columnChunk.MetaData.DictionaryPageOffset = w.writer.Offset()
			if err := c.writeDictionaryPage(&w.writer, c.dictionary); err != nil {
				return 0, fmt.Errorf("writing dictionary page of row group colum %d: %w", i, err)
			}
		}

		dataPageOffset := w.writer.Offset()
		c.columnChunk.MetaData.DataPageOffset = dataPageOffset
		for j := range c.offsetIndex.PageLocations {
			c.offsetIndex.PageLocations[j].Offset += dataPageOffset
		}

		for _, page := range c.pages {
			if _, err := io.Copy(&w.writer, page); err != nil {
				return 0, fmt.Errorf("writing buffered pages of row group column %d: %w", i, err)
			}
		}
	}

	totalByteSize := int64(0)
	totalCompressedSize := int64(0)

	for i := range w.columnChunk {
		c := &w.columnChunk[i].MetaData
		sortPageEncodingStats(c.EncodingStats)
		totalByteSize += int64(c.TotalUncompressedSize)
		totalCompressedSize += int64(c.TotalCompressedSize)
	}

	sortingColumns := w.sortingColumns
	if len(sortingColumns) == 0 && len(rowGroupSortingColumns) > 0 {
		sortingColumns = make([]format.SortingColumn, 0, len(rowGroupSortingColumns))
		forEachLeafColumnOf(rowGroupSchema, func(leaf leafColumn) {
			if sortingIndex := searchSortingColumn(rowGroupSortingColumns, leaf.path); sortingIndex < len(sortingColumns) {
				sortingColumns[sortingIndex] = format.SortingColumn{
					ColumnIdx:  int32(leaf.columnIndex),
					Descending: rowGroupSortingColumns[sortingIndex].Descending(),
					NullsFirst: rowGroupSortingColumns[sortingIndex].NullsFirst(),
				}
			}
		})
	}

	columns := make([]format.ColumnChunk, len(w.columnChunk))
	copy(columns, w.columnChunk)

	columnIndex := make([]format.ColumnIndex, len(w.columnIndex))
	copy(columnIndex, w.columnIndex)

	offsetIndex := make([]format.OffsetIndex, len(w.offsetIndex))
	copy(offsetIndex, w.offsetIndex)

	for i := range columns {
		c := &columns[i]
		c.MetaData.EncodingStats = make([]format.PageEncodingStats, len(c.MetaData.EncodingStats))
		copy(c.MetaData.EncodingStats, w.columnChunk[i].MetaData.EncodingStats)
	}

	for i := range offsetIndex {
		c := &offsetIndex[i]
		c.PageLocations = make([]format.PageLocation, len(c.PageLocations))
		copy(c.PageLocations, w.offsetIndex[i].PageLocations)
	}

	w.rowGroups = append(w.rowGroups, format.RowGroup{
		Columns:             columns,
		TotalByteSize:       totalByteSize,
		NumRows:             numRows,
		SortingColumns:      sortingColumns,
		FileOffset:          fileOffset,
		TotalCompressedSize: totalCompressedSize,
		Ordinal:             int16(len(w.rowGroups)),
	})

	w.columnIndexes = append(w.columnIndexes, columnIndex)
	w.offsetIndexes = append(w.offsetIndexes, offsetIndex)
	return numRows, nil
}

func (w *writer) WriteRows(rows []Row) (int, error) {
	return w.writeRows(len(rows), func(start, end int) (int, error) {
		defer func() {
			for i, values := range w.values {
				clearValues(values)
				w.values[i] = values[:0]
			}
		}()

		// TODO: if an error occurs in this method the writer may be left in an
		// partially functional state. Applications are not expected to continue
		// using the writer after getting an error, but maybe we could ensure that
		// we are preventing further use as well?
		for _, row := range rows[start:end] {
			row.Range(func(columnIndex int, columnValues []Value) bool {
				w.values[columnIndex] = append(w.values[columnIndex], columnValues...)
				return true
			})
		}

		for i, values := range w.values {
			if len(values) > 0 {
				if err := w.columns[i].writeRows(values); err != nil {
					return 0, err
				}
			}
		}

		return end - start, nil
	})
}

func (w *writer) writeRows(numRows int, write func(i, j int) (int, error)) (int, error) {
	written := 0

	for written < numRows {
		remain := w.maxRows - w.numRows
		length := numRows - written

		if remain == 0 {
			remain = w.maxRows

			if err := w.flush(); err != nil {
				return written, err
			}
		}

		if remain < int64(length) {
			length = int(remain)
		}

		// Since the writer cannot flush pages across row boundaries, calls to
		// WriteRows with very large slices can result in greatly exceeding the
		// target page size. To set a limit to the impact of these large writes
		// we chunk the input in slices of 64 rows.
		//
		// Note that this mechanism isn't perfect; for example, values may hold
		// large byte slices which could still cause the column buffers to grow
		// beyond the target page size.
		const maxRowsPerWrite = 64
		if length > maxRowsPerWrite {
			length = maxRowsPerWrite
		}

		n, err := write(written, written+length)
		written += n
		w.numRows += int64(n)
		if err != nil {
			return written, err
		}
	}

	return written, nil
}

// The WriteValues method is intended to work in pair with WritePage to allow
// programs to target writing values to specific columns of of the writer.
func (w *writer) WriteValues(values []Value) (numValues int, err error) {
	return w.columns[values[0].Column()].WriteValues(values)
}

// One writerBuffers is used by each writer instance, the memory buffers here
// are shared by all columns of the writer because serialization is not done
// concurrently, which helps keep memory utilization low, both in the total
// footprint and GC cost.
//
// The type also exposes helper methods to facilitate the generation of parquet
// pages. A scratch space is used when serialization requires combining multiple
// buffers or compressing the page data, with double-buffering technique being
// employed by swapping the scratch and page buffers to minimize memory copies.
type writerBuffers struct {
	header      bytes.Buffer // buffer where page headers are encoded
	repetitions []byte       // buffer used to encode repetition levels
	definitions []byte       // buffer used to encode definition levels
	page        []byte       // page buffer holding the page data
	scratch     []byte       // scratch space used for compression
}

func (wb *writerBuffers) crc32() (checksum uint32) {
	checksum = crc32.Update(checksum, crc32.IEEETable, wb.repetitions)
	checksum = crc32.Update(checksum, crc32.IEEETable, wb.definitions)
	checksum = crc32.Update(checksum, crc32.IEEETable, wb.page)
	return checksum
}

func (wb *writerBuffers) size() int {
	return len(wb.repetitions) + len(wb.definitions) + len(wb.page)
}

func (wb *writerBuffers) reset() {
	wb.repetitions = wb.repetitions[:0]
	wb.definitions = wb.definitions[:0]
	wb.page = wb.page[:0]
}

func encodeLevels(dst, src []byte, maxLevel byte) ([]byte, error) {
	bitWidth := bits.Len8(maxLevel)
	return levelEncodingsRLE[bitWidth-1].EncodeLevels(dst, src)
}

func (wb *writerBuffers) encodeRepetitionLevels(page Page, maxRepetitionLevel byte) (err error) {
	wb.repetitions, err = encodeLevels(wb.repetitions, page.RepetitionLevels(), maxRepetitionLevel)
	return
}

func (wb *writerBuffers) encodeDefinitionLevels(page Page, maxDefinitionLevel byte) (err error) {
	wb.definitions, err = encodeLevels(wb.definitions, page.DefinitionLevels(), maxDefinitionLevel)
	return
}

func (wb *writerBuffers) prependLevelsToDataPageV1(maxRepetitionLevel, maxDefinitionLevel byte) {
	hasRepetitionLevels := maxRepetitionLevel > 0
	hasDefinitionLevels := maxDefinitionLevel > 0

	if hasRepetitionLevels || hasDefinitionLevels {
		wb.scratch = wb.scratch[:0]
		// In data pages v1, the repetition and definition levels are prefixed
		// with the 4 bytes length of the sections. While the parquet-format
		// documentation indicates that the length prefix is part of the hybrid
		// RLE/Bit-Pack encoding, this is the only condition where it is used
		// so we treat it as a special case rather than implementing it in the
		// encoding.
		//
		// Reference https://github.com/apache/parquet-format/blob/master/Encodings.md#run-length-encoding--bit-packing-hybrid-rle--3
		if hasRepetitionLevels {
			wb.scratch = plain.AppendInt32(wb.scratch, int32(len(wb.repetitions)))
			wb.scratch = append(wb.scratch, wb.repetitions...)
			wb.repetitions = wb.repetitions[:0]
		}
		if hasDefinitionLevels {
			wb.scratch = plain.AppendInt32(wb.scratch, int32(len(wb.definitions)))
			wb.scratch = append(wb.scratch, wb.definitions...)
			wb.definitions = wb.definitions[:0]
		}
		wb.scratch = append(wb.scratch, wb.page...)
		wb.swapPageAndScratchBuffers()
	}
}

func (wb *writerBuffers) encode(page Page, enc encoding.Encoding) (err error) {
	pageType := page.Type()
	pageData := page.Data()
	wb.page, err = pageType.Encode(wb.page[:0], pageData, enc)
	return err
}

func (wb *writerBuffers) compress(codec compress.Codec) (err error) {
	wb.scratch, err = codec.Encode(wb.scratch[:0], wb.page)
	wb.swapPageAndScratchBuffers()
	return err
}

func (wb *writerBuffers) swapPageAndScratchBuffers() {
	wb.page, wb.scratch = wb.scratch, wb.page[:0]
}

type writerColumn struct {
	pool  BufferPool
	pages []io.ReadWriteSeeker

	columnPath   columnPath
	columnType   Type
	columnIndex  ColumnIndexer
	columnBuffer ColumnBuffer
	columnFilter BloomFilterColumn
	encoding     encoding.Encoding
	compression  compress.Codec
	dictionary   Dictionary

	dataPageType       format.PageType
	maxRepetitionLevel byte
	maxDefinitionLevel byte

	buffers *writerBuffers

	header struct {
		protocol thrift.CompactProtocol
		encoder  thrift.Encoder
	}

	filter         []byte
	numRows        int64
	bufferIndex    int32
	bufferSize     int32
	writePageStats bool
	isCompressed   bool
	encodings      []format.Encoding

	columnChunk *format.ColumnChunk
	offsetIndex *format.OffsetIndex
}

func (c *writerColumn) reset() {
	if c.columnBuffer != nil {
		c.columnBuffer.Reset()
	}
	if c.columnIndex != nil {
		c.columnIndex.Reset()
	}
	if c.dictionary != nil {
		c.dictionary.Reset()
	}
	for _, page := range c.pages {
		c.pool.PutBuffer(page)
	}
	for i := range c.pages {
		c.pages[i] = nil
	}
	c.pages = c.pages[:0]
	// Bloom filters may change in size between row groups, but we retain the
	// buffer to avoid reallocating large memory blocks.
	c.filter = c.filter[:0]
	c.numRows = 0
	// Reset the fields of column chunks that change between row groups,
	// but keep the ones that remain unchanged.
	c.columnChunk.MetaData.NumValues = 0
	c.columnChunk.MetaData.TotalUncompressedSize = 0
	c.columnChunk.MetaData.TotalCompressedSize = 0
	c.columnChunk.MetaData.DataPageOffset = 0
	c.columnChunk.MetaData.DictionaryPageOffset = 0
	c.columnChunk.MetaData.Statistics = format.Statistics{}
	c.columnChunk.MetaData.EncodingStats = c.columnChunk.MetaData.EncodingStats[:0]
	c.columnChunk.MetaData.BloomFilterOffset = 0
	c.offsetIndex.PageLocations = c.offsetIndex.PageLocations[:0]
}

func (c *writerColumn) totalRowCount() int64 {
	n := c.numRows
	if c.columnBuffer != nil {
		n += int64(c.columnBuffer.Len())
	}
	return n
}

func (c *writerColumn) flush() (err error) {
	if c.columnBuffer.Len() > 0 {
		defer c.columnBuffer.Reset()
		_, err = c.writeDataPage(c.columnBuffer.Page())
	}
	return err
}

func (c *writerColumn) flushFilterPages() error {
	if c.columnFilter == nil {
		return nil
	}

	// If there is a dictionary, it contains all the values that we need to
	// write to the filter.
	if dict := c.dictionary; dict != nil {
		// Need to always attempt to resize the filter, as the writer might
		// be reused after resetting which would have reset the length of
		// the filter to 0.
		c.resizeBloomFilter(int64(dict.Len()))
		return c.writePageToFilter(dict.Page())
	}

	// When the filter was already allocated, pages have been written to it as
	// they were seen by the column writer.
	if len(c.filter) > 0 {
		return nil
	}

	// When the filter was not allocated, the writer did not know how many
	// values were going to be seen and therefore could not properly size the
	// filter ahead of time. In this case, we read back all the pages that we
	// have encoded and copy their values back to the filter.
	//
	// A prior implementation of the column writer used to create in-memory
	// copies of the pages to avoid this decoding step; however, this unbounded
	// allocation caused memory exhaustion in production applications. CPU being
	// a somewhat more stretchable resource, we prefer spending time on this
	// decoding step than having to trigger incident response when production
	// systems are getting OOM-Killed.
	c.resizeBloomFilter(c.columnChunk.MetaData.NumValues)

	column := &Column{
		// Set all the fields required by the decodeDataPage* methods.
		typ:                c.columnType,
		encoding:           c.encoding,
		compression:        c.compression,
		maxRepetitionLevel: c.maxRepetitionLevel,
		maxDefinitionLevel: c.maxDefinitionLevel,
		index:              int16(c.bufferIndex),
	}

	rbuf, pool := getBufioReader(nil, 1024)
	pbuf := (*buffer)(nil)
	defer func() {
		putBufioReader(rbuf, pool)
		if pbuf != nil {
			pbuf.unref()
		}
	}()

	decoder := thrift.NewDecoder(c.header.protocol.NewReader(rbuf))

	for _, p := range c.pages {
		rbuf.Reset(p)

		header := new(format.PageHeader)
		if err := decoder.Decode(header); err != nil {
			return err
		}

		if pbuf != nil {
			pbuf.unref()
		}
		pbuf = buffers.get(int(header.CompressedPageSize))
		if _, err := io.ReadFull(rbuf, pbuf.data); err != nil {
			return err
		}
		if _, err := p.Seek(0, io.SeekStart); err != nil {
			return err
		}

		var page Page
		var err error

		switch header.Type {
		case format.DataPage:
			page, err = column.decodeDataPageV1(DataPageHeaderV1{header.DataPageHeader}, pbuf, nil, header.UncompressedPageSize)
		case format.DataPageV2:
			page, err = column.decodeDataPageV2(DataPageHeaderV2{header.DataPageHeaderV2}, pbuf, nil, header.UncompressedPageSize)
		}
		if page != nil {
			err = c.writePageToFilter(page)
			Release(page)
		}
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *writerColumn) resizeBloomFilter(numValues int64) {
	filterSize := c.columnFilter.Size(numValues)
	if cap(c.filter) < filterSize {
		c.filter = make([]byte, filterSize)
	} else {
		c.filter = c.filter[:filterSize]
		for i := range c.filter {
			c.filter[i] = 0
		}
	}
}

func (c *writerColumn) newColumnBuffer() ColumnBuffer {
	column := c.columnType.NewColumnBuffer(int(c.bufferIndex), c.columnType.EstimateNumValues(int(c.bufferSize)))
	switch {
	case c.maxRepetitionLevel > 0:
		column = newRepeatedColumnBuffer(column, c.maxRepetitionLevel, c.maxDefinitionLevel, nullsGoLast)
	case c.maxDefinitionLevel > 0:
		column = newOptionalColumnBuffer(column, c.maxDefinitionLevel, nullsGoLast)
	}
	return column
}

func (c *writerColumn) writeRows(rows []Value) error {
	if c.columnBuffer == nil {
		// Lazily create the row group column so we don't need to allocate it if
		// rows are not written individually to the column.
		c.columnBuffer = c.newColumnBuffer()
	}
	if _, err := c.columnBuffer.WriteValues(rows); err != nil {
		return err
	}
	if c.columnBuffer.Size() >= int64(c.bufferSize) {
		return c.flush()
	}
	return nil
}

func (c *writerColumn) WriteValues(values []Value) (numValues int, err error) {
	if c.columnBuffer == nil {
		c.columnBuffer = c.newColumnBuffer()
	}
	return c.columnBuffer.WriteValues(values)
}

func (c *writerColumn) writeBloomFilter(w io.Writer) error {
	e := thrift.NewEncoder(c.header.protocol.NewWriter(w))
	h := bloomFilterHeader(c.columnFilter)
	h.NumBytes = int32(len(c.filter))
	if err := e.Encode(&h); err != nil {
		return err
	}
	_, err := w.Write(c.filter)
	return err
}

func (c *writerColumn) writeDataPage(page Page) (int64, error) {
	numValues := page.NumValues()
	if numValues == 0 {
		return 0, nil
	}

	buf := c.buffers
	buf.reset()

	if c.maxRepetitionLevel > 0 {
		buf.encodeRepetitionLevels(page, c.maxRepetitionLevel)
	}
	if c.maxDefinitionLevel > 0 {
		buf.encodeDefinitionLevels(page, c.maxDefinitionLevel)
	}

	if err := buf.encode(page, c.encoding); err != nil {
		return 0, fmt.Errorf("encoding parquet data page: %w", err)
	}
	if c.dataPageType == format.DataPage {
		buf.prependLevelsToDataPageV1(c.maxDefinitionLevel, c.maxDefinitionLevel)
	}

	uncompressedPageSize := buf.size()
	if c.isCompressed {
		if err := buf.compress(c.compression); err != nil {
			return 0, fmt.Errorf("compressing parquet data page: %w", err)
		}
	}

	if page.Dictionary() == nil && len(c.filter) > 0 {
		// When the writer knows the number of values in advance (e.g. when
		// writing a full row group), the filter encoding is set and the page
		// can be directly applied to the filter, which minimizes memory usage
		// since there is no need to buffer the values in order to determine
		// the size of the filter.
		if err := c.writePageToFilter(page); err != nil {
			return 0, err
		}
	}

	statistics := format.Statistics{}
	if c.writePageStats {
		statistics = c.makePageStatistics(page)
	}

	pageHeader := &format.PageHeader{
		Type:                 c.dataPageType,
		UncompressedPageSize: int32(uncompressedPageSize),
		CompressedPageSize:   int32(buf.size()),
		CRC:                  int32(buf.crc32()),
	}

	numRows := page.NumRows()
	numNulls := page.NumNulls()
	switch c.dataPageType {
	case format.DataPage:
		pageHeader.DataPageHeader = &format.DataPageHeader{
			NumValues:               int32(numValues),
			Encoding:                c.encoding.Encoding(),
			DefinitionLevelEncoding: format.RLE,
			RepetitionLevelEncoding: format.RLE,
			Statistics:              statistics,
		}
	case format.DataPageV2:
		pageHeader.DataPageHeaderV2 = &format.DataPageHeaderV2{
			NumValues:                  int32(numValues),
			NumNulls:                   int32(numNulls),
			NumRows:                    int32(numRows),
			Encoding:                   c.encoding.Encoding(),
			DefinitionLevelsByteLength: int32(len(buf.definitions)),
			RepetitionLevelsByteLength: int32(len(buf.repetitions)),
			IsCompressed:               &c.isCompressed,
			Statistics:                 statistics,
		}
	}

	buf.header.Reset()
	if err := c.header.encoder.Encode(pageHeader); err != nil {
		return 0, err
	}

	size := int64(buf.header.Len()) +
		int64(len(buf.repetitions)) +
		int64(len(buf.definitions)) +
		int64(len(buf.page))

	err := c.writePageTo(size, func(output io.Writer) (written int64, err error) {
		for _, data := range [...][]byte{
			buf.header.Bytes(),
			buf.repetitions,
			buf.definitions,
			buf.page,
		} {
			wn, err := output.Write(data)
			written += int64(wn)
			if err != nil {
				return written, err
			}
		}
		return written, nil
	})
	if err != nil {
		return 0, err
	}

	c.recordPageStats(int32(buf.header.Len()), pageHeader, page)
	return numValues, nil
}

func (c *writerColumn) writeDictionaryPage(output io.Writer, dict Dictionary) (err error) {
	buf := c.buffers
	buf.reset()

	if err := buf.encode(dict.Page(), &Plain); err != nil {
		return fmt.Errorf("writing parquet dictionary page: %w", err)
	}

	uncompressedPageSize := buf.size()
	if isCompressed(c.compression) {
		if err := buf.compress(c.compression); err != nil {
			return fmt.Errorf("copmressing parquet dictionary page: %w", err)
		}
	}

	pageHeader := &format.PageHeader{
		Type:                 format.DictionaryPage,
		UncompressedPageSize: int32(uncompressedPageSize),
		CompressedPageSize:   int32(buf.size()),
		CRC:                  int32(buf.crc32()),
		DictionaryPageHeader: &format.DictionaryPageHeader{
			NumValues: int32(dict.Len()),
			Encoding:  format.Plain,
			IsSorted:  false,
		},
	}

	header := &c.buffers.header
	header.Reset()
	if err := c.header.encoder.Encode(pageHeader); err != nil {
		return err
	}
	if _, err := output.Write(header.Bytes()); err != nil {
		return err
	}
	if _, err := output.Write(buf.page); err != nil {
		return err
	}
	c.recordPageStats(int32(header.Len()), pageHeader, nil)
	return nil
}

func (w *writerColumn) writePageToFilter(page Page) (err error) {
	pageType := page.Type()
	pageData := page.Data()
	w.filter, err = pageType.Encode(w.filter, pageData, w.columnFilter.Encoding())
	return err
}

func (c *writerColumn) writePageTo(size int64, writeTo func(io.Writer) (int64, error)) error {
	buffer := c.pool.GetBuffer()
	defer func() {
		if buffer != nil {
			c.pool.PutBuffer(buffer)
		}
	}()
	written, err := writeTo(buffer)
	if err != nil {
		return err
	}
	if written != size {
		return fmt.Errorf("writing parquet column page expected %dB but got %dB: %w", size, written, io.ErrShortWrite)
	}
	offset, err := buffer.Seek(0, io.SeekStart)
	if err != nil {
		return err
	}
	if offset != 0 {
		return fmt.Errorf("resetting parquet page buffer to the start expected offset zero but got %d", offset)
	}
	c.pages, buffer = append(c.pages, buffer), nil
	return nil
}

func (c *writerColumn) makePageStatistics(page Page) format.Statistics {
	numNulls := page.NumNulls()
	minValue, maxValue, _ := page.Bounds()
	minValueBytes := minValue.Bytes()
	maxValueBytes := maxValue.Bytes()
	return format.Statistics{
		Min:       minValueBytes, // deprecated
		Max:       maxValueBytes, // deprecated
		NullCount: numNulls,
		MinValue:  minValueBytes,
		MaxValue:  maxValueBytes,
	}
}

func (c *writerColumn) recordPageStats(headerSize int32, header *format.PageHeader, page Page) {
	uncompressedSize := headerSize + header.UncompressedPageSize
	compressedSize := headerSize + header.CompressedPageSize

	if page != nil {
		numNulls := page.NumNulls()
		numValues := page.NumValues()
		minValue, maxValue, _ := page.Bounds()
		c.columnIndex.IndexPage(numValues, numNulls, minValue, maxValue)
		c.columnChunk.MetaData.NumValues += numValues

		c.offsetIndex.PageLocations = append(c.offsetIndex.PageLocations, format.PageLocation{
			Offset:             c.columnChunk.MetaData.TotalCompressedSize,
			CompressedPageSize: compressedSize,
			FirstRowIndex:      c.numRows,
		})

		c.numRows += page.NumRows()
	}

	pageType := header.Type
	encoding := format.Encoding(-1)
	switch pageType {
	case format.DataPageV2:
		encoding = header.DataPageHeaderV2.Encoding
	case format.DataPage:
		encoding = header.DataPageHeader.Encoding
	case format.DictionaryPage:
		encoding = header.DictionaryPageHeader.Encoding
	}

	c.columnChunk.MetaData.TotalUncompressedSize += int64(uncompressedSize)
	c.columnChunk.MetaData.TotalCompressedSize += int64(compressedSize)
	c.columnChunk.MetaData.EncodingStats = addPageEncodingStats(c.columnChunk.MetaData.EncodingStats, format.PageEncodingStats{
		PageType: pageType,
		Encoding: encoding,
		Count:    1,
	})
}

func addEncoding(encodings []format.Encoding, add format.Encoding) []format.Encoding {
	for _, enc := range encodings {
		if enc == add {
			return encodings
		}
	}
	return append(encodings, add)
}

func addPageEncodingStats(stats []format.PageEncodingStats, pages ...format.PageEncodingStats) []format.PageEncodingStats {
addPages:
	for _, add := range pages {
		for i, st := range stats {
			if st.PageType == add.PageType && st.Encoding == add.Encoding {
				stats[i].Count += add.Count
				continue addPages
			}
		}
		stats = append(stats, add)
	}
	return stats
}

func sortPageEncodings(encodings []format.Encoding) {
	sort.Slice(encodings, func(i, j int) bool {
		return encodings[i] < encodings[j]
	})
}

func sortPageEncodingStats(stats []format.PageEncodingStats) {
	sort.Slice(stats, func(i, j int) bool {
		s1 := &stats[i]
		s2 := &stats[j]
		if s1.PageType != s2.PageType {
			return s1.PageType < s2.PageType
		}
		return s1.Encoding < s2.Encoding
	})
}

var (
	_ RowWriterWithSchema = (*Writer)(nil)
	_ RowReaderFrom       = (*Writer)(nil)
	_ RowGroupWriter      = (*Writer)(nil)

	_ RowWriter   = (*writer)(nil)
	_ ValueWriter = (*writer)(nil)

	_ ValueWriter = (*writerColumn)(nil)
)
