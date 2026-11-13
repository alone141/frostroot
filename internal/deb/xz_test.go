package deb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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

// xzFileOfEmptyBlocks builds one stream, with no check, of blockCount
// blocks that each declare the dictionary code and hold no data: a block
// header, the LZMA2 end marker and padding, 16 bytes a block.
func xzFileOfEmptyBlocks(blockCount int, code byte) []byte {
	withCRC32 := func(data []byte) []byte {
		return binary.LittleEndian.AppendUint32(data, crc32.ChecksumIEEE(data))
	}
	file := append(bytes.Clone(xzStreamHeaderMagic), withCRC32([]byte{0, 0})...)
	for range blockCount {
		file = append(file, withCRC32([]byte{2, 0, xzLZMA2FilterID, 1, code, 0, 0, 0})...)
		file = append(file, 0x00, 0, 0, 0)
	}
	// Each record is the unpadded size, 12 + 1, and the uncompressed size.
	index := binary.AppendUvarint([]byte{0}, uint64(blockCount))
	for range blockCount {
		index = append(index, 13, 0)
	}
	index = withCRC32(append(index, make([]byte, xzPaddingBytes(int64(len(index))))...))
	file = append(file, index...)
	footerFields := append(binary.LittleEndian.AppendUint32(nil, uint32(len(index)/4-1)), 0, 0)
	file = binary.LittleEndian.AppendUint32(file, crc32.ChecksumIEEE(footerFields))
	return append(append(file, footerFields...), xzStreamFooterMagic...)
}

// TestXZReaderBoundsTheDictionariesOfManyBlocks: every block allocates its
// own dictionary, so a small file of empty blocks that each declare the
// largest one allowed would cost 64 MiB of allocation per 16 bytes.
func TestXZReaderBoundsTheDictionariesOfManyBlocks(t *testing.T) {
	// Code 28 is 64 MiB.
	fewBlocks := xzFileOfEmptyBlocks(2, 28)
	want, err := decodeXZ(xz.NewReader, fewBlocks)
	if err != nil || len(want) != 0 {
		t.Fatalf("the library reads %d bytes and %v from two empty blocks, want nothing and no error", len(want), err)
	}
	if got, err := decodeXZ(NewXZReader, fewBlocks); err != nil || len(got) != 0 {
		t.Errorf("two empty blocks: NewXZReader gave %d bytes and %v, want nothing and no error", len(got), err)
	}

	manyBlocks := xzFileOfEmptyBlocks(64, 28)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err = decodeXZ(NewXZReader, manyBlocks)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrXZDictionariesTooLarge) {
		t.Errorf("%d bytes of 64 empty blocks: err = %v, want ErrXZDictionariesTooLarge", len(manyBlocks), err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > xzFreeDictionaryBytes+(16<<20) {
		t.Errorf("64 empty blocks allocated %d MiB, want no more than the %d MiB allowed", allocated>>20, xzFreeDictionaryBytes>>20)
	}
}

// readXZFixture reads a file of testdata made with xz 5.2.7. The library's
// writer cannot make these: it writes no sizes in a block header.
//
//   - seq-xz9-blocks.xz: seq 1 20000 | xz -9 -T2 --block-size=8KiB, 14
//     blocks that each declare 64 MiB and their sizes.
//   - seq-xz6-blocks.xz: seq 1 20000 | xz -6 -T2 --block-size=2KiB, 54
//     blocks that each declare 8 MiB and their sizes.
//   - repeat-xz9-block.xz: 5,000 bytes of base64 of random bytes, twice,
//     through xz -9 -T2 --block-size=16KiB, one block whose second half is a
//     match 5,000 bytes back.
//   - streams4-xz9.xz, streams5-xz9.xz: for i in 1 2 3 4 [5]; do echo
//     "stream $i" | xz -9; done, streams of one block that declares 64 MiB
//     and no sizes.
func readXZFixture(t *testing.T, name string) []byte {
	t.Helper()
	file, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return file
}

// TestXZReaderDecodesSmallBlocksOfALargeDictionary: xz writes the preset's
// dictionary into every block header however small the block, so the
// blocks of xz -9 --block-size=8KiB each declared 64 MiB, and the budget
// for them refused an honest file after its fourth block. A block that
// declares its size is given no more dictionary than that.
func TestXZReaderDecodesSmallBlocksOfALargeDictionary(t *testing.T) {
	for _, name := range []string{"seq-xz9-blocks.xz", "seq-xz6-blocks.xz", "repeat-xz9-block.xz", "streams4-xz9.xz"} {
		t.Run(name, func(t *testing.T) {
			file := readXZFixture(t, name)
			want, err := decodeXZ(xz.NewReader, file)
			if err != nil {
				t.Fatalf("the library refuses %s: %v", name, err)
			}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			got, err := decodeXZ(NewXZReader, file)
			runtime.ReadMemStats(&after)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("NewXZReader gave %d bytes and %v, want the library's %d bytes", len(got), err, len(want))
			}
			// Four streams of no sizes get 64 MiB each; blocks with sizes
			// get a dictionary of their size.
			limit := uint64(16 << 20)
			if name == "streams4-xz9.xz" {
				limit += xzFreeDictionaryBytes
			}
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated > limit {
				t.Errorf("NewXZReader allocated %d MiB for %d bytes of xz", allocated>>20, len(file))
			}
		})
	}

	// A fifth stream that declares 64 MiB and holds a line is past what
	// the budget allows.
	if _, err := decodeXZ(NewXZReader, readXZFixture(t, "streams5-xz9.xz")); !errors.Is(err, ErrXZDictionariesTooLarge) {
		t.Errorf("five streams of xz -9 holding a line each: err = %v, want ErrXZDictionariesTooLarge", err)
	}
}

// TestXZReaderGivesNoWrongDataForAHeaderThatUndersizesItsBlock: a block's
// dictionary is cut to the size its header declares, so a header could
// declare too small a size for the matches its data makes. That must fail
// and never decode a match from the wrong place: the decoder refuses a
// match farther back than its dictionary, and NewXZReader a block that
// decodes more than its header says.
func TestXZReaderGivesNoWrongDataForAHeaderThatUndersizesItsBlock(t *testing.T) {
	file := readXZFixture(t, "repeat-xz9-block.xz")
	want, err := decodeXZ(xz.NewReader, file)
	if err != nil {
		t.Fatal(err)
	}
	// The block header: its size, flags with both sizes, the compressed
	// size and then the uncompressed size, 10,000 in two bytes, which 4096
	// also takes.
	header := slices.Clone(file[xzStreamHeaderBytes : xzStreamHeaderBytes+(int(file[xzStreamHeaderBytes])+1)*4])
	fields := header[2:]
	compressedSize, compressedLength := binary.Uvarint(fields)
	uncompressedSize, uncompressedLength := binary.Uvarint(fields[compressedLength:])
	if header[1] != xzCompressedSizeFlag|xzUncompressedSizeFlag || uncompressedSize != 10_000 || uncompressedLength != 2 {
		t.Fatalf("block header % x is not the one this test expects", header)
	}
	binary.PutUvarint(fields[compressedLength:], 4096)
	binary.LittleEndian.PutUint32(header[len(header)-4:], crc32.ChecksumIEEE(header[:len(header)-4]))
	forged := slices.Concat(file[:xzStreamHeaderBytes], header, file[xzStreamHeaderBytes+len(header):])

	got, err := decodeXZ(NewXZReader, forged)
	if err == nil {
		t.Errorf("a block declared as 4,096 bytes that holds 10,000 decoded without an error")
	}
	if !bytes.HasPrefix(want, got) {
		t.Errorf("NewXZReader gave %d bytes that are not the start of the block's data", len(got))
	}

	// The decoder alone, given the smallest dictionary, refuses the match
	// 5,000 bytes back rather than read it from what the dictionary holds.
	lzma2 := file[xzStreamHeaderBytes+len(header):][:compressedSize]
	decoder, err := lzma.Reader2Config{DictCap: lzma.MinDictCap}.NewReader2(bytes.NewReader(lzma2))
	if err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(decoder)
	if err == nil {
		t.Errorf("a %d-byte dictionary decoded a match 5,000 bytes back", lzma.MinDictCap)
	}
	if !bytes.HasPrefix(want, got) {
		t.Errorf("the decoder gave %d bytes that are not the start of the block's data", len(got))
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
	f.Add(xzFileOfEmptyBlocks(3, 0))
	for _, name := range []string{"seq-xz9-blocks.xz", "repeat-xz9-block.xz", "streams4-xz9.xz"} {
		file, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(file)
	}
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
