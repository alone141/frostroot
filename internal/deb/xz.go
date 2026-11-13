package deb

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"io"
	"slices"

	"github.com/ulikunitz/xz/lzma"
)

// maxXZDictionaryBytes is the largest LZMA2 dictionary a block of an xz file
// may declare: 64 MiB, what xz -9 uses, the largest of any preset.
const maxXZDictionaryBytes = 64 << 20

// ErrXZDictionaryTooLarge means a block of an xz file declares an LZMA2
// dictionary larger than any xz preset uses.
var ErrXZDictionaryTooLarge = fmt.Errorf("xz: a block declares a dictionary larger than %d MiB", maxXZDictionaryBytes>>20)

// Layout of the xz container, from its file format specification.
const (
	xzStreamHeaderBytes    = 12
	xzStreamFooterBytes    = 12
	xzMaxBlockHeaderBytes  = 1024
	xzLZMA2FilterID        = 0x21
	xzReservedBlockFlags   = 0x3c
	xzFilterCountMask      = 0x03
	xzCompressedSizeFlag   = 0x40
	xzUncompressedSizeFlag = 0x80
)

var (
	xzStreamHeaderMagic = []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}
	xzStreamFooterMagic = []byte{'Y', 'Z'}
	xzCRC64Table        = crc64.MakeTable(crc64.ECMA)
)

// NewXZReader returns a reader of the data an xz file holds, all of its
// streams. It refuses with ErrXZDictionaryTooLarge a block that declares a
// dictionary larger than any xz preset uses, and gives what
// github.com/ulikunitz/xz's NewReader gives for every file it accepts. That
// reader allocates whatever dictionary a block declares, up to 4 GiB, before
// it decodes a byte of it, so a few bytes of a hostile file could ask for
// gigabytes.
//
// The container is read here, and only the LZMA2 data of each block is left
// to the library's decoder. A filter in front of the library's reader could
// not find every block header: the decoder may stop short of the length a
// chunk declares and read the next header from inside that chunk, so only
// the decoder knows where a block ends.
func NewXZReader(compressed io.Reader) (io.Reader, error) {
	reader := &xzReader{source: &xzSource{reader: bufio.NewReader(compressed)}, records: sha256.New()}
	if err := reader.startStream(true); err != nil {
		return nil, err
	}
	return reader, nil
}

// xzReader decodes an xz file. Its own memory is constant: blocks are
// checked against the index by a digest of their sizes, not a list.
type xzReader struct {
	source       *xzSource
	headerBuffer [xzMaxBlockHeaderBytes]byte
	checkFlags   byte   // the current stream's check method
	blockCount   uint64 // blocks read in the current stream
	records      hash.Hash
	block        *xzBlock // nil between blocks
	err          error
}

// xzBlock is the block being decoded.
type xzBlock struct {
	decoder              io.Reader
	headerBytes          int64
	compressedStart      int64 // the source's offset where the LZMA2 data starts
	declaredCompressed   int64 // -1 when the header declares none
	declaredUncompressed int64 // -1 when the header declares none
	uncompressed         int64
	check                hash.Hash // nil when the stream has no check
}

// xzSource counts the bytes read from the file, which a block's sizes and
// the index are checked against.
type xzSource struct {
	reader *bufio.Reader
	offset int64
}

func (s *xzSource) Read(p []byte) (int, error) {
	n, err := s.reader.Read(p)
	s.offset += int64(n)
	return n, err
}

func (s *xzSource) ReadByte() (byte, error) {
	b, err := s.reader.ReadByte()
	if err == nil {
		s.offset++
	}
	return b, err
}

func (x *xzReader) Read(p []byte) (int, error) {
	for x.err == nil && len(p) > 0 {
		if x.block == nil {
			x.err = x.startBlock()
			continue
		}
		n, err := x.readBlock(p)
		if err != nil {
			x.err = err
		}
		if n > 0 || err != nil {
			return n, err
		}
	}
	return 0, x.err
}

// startStream reads a stream header, after any stream padding when it is
// not the first stream. It returns io.EOF at the end of the file.
func (x *xzReader) startStream(first bool) error {
	header := x.headerBuffer[:xzStreamHeaderBytes]
	for {
		if _, err := io.ReadFull(x.source, header[:4]); err != nil {
			if errors.Is(err, io.EOF) && !first {
				return io.EOF
			}
			return unexpectedEOF(err)
		}
		if !allZero(header[:4]) {
			break
		}
		if first {
			return errors.New("xz: stream padding before the first stream")
		}
	}
	if _, err := io.ReadFull(x.source, header[4:]); err != nil {
		return unexpectedEOF(err)
	}
	if !bytes.Equal(header[:len(xzStreamHeaderMagic)], xzStreamHeaderMagic) {
		return errors.New("xz: not an xz stream")
	}
	if crc32.ChecksumIEEE(header[6:8]) != binary.LittleEndian.Uint32(header[8:]) {
		return errors.New("xz: stream header checksum mismatch")
	}
	if header[6] != 0 || xzCheckBytes(header[7]) < 0 {
		return errors.New("xz: unsupported stream flags")
	}
	x.checkFlags = header[7]
	x.blockCount = 0
	x.records.Reset()
	return nil
}

// startBlock reads the next block header, or the index and footer that end
// the stream and then the start of the next stream.
func (x *xzReader) startBlock() error {
	headerSize, err := x.source.ReadByte()
	if err != nil {
		return unexpectedEOF(err)
	}
	if headerSize == 0 {
		if err := x.readIndexAndFooter(); err != nil {
			return err
		}
		return x.startStream(false)
	}
	header := x.headerBuffer[:(int(headerSize)+1)*4]
	header[0] = headerSize
	if _, err := io.ReadFull(x.source, header[1:]); err != nil {
		return unexpectedEOF(err)
	}
	fieldsEnd := len(header) - 4
	if crc32.ChecksumIEEE(header[:fieldsEnd]) != binary.LittleEndian.Uint32(header[fieldsEnd:]) {
		return errors.New("xz: block header checksum mismatch")
	}
	flags := header[1]
	if flags&xzReservedBlockFlags != 0 {
		return errors.New("xz: reserved block header flags set")
	}
	if flags&xzFilterCountMask != 0 {
		return errors.New("xz: a block uses a filter other than LZMA2 alone")
	}
	fields := bytes.NewReader(header[2:fieldsEnd])
	declaredCompressed, err := readXZBlockSize(fields, flags&xzCompressedSizeFlag != 0)
	if err != nil {
		return err
	}
	declaredUncompressed, err := readXZBlockSize(fields, flags&xzUncompressedSizeFlag != 0)
	if err != nil {
		return err
	}
	filterID, err := binary.ReadUvarint(fields)
	if err != nil {
		return fmt.Errorf("xz: block header: %w", unexpectedEOF(err))
	}
	if filterID != xzLZMA2FilterID {
		return errors.New("xz: a block uses a filter other than LZMA2")
	}
	propertiesSize, err := fields.ReadByte()
	if err != nil || propertiesSize != 1 {
		return errors.New("xz: wrong LZMA2 filter properties")
	}
	dictionaryCode, err := fields.ReadByte()
	if err != nil {
		return errors.New("xz: wrong LZMA2 filter properties")
	}
	dictionaryBytes, err := lzma.DecodeDictCap(dictionaryCode)
	if err != nil {
		return fmt.Errorf("xz: %w", err)
	}
	if dictionaryBytes > maxXZDictionaryBytes {
		return ErrXZDictionaryTooLarge
	}
	if !allZero(header[fieldsEnd-fields.Len() : fieldsEnd]) {
		return errors.New("xz: non-zero block header padding")
	}
	// The library's own reader gives the decoder this dictionary, whatever
	// its configuration says. The decoder reads its first chunk header at
	// once, so the offset is taken before.
	compressedStart := x.source.offset
	decoder, err := lzma.Reader2Config{DictCap: int(dictionaryBytes)}.NewReader2(x.source)
	if err != nil {
		return fmt.Errorf("xz: %w", err)
	}
	x.block = &xzBlock{
		decoder:              decoder,
		headerBytes:          int64(len(header)),
		compressedStart:      compressedStart,
		declaredCompressed:   declaredCompressed,
		declaredUncompressed: declaredUncompressed,
		check:                newXZCheck(x.checkFlags),
	}
	return nil
}

// readBlock reads decoded data from the current block and, at its end,
// checks its padding, its check and the sizes its header declares.
func (x *xzReader) readBlock(p []byte) (int, error) {
	block := x.block
	n, err := block.decoder.Read(p)
	block.uncompressed += int64(n)
	if block.check != nil {
		block.check.Write(p[:n])
	}
	compressed := x.source.offset - block.compressedStart
	if block.declaredUncompressed >= 0 && block.uncompressed > block.declaredUncompressed {
		return n, errors.New("xz: block is larger than its header says")
	}
	if block.declaredCompressed >= 0 && compressed > block.declaredCompressed {
		return n, errors.New("xz: block is larger than its header says")
	}
	if !errors.Is(err, io.EOF) {
		return n, err
	}
	if block.uncompressed < block.declaredUncompressed || compressed < block.declaredCompressed {
		return n, io.ErrUnexpectedEOF
	}
	checkBytes := xzCheckBytes(x.checkFlags)
	trailer := x.headerBuffer[:xzPaddingBytes(compressed)+checkBytes]
	if _, err := io.ReadFull(x.source, trailer); err != nil {
		return n, unexpectedEOF(err)
	}
	padding, storedCheck := trailer[:len(trailer)-checkBytes], trailer[len(trailer)-checkBytes:]
	if !allZero(padding) {
		return n, errors.New("xz: non-zero block padding")
	}
	if !bytes.Equal(storedCheck, xzCheckSum(x.checkFlags, block.check)) {
		return n, errors.New("xz: block check mismatch")
	}
	unpadded := block.headerBytes + compressed + int64(checkBytes)
	x.records.Write(binary.AppendUvarint(binary.AppendUvarint(nil, uint64(unpadded)), uint64(block.uncompressed)))
	x.blockCount++
	x.block = nil
	return n, nil
}

// readIndexAndFooter reads the rest of the index, whose indicator byte has
// been read, and the stream footer, and checks them against the blocks.
func (x *xzReader) readIndexAndFooter() error {
	index := &xzIndexReader{source: x.source, crc: crc32.ChecksumIEEE([]byte{0}), length: 1}
	recordCount, err := binary.ReadUvarint(index)
	if err != nil {
		return fmt.Errorf("xz: index: %w", unexpectedEOF(err))
	}
	if recordCount != x.blockCount {
		return errors.New("xz: index does not match the blocks")
	}
	records := sha256.New()
	for range recordCount {
		for range 2 { // the unpadded and the uncompressed size
			size, err := binary.ReadUvarint(index)
			if err != nil {
				return fmt.Errorf("xz: index: %w", unexpectedEOF(err))
			}
			records.Write(binary.AppendUvarint(nil, size))
		}
	}
	for range xzPaddingBytes(index.length) {
		padding, err := index.ReadByte()
		if err != nil {
			return unexpectedEOF(err)
		}
		if padding != 0 {
			return errors.New("xz: non-zero index padding")
		}
	}
	trailer := x.headerBuffer[:4+xzStreamFooterBytes]
	if _, err := io.ReadFull(x.source, trailer); err != nil {
		return unexpectedEOF(err)
	}
	indexCRC, footer := trailer[:4], trailer[4:]
	if binary.LittleEndian.Uint32(indexCRC) != index.crc {
		return errors.New("xz: index checksum mismatch")
	}
	if !bytes.Equal(records.Sum(nil), x.records.Sum(nil)) {
		return errors.New("xz: index does not match the blocks")
	}
	if !bytes.Equal(footer[10:], xzStreamFooterMagic) {
		return errors.New("xz: bad stream footer")
	}
	if crc32.ChecksumIEEE(footer[4:10]) != binary.LittleEndian.Uint32(footer[:4]) {
		return errors.New("xz: stream footer checksum mismatch")
	}
	if footer[8] != 0 || footer[9] != x.checkFlags {
		return errors.New("xz: stream footer flags do not match the header")
	}
	if (int64(binary.LittleEndian.Uint32(footer[4:8]))+1)*4 != index.length+4 {
		return errors.New("xz: stream footer gives the wrong index size")
	}
	return nil
}

// xzIndexReader reads the index byte by byte, counting its bytes and adding
// them to its CRC32.
type xzIndexReader struct {
	source *xzSource
	crc    uint32
	length int64
}

func (r *xzIndexReader) ReadByte() (byte, error) {
	b, err := r.source.ReadByte()
	if err != nil {
		return 0, err
	}
	r.length++
	r.crc = crc32.Update(r.crc, crc32.IEEETable, []byte{b})
	return b, nil
}

// readXZBlockSize reads a size from a block header, or returns -1 when the
// header leaves it out.
func readXZBlockSize(fields io.ByteReader, present bool) (int64, error) {
	if !present {
		return -1, nil
	}
	size, err := binary.ReadUvarint(fields)
	if err != nil {
		return 0, fmt.Errorf("xz: block header: %w", unexpectedEOF(err))
	}
	if size >= 1<<63 {
		return 0, errors.New("xz: size overflow in block header")
	}
	return int64(size), nil
}

// xzCheckBytes returns the size of the check a stream's flags name, or -1
// for a check the format does not define.
func xzCheckBytes(flags byte) int {
	switch flags {
	case 0x00:
		return 0
	case 0x01:
		return crc32.Size
	case 0x04:
		return crc64.Size
	case 0x0a:
		return sha256.Size
	}
	return -1
}

// newXZCheck returns the hash a stream's flags name, or nil for none.
func newXZCheck(flags byte) hash.Hash {
	switch flags {
	case 0x01:
		return crc32.NewIEEE()
	case 0x04:
		return crc64.New(xzCRC64Table)
	case 0x0a:
		return sha256.New()
	}
	return nil
}

// xzCheckSum returns a block's check as the file stores it: CRCs little
// endian, where hash.Hash gives them big endian.
func xzCheckSum(flags byte, check hash.Hash) []byte {
	if check == nil {
		return nil
	}
	sum := check.Sum(nil)
	if flags == 0x01 || flags == 0x04 {
		slices.Reverse(sum)
	}
	return sum
}

// xzPaddingBytes returns how many bytes pad length to a multiple of four.
func xzPaddingBytes(length int64) int {
	return int((4 - length%4) % 4)
}

func allZero(data []byte) bool {
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}

// unexpectedEOF reports a file that ends inside a structure.
func unexpectedEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
