package deb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math/rand/v2"
	"runtime"
	"testing"

	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"
)

// xzTestData returns size bytes that compress in parts and not in others,
// so that a stream holds both compressed and uncompressed LZMA2 chunks.
func xzTestData(size int) []byte {
	random := rand.New(rand.NewPCG(1, uint64(size)))
	data := make([]byte, 0, size)
	for len(data) < size {
		if random.IntN(2) == 0 {
			data = append(data, "Package: hello\nVersion: 2.10-3build1\n"...)
			continue
		}
		for range 64 {
			data = append(data, byte(random.Uint32()))
		}
	}
	return data[:size]
}

// compressXZ writes data as one xz stream with the library's writer.
func compressXZ(t testing.TB, data []byte, config xz.WriterConfig) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer, err := config.NewWriter(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

// decodeXZ reads everything a reader newReader makes gives for compressed.
func decodeXZ[R io.Reader](newReader func(io.Reader) (R, error), compressed []byte) ([]byte, error) {
	reader, err := newReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(reader)
}

// xzBlockHeaderOffsets returns where the block headers the library's writer
// makes start in file: a size byte, no flags, the LZMA2 filter, padding and
// a CRC32 that matches. A false match would need the CRC32 to match too.
func xzBlockHeaderOffsets(file []byte) []int {
	var offsets []int
	for offset := range file {
		headerBytes := (int(file[offset]) + 1) * 4
		if file[offset] == 0 || offset+headerBytes > len(file) {
			continue
		}
		header := file[offset : offset+headerBytes]
		if header[1] != 0 || header[2] != xzLZMA2FilterID || header[3] != 1 {
			continue
		}
		if crc32.ChecksumIEEE(header[:headerBytes-4]) == binary.LittleEndian.Uint32(header[headerBytes-4:]) {
			offsets = append(offsets, offset)
		}
	}
	return offsets
}

// withDictionaryCode returns a copy of file whose block header at offset
// declares the dictionary code, with the header's CRC32 made to match, so
// that nothing but the size of the dictionary is wrong with it.
func withDictionaryCode(t testing.TB, file []byte, offset int, code byte) []byte {
	t.Helper()
	changed := bytes.Clone(file)
	headerBytes := (int(changed[offset]) + 1) * 4
	header := changed[offset : offset+headerBytes]
	header[4] = code
	binary.LittleEndian.PutUint32(header[headerBytes-4:], crc32.ChecksumIEEE(header[:headerBytes-4]))
	return changed
}

// smallBlocks makes the library's writer start a new block every 4 KiB,
// with the smallest dictionary, so that a test allocates little.
var smallBlocks = xz.WriterConfig{DictCap: lzma.MinDictCap, BlockSize: 4 << 10}

func TestXZReaderDecodesWhatTheLibraryDecodes(t *testing.T) {
	twoStreams := append(append(compressXZ(t, xzTestData(5000), smallBlocks), make([]byte, 8)...), compressXZ(t, xzTestData(7000), xz.WriterConfig{CheckSum: xz.SHA256})...)
	testCases := []struct {
		name string
		file []byte
	}{
		{name: "empty", file: compressXZ(t, nil, xz.WriterConfig{})},
		{name: "one byte", file: compressXZ(t, []byte("x"), xz.WriterConfig{})},
		{name: "a control file", file: compressXZ(t, []byte(sampleControl), xz.WriterConfig{})},
		{name: "200 kB in one block", file: compressXZ(t, xzTestData(200_000), xz.WriterConfig{})},
		{name: "200 kB in many blocks", file: compressXZ(t, xzTestData(200_000), smallBlocks)},
		{name: "CRC32 check", file: compressXZ(t, xzTestData(10_000), xz.WriterConfig{CheckSum: xz.CRC32})},
		{name: "SHA-256 check", file: compressXZ(t, xzTestData(10_000), xz.WriterConfig{CheckSum: xz.SHA256})},
		{name: "no check", file: compressXZ(t, xzTestData(10_000), xz.WriterConfig{NoCheckSum: true})},
		{name: "two streams with padding between", file: twoStreams},
		{name: "padding after the last stream", file: append(compressXZ(t, xzTestData(100), xz.WriterConfig{}), make([]byte, 4)...)},
		{name: "a 64 MiB dictionary, the largest allowed", file: withDictionaryCode(t, compressXZ(t, xzTestData(1000), xz.WriterConfig{}), xzStreamHeaderBytes, 28)},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			want, err := decodeXZ(xz.NewReader, testCase.file)
			if err != nil {
				t.Fatalf("the library cannot read the test file: %v", err)
			}
			got, err := decodeXZ(NewXZReader, testCase.file)
			if err != nil {
				t.Fatalf("NewXZReader: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("NewXZReader gave %d bytes that differ from the library's %d", len(got), len(want))
			}
		})
	}
	if blocks := len(xzBlockHeaderOffsets(testCases[4].file)); blocks < 10 {
		t.Errorf("the many-block file has %d blocks, want it to have many", blocks)
	}
}

func TestXZReaderRefusesALargeDictionaryInEveryBlock(t *testing.T) {
	firstStream := compressXZ(t, xzTestData(20_000), smallBlocks)
	file := append(bytes.Clone(firstStream), compressXZ(t, xzTestData(20_000), smallBlocks)...)
	offsets := xzBlockHeaderOffsets(file)
	secondStreamBlock := -1
	for _, offset := range offsets {
		if offset > len(firstStream) {
			secondStreamBlock = offset
			break
		}
	}
	if len(offsets) < 4 || offsets[0] != xzStreamHeaderBytes || secondStreamBlock < 0 {
		t.Fatalf("block headers at %v, want several in each of two streams", offsets)
	}
	for _, block := range []struct {
		name   string
		offset int
	}{
		{name: "first block", offset: offsets[0]},
		{name: "second block", offset: offsets[1]},
		{name: "first block of the second stream", offset: secondStreamBlock},
	} {
		// Code 29 is 96 MiB, the first size past the limit; 40 is 4 GiB - 1,
		// the largest the format can say.
		for _, code := range []byte{29, 40} {
			t.Run(fmt.Sprintf("%s, code %d", block.name, code), func(t *testing.T) {
				hostile := withDictionaryCode(t, file, block.offset, code)
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				_, err := decodeXZ(NewXZReader, hostile)
				runtime.ReadMemStats(&after)
				if !errors.Is(err, ErrXZDictionaryTooLarge) {
					t.Errorf("dictionary code %d: err = %v, want ErrXZDictionaryTooLarge", code, err)
				}
				if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 16<<20 {
					t.Errorf("dictionary code %d: allocated %d MiB, want far less than the dictionary", code, allocated>>20)
				}
			})
		}
	}
}

func TestXZReaderRefusesACutOrExtendedFile(t *testing.T) {
	tinyBlocks := xz.WriterConfig{DictCap: lzma.MinDictCap, BlockSize: 1 << 10}
	firstStream := compressXZ(t, xzTestData(2500), tinyBlocks)
	file := append(append(bytes.Clone(firstStream), make([]byte, 4)...), compressXZ(t, xzTestData(600), tinyBlocks)...)
	if blocks := len(xzBlockHeaderOffsets(file)); blocks < 4 {
		t.Fatalf("the test file has %d blocks, want several", blocks)
	}
	// A file may end after any stream and its padding.
	whole := map[int]bool{len(firstStream): true, len(firstStream) + 4: true, len(file): true}
	for length := range len(file) + 1 {
		got, err := decodeXZ(NewXZReader, file[:length])
		if err != nil {
			continue
		}
		if !whole[length] {
			t.Errorf("NewXZReader accepted the first %d of %d bytes", length, len(file))
			continue
		}
		want, wantErr := decodeXZ(xz.NewReader, file[:length])
		if wantErr != nil || !bytes.Equal(got, want) {
			t.Errorf("first %d bytes: NewXZReader gave %d bytes, the library %d bytes and %v", length, len(got), len(want), wantErr)
		}
	}
	for _, trailer := range [][]byte{[]byte("garbage!"), {0, 0, 0}, {0, 0, 0, 0, 1}, xzStreamHeaderMagic} {
		if _, err := decodeXZ(NewXZReader, append(bytes.Clone(file), trailer...)); err == nil {
			t.Errorf("NewXZReader accepted the file followed by %q", trailer)
		}
	}
}

// FuzzXZReader: an xz file comes from the network, or from a .deb nothing
// has checked yet. Whatever NewXZReader accepts, the library's reader must
// accept too and decode to the same bytes. The library is run only on what
// NewXZReader accepts, since on anything else it may ask for gigabytes.
func FuzzXZReader(f *testing.F) {
	f.Add(compressXZ(f, nil, xz.WriterConfig{}))
	f.Add(compressXZ(f, []byte(sampleControl), xz.WriterConfig{CheckSum: xz.CRC32}))
	f.Add(compressXZ(f, xzTestData(3000), smallBlocks))
	f.Add(append(append(compressXZ(f, []byte("a"), xz.WriterConfig{NoCheckSum: true}), 0, 0, 0, 0), compressXZ(f, []byte("b"), xz.WriterConfig{CheckSum: xz.SHA256})...))
	f.Add(withDictionaryCode(f, compressXZ(f, []byte("x"), xz.WriterConfig{}), xzStreamHeaderBytes, 40))
	f.Add(xzFileWithAShortChunk(0))
	f.Fuzz(func(t *testing.T, file []byte) {
		got, err := decodeXZ(NewXZReader, file)
		if err != nil {
			return
		}
		want, err := decodeXZ(xz.NewReader, file)
		if err != nil {
			t.Fatalf("NewXZReader accepted what the library refuses: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("NewXZReader gave %q, the library %q", got, want)
		}
	})
}

// xzFileWithAShortChunk builds a stream of two blocks whose first block's
// one LZMA2 chunk declares 100 bytes of compressed data, of which the
// library's decoder reads 6 before it has the one byte the chunk holds and
// stops. It then reads the end of the block, and the second block, from
// inside those 100 bytes. The second block declares the dictionary code.
func xzFileWithAShortChunk(code byte) []byte {
	littleEndian := func(value uint32) []byte { return binary.LittleEndian.AppendUint32(nil, value) }
	withCRC32 := func(data []byte) []byte { return append(data, littleEndian(crc32.ChecksumIEEE(data))...) }
	blockHeader := func(dictionaryCode byte) []byte {
		return withCRC32([]byte{2, 0, xzLZMA2FilterID, 1, dictionaryCode, 0, 0, 0})
	}
	file := append(bytes.Clone(xzStreamHeaderMagic), withCRC32([]byte{0, 1})...) // CRC32 check
	file = append(file, blockHeader(0)...)
	// A chunk that resets everything, 1 byte decoded from 100: the range
	// coder's first byte and code are zero, so it decodes a literal zero
	// and may end there, after 6 bytes. Then the end of the chunks, and
	// padding to four.
	firstBlockData := []byte{0xe0, 0, 0, 0, 99, 0x5d, 0, 0, 0, 0, 0, 0, 0x00, 0, 0, 0}
	file = append(file, firstBlockData...)
	file = append(file, littleEndian(crc32.ChecksumIEEE([]byte{0}))...)
	file = append(file, blockHeader(code)...)
	// One uncompressed chunk holding "y", the end of the chunks, padding.
	file = append(file, 1, 0, 0, 'y', 0, 0, 0, 0)
	file = append(file, littleEndian(crc32.ChecksumIEEE([]byte("y")))...)
	// The index: two records of the unpadded and the uncompressed size, of
	// 12 + 13 + 4 and 12 + 5 + 4 bytes, then padding.
	index := withCRC32([]byte{0, 2, 29, 1, 21, 1, 0, 0})
	file = append(file, index...)
	footerFields := append(littleEndian(uint32(len(index)/4-1)), 0, 1)
	file = append(file, littleEndian(crc32.ChecksumIEEE(footerFields))...)
	return append(append(file, footerFields...), xzStreamFooterMagic...)
}

// TestXZReaderFindsABlockInsideAChunk: a filter in front of the library's
// reader that skipped each chunk by the length it declares would let the
// second block of this file through unseen.
func TestXZReaderFindsABlockInsideAChunk(t *testing.T) {
	honest := xzFileWithAShortChunk(0)
	want, err := decodeXZ(xz.NewReader, honest)
	if err != nil || string(want) != "\x00y" {
		t.Fatalf("the library reads %q, %v from the test file, want the two blocks' bytes", want, err)
	}
	got, err := decodeXZ(NewXZReader, honest)
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("NewXZReader = %q, %v; want %q", got, err, want)
	}
	if _, err := decodeXZ(NewXZReader, xzFileWithAShortChunk(40)); !errors.Is(err, ErrXZDictionaryTooLarge) {
		t.Errorf("with a 4 GiB dictionary in the second block, err = %v, want ErrXZDictionaryTooLarge", err)
	}
}
