package paxstrip

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"maps"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The two records Copy drops, with values shaped like the ACLs
// systemd-tmpfiles sets on /var/log/journal.
const (
	aclAccessKey  = "SCHILY.xattr.system.posix_acl_access"
	aclDefaultKey = "SCHILY.xattr.system.posix_acl_default"
	capabilityKey = "SCHILY.xattr.security.capability"
)

var (
	aclValue        = "\x02\x00\x00\x00\x01\x00\x07\x00\xff\xff\xff\xff\x04\x00\x05\x00\xff\xff\xff\xff"
	capabilityValue = "\x01\x00\x00\x02\x00\x20\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"
	fixtureTime     = time.Unix(1789662022, 0)
	// longName is longer than ustar's name and prefix can split, so the
	// writer puts it in a pax "path" record.
	longName = "usr/share/doc/" + strings.Repeat("n", 150)
)

// fixtureEntry is one entry of a test archive.
type fixtureEntry struct {
	header  tar.Header
	content string
}

// fixtureEntries is an archive shaped like a frostroot tarball: the
// journal's directory with both ACLs and a record of its own, ping with its
// capability, a directory with nothing but the ACLs, a hardlink, a device,
// a long name and contents that are not whole blocks.
func fixtureEntries() []fixtureEntry {
	directory := func(name string, records map[string]string) fixtureEntry {
		return fixtureEntry{header: tar.Header{Typeflag: tar.TypeDir, Name: name, Mode: 0o2755, Uid: 0, Gid: 101, Uname: "root", Gname: "systemd-journal", ModTime: fixtureTime, PAXRecords: records, Format: tar.FormatPAX}}
	}
	file := func(name, content string, records map[string]string) fixtureEntry {
		return fixtureEntry{header: tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o755, Size: int64(len(content)), Uname: "root", Gname: "root", ModTime: fixtureTime, PAXRecords: records, Format: tar.FormatPAX}, content: content}
	}
	return []fixtureEntry{
		directory("./", nil),
		directory("./var/log/journal/", map[string]string{aclAccessKey: aclValue, aclDefaultKey: aclValue, "SCHILY.xattr.user.note": "kept"}),
		file("./usr/bin/ping", strings.Repeat("ELF", 300), map[string]string{capabilityKey: capabilityValue}),
		directory("./var/log/other/", map[string]string{aclAccessKey: aclValue, aclDefaultKey: aclValue}),
		{header: tar.Header{Typeflag: tar.TypeLink, Name: "./usr/bin/ping4", Linkname: "./usr/bin/ping", ModTime: fixtureTime, Format: tar.FormatPAX}},
		{header: tar.Header{Typeflag: tar.TypeChar, Name: "./dev/null", Mode: 0o666, Devmajor: 1, Devminor: 3, ModTime: fixtureTime, Format: tar.FormatPAX}},
		{header: tar.Header{Typeflag: tar.TypeSymlink, Name: "./bin", Linkname: "usr/bin", ModTime: fixtureTime, Format: tar.FormatPAX}},
		file("./"+longName, "long", nil),
		file("./etc/hello", strings.Repeat("hello\n", 117), nil),
		file("./etc/empty", "", nil),
	}
}

// fixtureWithoutACLs is fixtureEntries without either ACL record.
func fixtureWithoutACLs() []fixtureEntry {
	entries := fixtureEntries()
	for index := range entries {
		records := maps.Clone(entries[index].header.PAXRecords)
		delete(records, aclAccessKey)
		delete(records, aclDefaultKey)
		entries[index].header.PAXRecords = records
	}
	return entries
}

// writeArchive writes entries with archive/tar, as an uncompressed archive.
func writeArchive(tb testing.TB, entries []fixtureEntry) []byte {
	tb.Helper()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for _, entry := range entries {
		header := entry.header
		if err := writer.WriteHeader(&header); err != nil {
			tb.Fatalf("writing %s: %v", header.Name, err)
		}
		if _, err := io.WriteString(writer, entry.content); err != nil {
			tb.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		tb.Fatal(err)
	}
	return archive.Bytes()
}

func compress(tb testing.TB, data []byte) []byte {
	tb.Helper()
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(data); err != nil {
		tb.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		tb.Fatal(err)
	}
	return compressed.Bytes()
}

func decompress(tb testing.TB, data []byte) []byte {
	tb.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		tb.Fatal(err)
	}
	content, err := io.ReadAll(reader)
	if err != nil {
		tb.Fatal(err)
	}
	return content
}

// copyArchive runs Copy over raw, compressed first, and returns the
// decompressed output.
func copyArchive(t *testing.T, raw []byte) (output []byte, dropped int) {
	t.Helper()
	var copied bytes.Buffer
	dropped, err := Copy(&copied, bytes.NewReader(compress(t, raw)))
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	return decompress(t, copied.Bytes()), dropped
}

// rawEntry is one entry of an archive as its blocks: the header block and
// the data blocks, padding included. It is read here without paxstrip's
// code, so that the tests do not check the walk against itself.
type rawEntry struct {
	header []byte
	data   []byte
}

func (e rawEntry) typeflag() byte { return e.header[156] }

// splitArchive cuts an archive with octal sizes into its entries and what
// follows the first zero block.
func splitArchive(t *testing.T, archive []byte) (entries []rawEntry, trailer []byte) {
	t.Helper()
	for offset := 0; ; {
		if offset+512 > len(archive) {
			t.Fatalf("the archive ends at byte %d without a zero block", offset)
		}
		header := archive[offset : offset+512]
		if bytes.Equal(header, make([]byte, 512)) {
			return entries, archive[offset:]
		}
		size, err := strconv.ParseInt(strings.Trim(string(header[124:136]), " \x00"), 8, 64)
		if err != nil {
			t.Fatalf("the header at byte %d: size: %v", offset, err)
		}
		if strings.ContainsRune("123456", rune(header[156])) {
			size = 0
		}
		padded := int((size + 511) / 512 * 512)
		entries = append(entries, rawEntry{header: header, data: archive[offset+512 : offset+512+padded]})
		offset += 512 + padded
	}
}

// recordsOf splits a pax header's data into its records, as text.
func recordsOf(t *testing.T, entry rawEntry) []string {
	t.Helper()
	size, err := strconv.ParseInt(strings.Trim(string(entry.header[124:136]), " \x00"), 8, 64)
	if err != nil {
		t.Fatal(err)
	}
	data := string(entry.data[:size])
	var records []string
	for data != "" {
		length, err := strconv.Atoi(data[:strings.IndexByte(data, ' ')])
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, data[:length])
		data = data[length:]
	}
	return records
}

func isACLRecord(record string) bool {
	return strings.Contains(record, " "+DroppedKeyPrefix)
}

// TestCopyDropsOnlyTheACLRecords holds Copy to what AGENTS.md promises:
// every block of the output is the block that was read, but for the pax
// headers that held an ACL record, which keep every other record in its
// order and differ in nothing but the size and checksum fields, and the
// pax header that held nothing else, which is gone whole.
func TestCopyDropsOnlyTheACLRecords(t *testing.T) {
	input := writeArchive(t, fixtureEntries())
	output, dropped := copyArchive(t, input)
	if dropped != 4 {
		t.Errorf("dropped = %d records, want 4: two on each of two directories", dropped)
	}
	inputEntries, inputTrailer := splitArchive(t, input)
	outputEntries, outputTrailer := splitArchive(t, output)
	if !bytes.Equal(inputTrailer, outputTrailer) {
		t.Errorf("what follows the last entry changed: %d bytes, want %d", len(outputTrailer), len(inputTrailer))
	}
	altered, removed := 0, 0
	next := 0
	for index, inputEntry := range inputEntries {
		if inputEntry.typeflag() != 'x' || !hasACLRecord(t, inputEntry) {
			if next >= len(outputEntries) {
				t.Fatalf("entry %d of the input is missing from the output", index)
			}
			if outputEntry := outputEntries[next]; !bytes.Equal(outputEntry.header, inputEntry.header) || !bytes.Equal(outputEntry.data, inputEntry.data) {
				t.Errorf("entry %d of the input (%q) changed in the copy", index, cString(inputEntry.header[:100]))
			}
			next++
			continue
		}
		var wantRecords []string
		for _, record := range recordsOf(t, inputEntry) {
			if !isACLRecord(record) {
				wantRecords = append(wantRecords, record)
			}
		}
		if len(wantRecords) == 0 {
			removed++
			continue
		}
		altered++
		outputEntry := outputEntries[next]
		next++
		if outputEntry.typeflag() != 'x' {
			t.Fatalf("entry %d: the rewritten pax header is of type %q", index, outputEntry.typeflag())
		}
		if got := recordsOf(t, outputEntry); strings.Join(got, "") != strings.Join(wantRecords, "") {
			t.Errorf("entry %d: records = %q, want %q", index, got, wantRecords)
		}
		for offset := range 512 {
			inSize := offset >= 124 && offset < 136
			inChecksum := offset >= 148 && offset < 156
			if !inSize && !inChecksum && outputEntry.header[offset] != inputEntry.header[offset] {
				t.Errorf("entry %d: header byte %d changed from %#x to %#x", index, offset, inputEntry.header[offset], outputEntry.header[offset])
			}
		}
		if !checksumMatches(outputEntry.header) {
			t.Errorf("entry %d: the rewritten header fails its checksum", index)
		}
	}
	if next != len(outputEntries) {
		t.Errorf("the output has %d entries, want %d", len(outputEntries), next)
	}
	if altered != 1 || removed != 1 {
		t.Errorf("altered %d pax headers and removed %d, want one of each", altered, removed)
	}
}

func hasACLRecord(t *testing.T, entry rawEntry) bool {
	t.Helper()
	for _, record := range recordsOf(t, entry) {
		if isACLRecord(record) {
			return true
		}
	}
	return false
}

func cString(field []byte) string {
	if end := bytes.IndexByte(field, 0); end >= 0 {
		return string(field[:end])
	}
	return string(field)
}

// TestCopyKeepsWhatArchiveTarReads reads both archives with archive/tar:
// the same entries, with the same names, types, modes, owners, times,
// links, devices and contents, and the same records less the two ACLs.
func TestCopyKeepsWhatArchiveTarReads(t *testing.T) {
	input := writeArchive(t, fixtureEntries())
	output, _ := copyArchive(t, input)
	inputEntries := readWithArchiveTar(t, input)
	outputEntries := readWithArchiveTar(t, output)
	if len(outputEntries) != len(inputEntries) {
		t.Fatalf("the copy has %d entries, want %d", len(outputEntries), len(inputEntries))
	}
	sawCapability := false
	for index, want := range inputEntries {
		got := outputEntries[index]
		wantRecords := maps.Clone(want.header.PAXRecords)
		delete(wantRecords, aclAccessKey)
		delete(wantRecords, aclDefaultKey)
		if !maps.Equal(got.header.PAXRecords, wantRecords) && (len(got.header.PAXRecords) != 0 || len(wantRecords) != 0) {
			t.Errorf("%s: records = %q, want %q", want.header.Name, got.header.PAXRecords, wantRecords)
		}
		if got.header.PAXRecords[capabilityKey] == capabilityValue {
			sawCapability = true
		}
		if _, kept := got.header.PAXRecords[aclAccessKey]; kept {
			t.Errorf("%s: the access ACL is still there", want.header.Name)
		}
		if gotFields, wantFields := headerFields(got.header), headerFields(want.header); gotFields != wantFields {
			t.Errorf("header changed:\n got %s\nwant %s", gotFields, wantFields)
		}
		if got.content != want.content {
			t.Errorf("%s: content changed", want.header.Name)
		}
	}
	if !sawCapability {
		t.Error("the capability of ping is gone; only the ACLs may go")
	}
}

// headerFields renders what an entry is, records apart.
func headerFields(header tar.Header) string {
	return fmt.Sprintf("name %q type %q mode %o uid %d gid %d user %q group %q mtime %s link %q device %d,%d size %d",
		header.Name, header.Typeflag, header.Mode, header.Uid, header.Gid, header.Uname, header.Gname,
		header.ModTime.UTC(), header.Linkname, header.Devmajor, header.Devminor, header.Size)
}

func readWithArchiveTar(t *testing.T, archive []byte) []fixtureEntry {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(archive))
	var entries []fixtureEntry
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return entries
		}
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, fixtureEntry{header: *header, content: string(content)})
	}
}

func TestCopyOfAnArchiveWithoutACLsIsIdentical(t *testing.T) {
	input := writeArchive(t, fixtureWithoutACLs())
	output, dropped := copyArchive(t, input)
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0", dropped)
	}
	if !bytes.Equal(output, input) {
		t.Error("the copy of an archive without ACL records differs from it")
	}
}

// TestCopyReadsBase256Sizes: GNU tar writes a size too large for octal
// digits in base 256, and the walk must still find the next header.
func TestCopyReadsBase256Sizes(t *testing.T) {
	input := writeArchive(t, fixtureEntries())
	entries, _ := splitArchive(t, input)
	offset := 0
	for _, entry := range entries {
		if cString(entry.header[:100]) == "./etc/hello" {
			break
		}
		offset += 512 + len(entry.data)
	}
	header := input[offset : offset+512]
	size, err := strconv.ParseInt(strings.Trim(string(header[124:136]), " \x00"), 8, 64)
	if err != nil {
		t.Fatal(err)
	}
	field := header[124:136]
	clear(field)
	field[0] = 0x80
	for index := 11; index > 0; index-- {
		field[index] = byte(size)
		size >>= 8
	}
	setChecksum(header)
	output, dropped := copyArchive(t, input)
	if dropped != 4 {
		t.Errorf("dropped = %d, want 4", dropped)
	}
	if got := readWithArchiveTar(t, output); len(got) != len(fixtureEntries()) {
		t.Errorf("the copy has %d entries, want %d", len(got), len(fixtureEntries()))
	}
	if !bytes.Contains(output, header) {
		t.Error("the header with a base-256 size did not pass through as it was")
	}
}

// rawHeader returns a ustar header block for a hand-made archive, with its
// checksum.
func rawHeader(name string, typeflag byte, size int64) []byte {
	header := make([]byte, 512)
	copy(header, name)
	copy(header[100:], "0000644\x00")
	copy(header[108:], "0000000\x00")
	copy(header[116:], "0000000\x00")
	copy(header[124:], fmt.Sprintf("%011o\x00", size))
	copy(header[136:], "14652632506\x00")
	copy(header[148:], "        ")
	header[156] = typeflag
	copy(header[257:], "ustar\x0000")
	setChecksum(header)
	return header
}

// paxRecordText renders one pax record, its length counting itself.
func paxRecordText(key, value string) string {
	body := " " + key + "=" + value + "\n"
	length := len(body) + 1
	for len(strconv.Itoa(length))+len(body) != length {
		length++
	}
	return strconv.Itoa(length) + body
}

func padded(data string) []byte {
	block := make([]byte, (len(data)+511)/512*512)
	copy(block, data)
	return block
}

// TestCopyFollowsThePAXSizeRecord: a pax "size" record overrides the size
// field of the entry it describes, as GNU tar writes one for a file over
// 8 GiB, and the walk must step over that many bytes.
func TestCopyFollowsThePAXSizeRecord(t *testing.T) {
	content := strings.Repeat("z", 600)
	records := paxRecordText("size", "600") + paxRecordText(aclAccessKey, aclValue)
	var archive []byte
	archive = append(archive, rawHeader("PaxHeaders/big", 'x', int64(len(records)))...)
	archive = append(archive, padded(records)...)
	archive = append(archive, rawHeader("big", '0', 0)...)
	archive = append(archive, padded(content)...)
	archive = append(archive, make([]byte, 1024)...)
	output, dropped := copyArchive(t, archive)
	if dropped != 1 {
		t.Errorf("dropped = %d, want 1", dropped)
	}
	entries := readWithArchiveTar(t, output)
	if len(entries) != 1 || entries[0].content != content {
		t.Fatalf("read back %d entries, want the one file of 600 bytes", len(entries))
	}
	if _, kept := entries[0].header.PAXRecords[aclAccessKey]; kept {
		t.Error("the ACL record is still there")
	}
}

func TestCopyRefuses(t *testing.T) {
	archive := writeArchive(t, fixtureEntries())
	entries, _ := splitArchive(t, archive)
	// Offsets of the parts of the archive the cuts aim at.
	var firstPAXData, helloData, secondHeader int
	offset := 0
	for index, entry := range entries {
		switch {
		case entry.typeflag() == 'x' && firstPAXData == 0:
			firstPAXData = offset + 512
		case cString(entry.header[:100]) == "./etc/hello":
			helloData = offset + 512
		}
		if index == 1 {
			secondHeader = offset
		}
		offset += 512 + len(entry.data)
	}
	withChange := func(change func(archive []byte)) []byte {
		changed := bytes.Clone(archive)
		change(changed)
		return changed
	}
	// archive/tar refuses to write a pax header this large, so it is made
	// by hand.
	comment := paxRecordText("comment", strings.Repeat("c", MaxPAXHeaderSize))
	oversized := append(append(append(rawHeader("PaxHeaders/big", 'x', int64(len(comment))), padded(comment)...), rawHeader("big", '0', 0)...), make([]byte, 1024)...)
	hardlinkWithSize := append(append(rawHeader("link", '1', 512), make([]byte, 512)...), make([]byte, 1024)...)
	// A byte flipped in the middle of the deflate stream: gzip's checksum,
	// the inflater or a header's checksum finds it, whichever comes first.
	corrupt := compress(t, archive)
	corrupt[len(corrupt)/2] ^= 0xff
	for _, testCase := range []struct {
		name       string
		compressed []byte
		wantInErr  string
	}{
		{"cut in the middle of a header", compress(t, archive[:secondHeader+100]), "ends in the middle of a header"},
		{"cut in the middle of the data", compress(t, archive[:helloData+300]), "ends in the middle of an entry's data"},
		{"cut in the middle of a pax header", compress(t, archive[:firstPAXData+20]), "ends in the middle of a pax header"},
		{"cut before the end-of-archive marker", compress(t, archive[:offset]), "without the end-of-archive marker"},
		{"empty", compress(t, nil), "without the end-of-archive marker"},
		{"a header that fails its checksum", compress(t, withChange(func(archive []byte) { archive[secondHeader+10] ^= 1 })), "fails its checksum"},
		{"not gzip", archive, "gzip"},
		{"a pax header over the bound", compress(t, oversized), "more than the"},
		{"a record without its newline", compress(t, withChange(func(archive []byte) {
			end := bytes.IndexByte(archive[firstPAXData:], '\n')
			archive[firstPAXData+end] = 'X'
		})), "the pax header"},
		{"a record with a length that does not fit", compress(t, withChange(func(archive []byte) { archive[firstPAXData] = '9' })), "the pax header"},
		{"a size that is not octal", compress(t, withChange(func(archive []byte) {
			archive[secondHeader+124] = '9'
			setChecksum(archive[secondHeader : secondHeader+512])
		})), "not an octal number"},
		{"a hardlink with data", compress(t, hardlinkWithSize), "which has no data"},
		{"a corrupt gzip stream", corrupt, ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var output bytes.Buffer
			_, err := Copy(&output, bytes.NewReader(testCase.compressed))
			if !errors.Is(err, ErrNotTarball) {
				t.Fatalf("Copy = %v, want an error wrapping ErrNotTarball", err)
			}
			if !strings.Contains(err.Error(), testCase.wantInErr) {
				t.Errorf("Copy = %q, want it to say %q", err, testCase.wantInErr)
			}
		})
	}
}

// failingReader fails as a disk does, after some bytes.
type failingReader struct {
	data []byte
}

var errDisk = errors.New("input/output error")

func (r *failingReader) Read(buffer []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, errDisk
	}
	count := copy(buffer, r.data)
	r.data = r.data[count:]
	return count, nil
}

// failingWriter fails as a full disk does.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errDisk }

// TestCopyTellsTheInputFromTheOutput: a failing disk under the input or
// the output is not a malformed archive.
func TestCopyTellsTheInputFromTheOutput(t *testing.T) {
	compressed := compress(t, writeArchive(t, fixtureEntries()))
	_, err := Copy(io.Discard, &failingReader{data: compressed[:len(compressed)/2]})
	if !errors.Is(err, errDisk) || errors.Is(err, ErrNotTarball) {
		t.Errorf("Copy from a failing reader = %v, want its error and not ErrNotTarball", err)
	}
	_, err = Copy(failingWriter{}, bytes.NewReader(compressed))
	if !errors.Is(err, errDisk) || errors.Is(err, ErrNotTarball) {
		t.Errorf("Copy to a failing writer = %v, want its error and not ErrNotTarball", err)
	}
}

// FuzzCopy: the archive is a file someone hands over. Copy refuses what it
// cannot read without a panic, and what it accepts it accepts again, with
// nothing left to drop and the same bytes.
func FuzzCopy(f *testing.F) {
	f.Add(writeArchive(f, fixtureEntries()))
	f.Add(writeArchive(f, fixtureWithoutACLs()))
	records := paxRecordText("size", "3") + paxRecordText(aclDefaultKey, aclValue)
	f.Add(append(append(append(append(rawHeader("PaxHeaders/x", 'x', int64(len(records))), padded(records)...), rawHeader("x", '0', 0)...), padded("abc")...), make([]byte, 1024)...))
	f.Fuzz(func(t *testing.T, raw []byte) {
		var first bytes.Buffer
		dropped, err := Copy(&first, bytes.NewReader(compress(t, raw)))
		if err != nil {
			if !errors.Is(err, ErrNotTarball) {
				t.Fatalf("Copy = %v, which does not wrap ErrNotTarball", err)
			}
			return
		}
		firstOutput := decompress(t, first.Bytes())
		if dropped == 0 && !bytes.Equal(firstOutput, raw) {
			t.Fatal("nothing was dropped, yet the copy differs from the input")
		}
		var second bytes.Buffer
		droppedAgain, err := Copy(&second, bytes.NewReader(first.Bytes()))
		if err != nil {
			t.Fatalf("Copy of the copy = %v", err)
		}
		if droppedAgain != 0 {
			t.Fatalf("the copy still had %d records to drop", droppedAgain)
		}
		if !bytes.Equal(decompress(t, second.Bytes()), firstOutput) {
			t.Fatal("the copy of the copy differs from the copy")
		}
	})
}
