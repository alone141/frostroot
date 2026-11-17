// Package paxstrip copies a gzip-compressed tar archive block by block and
// leaves out one kind of pax record: the POSIX ACLs, keyed
// SCHILY.xattr.system.posix_acl_*, which the unpacking of WSL 1 refuses.
//
// It is not a tar writer. Every 512-byte block passes through as it was
// read, so every header, every other record and every byte of content stays
// what mmdebstrap or mkosi wrote. The one exception is a pax extended header
// that holds such a record: its data is written again with the other
// records in their order, and its size and checksum fields are recomputed;
// a pax header that held nothing else is left out whole. Nothing is
// unpacked, and nothing touches a file system: Copy reads a reader and
// writes a writer.
//
// The archive is a file someone hands over, so it is read as hostile: every
// header's checksum is verified, a pax header is held in memory only up to
// MaxPAXHeaderSize, and anything malformed or cut short is refused with an
// error that wraps ErrNotTarball.
package paxstrip

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// DroppedKeyPrefix is the prefix of the pax record keys Copy leaves out:
// the access and default ACLs that systemd-tmpfiles sets on
// /var/log/journal, and that WSL 1 refuses to register a tarball with.
const DroppedKeyPrefix = "SCHILY.xattr.system.posix_acl_"

// MaxPAXHeaderSize bounds the data of one pax extended header, which Copy
// holds in memory to rewrite it. mmdebstrap's are a few hundred bytes.
const MaxPAXHeaderSize = 1 << 20

// ErrNotTarball is wrapped by every error that says the input is not a
// gzip-compressed tar archive Copy can read: not gzip, a header that fails
// its checksum, a malformed pax record, an archive cut short.
var ErrNotTarball = errors.New("not a gzip-compressed tar archive")

// blockSize is the unit of a tar archive: every header is one block, and
// every entry's data is padded to a whole number of them.
const blockSize = 512

// Where the fields Copy reads lie in a header block (POSIX ustar, which GNU
// tar's format shares up to the magic).
const (
	sizeFieldStart     = 124
	sizeFieldEnd       = 136
	checksumFieldStart = 148
	checksumFieldEnd   = 156
	typeflagOffset     = 156
	// gnuSparseExtendedOffset is the old GNU format's flag that sparse map
	// blocks follow a sparse file's header; gnuSparseBlockExtendedOffset is
	// the same flag in each of those blocks.
	gnuSparseExtendedOffset      = 482
	gnuSparseBlockExtendedOffset = 504
)

// Type flags Copy treats apart from the rest.
const (
	typePAXExtended = 'x'
	typePAXGlobal   = 'g'
	typeGNULongName = 'L'
	typeGNULongLink = 'K'
	typeGNUSparse   = 'S'
)

// headerOnlyTypes are the entries that carry no data: hard and symbolic
// links, devices, directories and FIFOs. Readers disagree about whether a
// size in such a header means data follows, so a nonzero one is refused
// rather than read one way here and another way by Windows.
const headerOnlyTypes = "123456"

// copyBufferSize is how much of the decompressed archive is read ahead.
const copyBufferSize = 64 << 10

// Copy reads the gzip-compressed tar archive src and writes it to dst,
// gzip-compressed, without the pax records whose keys start with
// DroppedKeyPrefix. It returns how many records it dropped. Decompressed,
// the output of an archive without such a record is the input, byte for
// byte. An error that wraps ErrNotTarball means src is not an archive Copy
// can read; any other error is a failure to read src or to write dst.
func Copy(dst io.Writer, src io.Reader) (dropped int, err error) {
	source := &sourceReader{reader: src}
	decompressed, err := gzip.NewReader(source)
	if err != nil {
		if source.err != nil {
			return 0, fmt.Errorf("reading the tarball: %w", source.err)
		}
		return 0, fmt.Errorf("%w: %w", ErrNotTarball, err)
	}
	// The copy lives only until Windows has unpacked it, so speed matters
	// more than its size.
	compressed, err := gzip.NewWriterLevel(dst, gzip.BestSpeed)
	if err != nil { // unreachable with a valid level, but never ignore an error
		return 0, err
	}
	walk := archiveWalk{source: source, input: bufio.NewReaderSize(decompressed, copyBufferSize), output: taggingWriter{writer: compressed}, pendingSize: -1}
	if err := walk.run(); err != nil {
		return walk.dropped, err
	}
	if err := compressed.Close(); err != nil {
		return walk.dropped, fmt.Errorf("writing the copy: %w", err)
	}
	return walk.dropped, nil
}

// sourceReader remembers the first error the compressed input itself
// returned, so that a failing disk is not reported as a malformed archive.
type sourceReader struct {
	reader io.Reader
	err    error
}

func (s *sourceReader) Read(buffer []byte) (int, error) {
	count, err := s.reader.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) && s.err == nil {
		s.err = err
	}
	return count, err
}

// archiveWalk is one pass over the decompressed archive.
type archiveWalk struct {
	source  *sourceReader
	input   *bufio.Reader
	output  io.Writer
	dropped int
	// offset is where the next block starts in the decompressed archive,
	// for messages.
	offset int64
	// pendingSize is the size a pax extended header's "size" record gives
	// the next entry, or -1 for none.
	pendingSize int64
}

func (w *archiveWalk) run() error {
	header := make([]byte, blockSize)
	for {
		headerOffset := w.offset
		if err := w.read(header, "a header"); err != nil {
			if errors.Is(err, errEndOfInput) {
				return fmt.Errorf("%w: it ends at byte %d without the end-of-archive marker", ErrNotTarball, headerOffset)
			}
			return err
		}
		if isZeroBlock(header) {
			// The end-of-archive marker. What follows it, the second zero
			// block and the padding to the record size, passes through as
			// it is.
			if err := w.write(header); err != nil {
				return err
			}
			if _, err := io.Copy(w.output, w.input); err != nil {
				return w.copyError(err, "the end of the archive")
			}
			return nil
		}
		if !checksumMatches(header) {
			return fmt.Errorf("%w: the header at byte %d fails its checksum", ErrNotTarball, headerOffset)
		}
		size, err := parseNumeric(header[sizeFieldStart:sizeFieldEnd])
		if err != nil {
			return fmt.Errorf("%w: the header at byte %d: size: %w", ErrNotTarball, headerOffset, err)
		}
		typeflag := header[typeflagOffset]
		if typeflag == typePAXExtended || typeflag == typePAXGlobal {
			if err := w.copyPAXHeader(header, size, typeflag == typePAXGlobal, headerOffset); err != nil {
				return err
			}
			continue
		}
		if err := w.copyEntry(header, size, typeflag, headerOffset); err != nil {
			return err
		}
	}
}

// copyEntry copies one entry that is not a pax header: its header, the
// sparse map blocks an old GNU sparse file has, and its data.
func (w *archiveWalk) copyEntry(header []byte, size int64, typeflag byte, headerOffset int64) error {
	isMeta := typeflag == typeGNULongName || typeflag == typeGNULongLink
	if !isMeta && w.pendingSize >= 0 {
		size = w.pendingSize
	}
	if !isMeta {
		w.pendingSize = -1
	}
	if size != 0 && strings.IndexByte(headerOnlyTypes, typeflag) >= 0 {
		return fmt.Errorf("%w: the header at byte %d is of type %q, which has no data, and gives a size of %d", ErrNotTarball, headerOffset, typeflag, size)
	}
	if err := w.write(header); err != nil {
		return err
	}
	if typeflag == typeGNUSparse && header[gnuSparseExtendedOffset] != 0 {
		if err := w.copySparseMap(); err != nil {
			return err
		}
	}
	if size > math.MaxInt64-(blockSize-1) {
		return fmt.Errorf("%w: the header at byte %d gives a size of %d, too large to be read", ErrNotTarball, headerOffset, size)
	}
	padded := paddedSize(size)
	copied, err := io.CopyN(w.output, w.input, padded)
	w.offset += copied
	if err != nil {
		return w.copyError(err, "an entry's data")
	}
	return nil
}

// copySparseMap copies the blocks of an old GNU sparse file's map, which
// follow its header until one says no more follow.
func (w *archiveWalk) copySparseMap() error {
	block := make([]byte, blockSize)
	for {
		if err := w.read(block, "a sparse map"); err != nil {
			return w.truncated(err, "a sparse map")
		}
		if err := w.write(block); err != nil {
			return err
		}
		if block[gnuSparseBlockExtendedOffset] == 0 {
			return nil
		}
	}
}

// copyPAXHeader copies a pax extended or global header. One without a
// record to drop passes through as it was read. One with such a record is
// written again without it, and left out whole if nothing else remains.
func (w *archiveWalk) copyPAXHeader(header []byte, size int64, global bool, headerOffset int64) error {
	if size > MaxPAXHeaderSize {
		return fmt.Errorf("%w: the pax header at byte %d holds %d bytes, more than the %d frostroot reads", ErrNotTarball, headerOffset, size, MaxPAXHeaderSize)
	}
	data := make([]byte, paddedSize(size))
	if err := w.read(data, "a pax header"); err != nil {
		return w.truncated(err, "a pax header")
	}
	records, err := parsePAXRecords(data[:size])
	if err != nil {
		return fmt.Errorf("%w: the pax header at byte %d: %w", ErrNotTarball, headerOffset, err)
	}
	var kept []byte
	droppedHere := 0
	for _, record := range records {
		if strings.HasPrefix(record.key, DroppedKeyPrefix) {
			droppedHere++
			continue
		}
		if record.key == "size" {
			if global {
				return fmt.Errorf("%w: the global pax header at byte %d gives every entry one size", ErrNotTarball, headerOffset)
			}
			recordSize, err := parseDecimal(record.value)
			if err != nil {
				return fmt.Errorf("%w: the pax header at byte %d: size record: %w", ErrNotTarball, headerOffset, err)
			}
			w.pendingSize = recordSize
		}
		kept = append(kept, record.raw...)
	}
	if droppedHere == 0 {
		if err := w.write(header); err != nil {
			return err
		}
		return w.write(data)
	}
	w.dropped += droppedHere
	if len(kept) == 0 {
		return nil
	}
	rewritten := bytes.Clone(header)
	if err := setSize(rewritten[sizeFieldStart:sizeFieldEnd], int64(len(kept))); err != nil {
		return fmt.Errorf("%w: the pax header at byte %d: %w", ErrNotTarball, headerOffset, err)
	}
	setChecksum(rewritten)
	if err := w.write(rewritten); err != nil {
		return err
	}
	padding := make([]byte, paddedSize(int64(len(kept)))-int64(len(kept)))
	if err := w.write(kept); err != nil {
		return err
	}
	return w.write(padding)
}

// errEndOfInput is read's answer when the archive ended exactly where a
// block was to start.
var errEndOfInput = errors.New("end of input")

// read fills buffer from the archive, which is a whole number of blocks.
func (w *archiveWalk) read(buffer []byte, what string) error {
	count, err := io.ReadFull(w.input, buffer)
	w.offset += int64(count)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, io.EOF):
		return errEndOfInput
	default:
		return w.copyError(err, what)
	}
}

// truncated turns read's errors into the one for an archive that ends
// inside what.
func (w *archiveWalk) truncated(err error, what string) error {
	if errors.Is(err, errEndOfInput) {
		return fmt.Errorf("%w: it ends in the middle of %s, at byte %d", ErrNotTarball, what, w.offset)
	}
	return err
}

// copyError says why reading or copying stopped: the compressed input
// failing, the output failing, an archive cut short, or gzip finding the
// stream corrupt.
func (w *archiveWalk) copyError(err error, what string) error {
	var failedWrite *writeError
	switch {
	case errors.As(err, &failedWrite):
		return err
	case w.source.err != nil:
		return fmt.Errorf("reading the tarball: %w", w.source.err)
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return fmt.Errorf("%w: it ends in the middle of %s, at byte %d", ErrNotTarball, what, w.offset)
	}
	return fmt.Errorf("%w: %w", ErrNotTarball, err)
}

// writeError is a failure to write the copy, kept apart from the input's
// failures as it travels through io.Copy.
type writeError struct{ err error }

func (e *writeError) Error() string { return "writing the copy: " + e.err.Error() }
func (e *writeError) Unwrap() error { return e.err }

// taggingWriter turns every error of the writer it holds into a writeError.
type taggingWriter struct{ writer io.Writer }

func (t taggingWriter) Write(data []byte) (int, error) {
	count, err := t.writer.Write(data)
	if err != nil {
		return count, &writeError{err: err}
	}
	return count, nil
}

func (w *archiveWalk) write(data []byte) error {
	_, err := w.output.Write(data)
	return err
}

// paddedSize rounds size up to a whole number of blocks. The caller has
// made sure that cannot overflow.
func paddedSize(size int64) int64 {
	return (size + blockSize - 1) / blockSize * blockSize
}

func isZeroBlock(block []byte) bool {
	for _, value := range block {
		if value != 0 {
			return false
		}
	}
	return true
}

// checksumMatches reports whether a header's checksum field holds the sum
// of its bytes, counting the field itself as spaces. Old tar programs
// summed signed bytes, so either sum is accepted, as GNU tar and Go's
// archive/tar accept it.
func checksumMatches(header []byte) bool {
	stored, err := parseOctal(header[checksumFieldStart:checksumFieldEnd])
	if err != nil {
		return false
	}
	unsigned, signed := headerSums(header)
	return stored == unsigned || stored == signed
}

func headerSums(header []byte) (unsigned, signed int64) {
	for index, value := range header {
		if index >= checksumFieldStart && index < checksumFieldEnd {
			value = ' '
		}
		unsigned += int64(value)
		signed += int64(int8(value))
	}
	return unsigned, signed
}

// setChecksum writes a header's unsigned checksum into its field, in the
// layout the field already had: the same number of digits and the same
// terminator. A field too narrow for the sum gets the layout POSIX gives,
// six digits, a NUL and a space.
func setChecksum(header []byte) {
	unsigned, _ := headerSums(header)
	field := header[checksumFieldStart:checksumFieldEnd]
	if !setOctalDigits(field, unsigned) {
		copy(field, fmt.Sprintf("%06o\x00 ", unsigned))
	}
}

// setSize writes size into a size field in the form it already had: in
// base 256 if it was, otherwise as octal digits in the same places.
func setSize(field []byte, size int64) error {
	if field[0]&0x80 != 0 {
		for index := len(field) - 1; index > 0; index-- {
			field[index] = byte(size)
			size >>= 8
		}
		field[0] = 0x80
		return nil
	}
	if !setOctalDigits(field, size) {
		return fmt.Errorf("size %d does not fit the header's size field", size)
	}
	return nil
}

// setOctalDigits writes value over the run of octal digits field holds,
// zero-padded to the run's width, and reports whether it fit.
func setOctalDigits(field []byte, value int64) bool {
	start := 0
	for start < len(field) && field[start] == ' ' {
		start++
	}
	end := start
	for end < len(field) && field[end] >= '0' && field[end] <= '7' {
		end++
	}
	digits := strconv.FormatInt(value, 8)
	if len(digits) > end-start {
		return false
	}
	copy(field[start:end], strings.Repeat("0", end-start-len(digits))+digits)
	return true
}

// parseNumeric reads a numeric header field: octal digits, or, as GNU tar
// writes a size too large for them, a positive number in base 256.
func parseNumeric(field []byte) (int64, error) {
	if len(field) == 0 || field[0]&0x80 == 0 {
		return parseOctal(field)
	}
	if field[0]&0x40 != 0 {
		return 0, errors.New("a negative number")
	}
	var value uint64
	for index, digit := range field {
		if index == 0 {
			digit &= 0x7f
		}
		if value>>56 != 0 {
			return 0, errors.New("a number too large")
		}
		value = value<<8 | uint64(digit)
	}
	if value > math.MaxInt64 {
		return 0, errors.New("a number too large")
	}
	return int64(value), nil
}

// parseOctal reads octal digits padded with spaces or NULs on either side,
// as tar programs pad them. An empty field is zero.
func parseOctal(field []byte) (int64, error) {
	digits := strings.Trim(string(field), " \x00")
	if digits == "" {
		return 0, nil
	}
	for _, digit := range digits {
		if digit < '0' || digit > '7' {
			return 0, fmt.Errorf("%q is not an octal number", digits)
		}
	}
	value, err := strconv.ParseInt(digits, 8, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is too large", digits)
	}
	return value, nil
}

// parseDecimal reads a non-negative decimal number, digits only.
func parseDecimal(text []byte) (int64, error) {
	if len(text) == 0 {
		return 0, errors.New("no number")
	}
	for _, digit := range text {
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("%q is not a decimal number", text)
		}
	}
	value, err := strconv.ParseInt(string(text), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is too large", text)
	}
	return value, nil
}

// paxRecord is one "LENGTH KEY=VALUE\n" record of a pax header, with the
// bytes it was read from.
type paxRecord struct {
	raw   []byte
	key   string
	value []byte
}

// maxPAXLengthDigits bounds the length prefix of a record: a record cannot
// be longer than MaxPAXHeaderSize.
var maxPAXLengthDigits = len(strconv.Itoa(MaxPAXHeaderSize))

// parsePAXRecords splits a pax header's data into its records. The data
// must be records and nothing else: each starts with its own length in
// decimal, which counts the whole record, then a space, a key that is not
// empty, "=", the value and a newline.
func parsePAXRecords(data []byte) ([]paxRecord, error) {
	var records []paxRecord
	for offset := 0; offset < len(data); {
		rest := data[offset:]
		space := bytes.IndexByte(rest, ' ')
		if space <= 0 || space > maxPAXLengthDigits {
			return nil, fmt.Errorf("the record at byte %d does not start with its length", offset)
		}
		length, err := parseDecimal(rest[:space])
		if err != nil {
			return nil, fmt.Errorf("the record at byte %d: length: %w", offset, err)
		}
		if length <= int64(space)+1 || length > int64(len(rest)) {
			return nil, fmt.Errorf("the record at byte %d gives a length of %d, which does not fit", offset, length)
		}
		raw := rest[:length]
		if raw[length-1] != '\n' {
			return nil, fmt.Errorf("the record at byte %d does not end in a newline", offset)
		}
		body := raw[space+1 : length-1]
		equals := bytes.IndexByte(body, '=')
		if equals <= 0 {
			return nil, fmt.Errorf("the record at byte %d has no key", offset)
		}
		records = append(records, paxRecord{raw: raw, key: string(body[:equals]), value: body[equals+1:]})
		offset += int(length)
	}
	return records, nil
}
