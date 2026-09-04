// PalmDOC (LZ77-style) record decompression.
//
// Ported with attribution from KindleUnpack's PalmdocReader.unpack
// (lib/mobi_uncompress.py, GPL-3.0,
// https://github.com/kevinhendricks/KindleUnpack), cross-checked
// opcode for opcode against foliate-js's decompressPalmDOC (mobi.js,
// MIT, https://github.com/johnfactotum/foliate-js). The two agree on
// every opcode.

package mobi

import "fmt"

// palmdocTextRecordSize is the text record size virtually every MOBI
// file declares (the PalmDOC header's recordSize field). Decompression
// buffers preallocate on it so the common per-record decompress is a
// single allocation followed by cheap appends.
const palmdocTextRecordSize = 4096

// decompressPalmDOC decompresses one PalmDOC-compressed record.
//
// The format is byte-oriented LZ77 with a single opcode space
// (big-endian):
//
//	0x00       literal 0x00
//	0x01–0x08  copy the next 1–8 bytes literally
//	0x09–0x7F  literal: the byte itself
//	0x80–0xBF  2-byte back-reference: pair = (b<<8)|next,
//	           distance = (pair & 0x3FFF) >> 3,
//	           length = (pair & 0x7) + 3; copies may overlap their own
//	           output (distance < length repeats), which is how runs
//	           are encoded
//	0xC0–0xFF  two literals: 0x20 (space), then b^0x80
//
// Truncated input — a counted run or a back-reference pair cut short
// at the end of the record — and back-references pointing before the
// start of the output (distance 0, or distance beyond what has been
// produced) are corrupt: ErrCorrupt, never a panic.
func decompressPalmDOC(src []byte) ([]byte, error) {
	capacity := palmdocTextRecordSize
	if len(src) > capacity {
		capacity = len(src)
	}
	dst := make([]byte, 0, capacity)
	for p := 0; p < len(src); {
		b := src[p]
		p++
		switch {
		case b >= 0x01 && b <= 0x08:
			if p+int(b) > len(src) {
				return nil, fmt.Errorf("%w: literal run of %d bytes at offset %d runs past the %d-byte record",
					ErrCorrupt, b, p-1, len(src))
			}
			dst = append(dst, src[p:p+int(b)]...)
			p += int(b)
		case b < 0x80: // 0x00 and 0x09–0x7F: the byte is itself
			dst = append(dst, b)
		case b < 0xC0:
			if p >= len(src) {
				return nil, fmt.Errorf("%w: back-reference at offset %d is missing its second byte",
					ErrCorrupt, p-1)
			}
			pair := int(b)<<8 | int(src[p])
			p++
			dist := (pair & 0x3FFF) >> 3
			length := pair&0x7 + 3
			if dist == 0 || dist > len(dst) {
				return nil, fmt.Errorf("%w: back-reference distance %d at offset %d with %d bytes of output",
					ErrCorrupt, dist, p-2, len(dst))
			}
			// Byte-at-a-time copy: a back-reference may overlap its own
			// output, so the source bytes may not exist yet at slicing
			// time.
			start := len(dst) - dist
			for i := range length {
				dst = append(dst, dst[start+i])
			}
		default: // 0xC0–0xFF: space + char pair
			dst = append(dst, 0x20, b^0x80)
		}
	}
	return dst, nil
}
