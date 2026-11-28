package record

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
)

// Recording buffer sizes. Unbuffered, the pion Ogg/IVF writers issue one
// write(2) per page or frame, as the WAV writer did before wavBufSize; these
// batch them. A crash loses at most a buffer's worth of a leg's tail.
const (
	oggBufSize   = 8 << 10  // ~2 s of 16 kHz Opus with Ogg page overhead
	videoBufSize = 32 << 10 // a few frames of a camera or screen share
)

// bufferedFile is an *os.File behind a bufio.Writer for the pion media
// writers. Seek flushes first, so a writer that patches its header on Close
// (ivfwriter's frame count) lands the patch in the right place.
type bufferedFile struct {
	f   *os.File
	buf *bufio.Writer
	pos int64 // offset of the next byte written
}

func newBufferedFile(path string, size int) (*bufferedFile, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &bufferedFile{f: f, buf: bufio.NewWriterSize(f, size)}, nil
}

func (b *bufferedFile) Write(p []byte) (int, error) {
	n, err := b.buf.Write(p)
	b.pos += int64(n)
	return n, err
}

func (b *bufferedFile) Seek(offset int64, whence int) (int64, error) {
	if err := b.buf.Flush(); err != nil {
		return 0, err
	}
	pos, err := b.f.Seek(offset, whence)
	if err == nil {
		b.pos = pos
	}
	return pos, err
}

func (b *bufferedFile) Close() error {
	if err := b.buf.Flush(); err != nil {
		_ = b.f.Close()
		return err
	}
	return b.f.Close()
}

// oggFile is a bufferedFile for oggwriter.NewWith. Given a stream instead of
// a file, oggwriter's Close skips re-flagging the last page as end of stream,
// so oggFile does it: oggwriter writes each page in one Write, so the last
// Write is the last page. The result matches oggwriter.New byte for byte.
type oggFile struct {
	*bufferedFile
	lastOff int64
	lastLen int
}

func newOggFile(path string) (*oggFile, error) {
	b, err := newBufferedFile(path, oggBufSize)
	if err != nil {
		return nil, err
	}
	return &oggFile{bufferedFile: b}, nil
}

func (o *oggFile) Write(p []byte) (int, error) {
	o.lastOff, o.lastLen = o.pos, len(p)
	return o.bufferedFile.Write(p)
}

// Ogg page header fields (RFC 3533 §6).
const (
	oggPageHeaderSize = 27
	oggHeaderTypeEOS  = 0x04
	oggCRCOffset      = 22
)

func (o *oggFile) Close() error {
	if err := o.buf.Flush(); err != nil {
		_ = o.f.Close()
		return err
	}
	if err := o.markEOS(); err != nil {
		_ = o.f.Close()
		return err
	}
	return o.f.Close()
}

// markEOS rewrites the last page's header type as end of stream and its CRC.
func (o *oggFile) markEOS() error {
	if o.lastLen < oggPageHeaderSize {
		return nil // nothing written
	}
	page := make([]byte, o.lastLen)
	if _, err := o.f.ReadAt(page, o.lastOff); err != nil {
		return err
	}
	if string(page[:4]) != "OggS" {
		return nil // not a page boundary: leave the file as written
	}
	page[5] = oggHeaderTypeEOS
	binary.LittleEndian.PutUint32(page[oggCRCOffset:], 0)
	binary.LittleEndian.PutUint32(page[oggCRCOffset:], oggCRC(page))
	_, err := o.f.WriteAt(page, o.lastOff)
	return err
}

// oggCRCTable is Ogg's CRC-32: polynomial 0x04c11db7, MSB first, no reflection.
var oggCRCTable = func() (t [256]uint32) {
	for i := range t {
		r := uint32(i) << 24
		for range 8 {
			if r&0x80000000 != 0 {
				r = r<<1 ^ 0x04c11db7
			} else {
				r <<= 1
			}
		}
		t[i] = r
	}
	return t
}()

func oggCRC(page []byte) uint32 {
	var crc uint32
	for _, b := range page {
		crc = crc<<8 ^ oggCRCTable[byte(crc>>24)^b]
	}
	return crc
}

var (
	_ io.WriteSeeker = (*bufferedFile)(nil)
	_ io.Closer      = (*oggFile)(nil)
)
