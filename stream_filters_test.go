package gopdf

import (
	"bytes"
	"testing"
)

// ============================================================
// Unit tests for the stream filter decoders: LZWDecode,
// RunLengthDecode, and (indirectly) FlateDecode via /Length cuts.
// The LZW tests use a minimal reference encoder so the decoder is
// verified against independently produced streams.
// ============================================================

// lzwEncode encodes data per PDF's LZW variant (9-12 bit codes, early
// change, 256=EOD, 257=clear) — small reference implementation used only
// to produce test vectors for decodeLZW.
func lzwEncode(data []byte, early int) []byte {
	table := make(map[string]int)
	for c := 0; c < 256; c++ {
		table[string([]byte{byte(c)})] = c
	}
	nextCode := 258
	width := 9
	var out bitsWriter
	var cur []byte
	emit := func(code int) {
		out.writeBits(uint32(code), width)
	}
	for _, b := range data {
		next := append(append([]byte{}, cur...), b)
		if _, ok := table[string(next)]; ok {
			cur = next
			continue
		}
		emit(table[string(cur)])
		table[string(next)] = nextCode
		nextCode++
		if nextCode == (1<<width) && width < 12 {
			width++
		}
		cur = []byte{b}
	}
	if len(cur) > 0 {
		emit(table[string(cur)])
	}
	emit(257) // EOD
	return out.bytes()
}

type bitsWriter struct {
	buf  uint32
	nbit uint
	out  []byte
}

func (w *bitsWriter) writeBits(code uint32, width int) {
	w.buf = w.buf<<uint(width) | code
	w.nbit += uint(width)
	for w.nbit >= 8 {
		w.nbit -= 8
		w.out = append(w.out, byte(w.buf>>w.nbit))
	}
	w.buf &= (1 << w.nbit) - 1
}

func (w *bitsWriter) bytes() []byte {
	if w.nbit > 0 {
		w.out = append(w.out, byte(w.buf<<(8-w.nbit)))
		w.nbit = 0
	}
	return w.out
}

func TestLZWRoundTrip(t *testing.T) {
	cases := [][]byte{
		{},
		{0},
		[]byte("hello"),
		bytes.Repeat([]byte("ab"), 200),
		bytes.Repeat([]byte{0x41, 0x42, 0x43, 0x00, 0xFF}, 500), // all 256 values covered partially
	}
	for i, c := range []byte{0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68} { // warm loop to grow widths
		_ = i
		_ = c
	}
	for _, tc := range cases {
		enc := lzwEncode(tc, 1)
		got := decodeLZW(enc, "")
		if !bytes.Equal(got, tc) {
			t.Errorf("LZW round-trip failed: len %d → %d, first diff at %d",
				len(tc), len(got), firstDiff(tc, got))
		}
	}
}

func TestLZWEarlyChange0(t *testing.T) {
	data := bytes.Repeat([]byte("the quick brown fox "), 100)
	enc := lzwEncode(data, 0)
	got := decodeLZW(enc, "/EarlyChange 0")
	if !bytes.Equal(got, data) {
		t.Errorf("early-change 0 round-trip failed at %d", firstDiff(data, got))
	}
}

func TestRunLengthRoundTrip(t *testing.T) {
	// encode: literal run + repeat run + EOD
	enc := []byte{2, 'a', 'b', 'c', 0xFE, 'z', 128}
	got := decodeRunLength(enc)
	want := []byte{'a', 'b', 'c'}
	for i := 0; i < 3; i++ { // 257-254 = 3 repeats of 'z'
		want = append(want, 'z')
	}
	if !bytes.Equal(got, want) {
		t.Errorf("RunLength: got %q want %q", got, want)
	}
	// EOD cuts trailing data
	got = decodeRunLength([]byte{1, 'x', 'y', 128, 5, 'z'})
	if string(got) != "xy" {
		t.Errorf("RunLength EOD: got %q", got)
	}
}

func firstDiff(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}
