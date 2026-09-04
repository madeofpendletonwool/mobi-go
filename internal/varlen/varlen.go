// Package varlen holds the variable-length quantity codecs shared by
// the text and index layers: MOBI stores sizes and index tag values as
// 7-bits-per-byte quantities, and both the trailing-entry stripper
// (text.go) and the INDX tag-value reader (indx.go) decode them.
//
// Ported with attribution from KindleUnpack's
// getVariableWidthValue / getSizeOfTrailingDataEntry
// (lib/mobi_index.py, GPL-3.0,
// https://github.com/kevinhendricks/KindleUnpack), with foliate-js's
// getVarLen / getVarLenFromEnd (mobi.js, MIT,
// https://github.com/johnfactotum/foliate-js) as the structural
// cross-check; the two agree byte for byte.
package varlen

// maxBytes caps a forward read: 9 bytes carry 63 bits, the most an int
// can hold on every supported platform. Longer streams are corrupt.
const maxBytes = 9

// Read decodes a forward variable-length quantity at b[i]: 7 bits per
// byte, most-significant group first, terminated by a byte with its
// high bit set. It returns the value, the number of bytes consumed, and
// whether the read was well-formed: a quantity that runs past the end
// of b without a terminator, or that would overflow 63 bits, reports
// ok=false with the bytes it did consume.
func Read(b []byte, i int) (value, size int, ok bool) {
	v := 0
	for n := 0; n < maxBytes; n++ {
		if i+n >= len(b) {
			return v, n, false
		}
		c := b[i+n]
		v = v<<7 | int(c&0x7F)
		if c&0x80 != 0 {
			return v, n + 1, true
		}
	}
	// Ten-plus bytes with no terminator: the accumulated value can no
	// longer be trusted (and would overflow int on the next shift).
	return v, maxBytes, false
}

// FromEnd reads a variable-length quantity from the end of b, the way
// the text layer sizes trailing data entries: the last (up to) four
// bytes, where a byte with its high bit set starts the value — reading
// forward, the accumulator resets at each high-bit byte and whatever
// remains is the value. KindleUnpack and foliate-js agree on this form.
func FromEnd(b []byte) int {
	n := 0
	start := len(b) - 4
	if start < 0 {
		start = 0
	}
	for _, v := range b[start:] {
		if v&0x80 != 0 {
			n = 0
		}
		n = n<<7 | int(v&0x7F)
	}
	return n
}

// Append encodes v as a forward variable-length quantity, the inverse
// of Read: 7-bit groups, most significant first, every byte but the
// last with its high bit clear, the last with it set. It is the fixture
// encoder's counterpart to the shipped decoders.
func Append(dst []byte, v int) []byte {
	var groups [maxBytes]byte
	n := 0
	for {
		groups[n] = byte(v & 0x7F)
		n++
		v >>= 7
		if v == 0 {
			break
		}
	}
	// groups holds least-significant first; emit most-significant
	// first, setting the terminator bit on the final byte.
	for i := n - 1; i >= 0; i-- {
		c := groups[i]
		if i == 0 {
			c |= 0x80
		}
		dst = append(dst, c)
	}
	return dst
}
