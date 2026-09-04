// Test-side PalmDOC compressor.
//
// Greedy and literal-first, per the fixture stance: real books are
// never committed, so tests compress their own synthetic text. The
// encoder emits self-literals, counted verbatim runs for bytes that
// have no self encoding (0x01–0x08, 0x80–0xFF), and — as its only
// back-reference form — overlap copies (distance 1) for runs of a
// repeated byte, so end-to-end fixtures exercise the pair opcodes too.

package testutil

// CompressPalmDOC compresses src into a valid PalmDOC stream that
// decompressPalmDOC decodes back to src byte for byte.
func CompressPalmDOC(src []byte) []byte {
	var out []byte
	i := 0
	for i < len(src) {
		b := src[i]
		if b >= 0x01 && b <= 0x08 || b >= 0x80 {
			// Bytes with no self-literal encoding ride in counted
			// verbatim runs of up to 8.
			n := 0
			for i+n < len(src) && n < 8 {
				c := src[i+n]
				if c >= 0x01 && c <= 0x08 || c >= 0x80 {
					n++
				} else {
					break
				}
			}
			out = append(out, byte(n))
			out = append(out, src[i:i+n]...)
			i += n
			continue
		}
		// Self-literal byte. A run of 4 or more repeats becomes one
		// literal plus distance-1 overlap back-references.
		run := 1
		for i+run < len(src) && src[i+run] == b && run < 10 {
			run++
		}
		if run >= 4 {
			out = append(out, b)
			i++
			for i < len(src) && src[i] == b {
				take := 1
				for i+take < len(src) && src[i+take] == b && take < 10 {
					take++
				}
				if take < 3 {
					for range take {
						out = append(out, b)
					}
				} else {
					pair := uint16(1<<3 | uint16(take-3))
					out = append(out, byte(0x80|pair>>8), byte(pair))
				}
				i += take
			}
			continue
		}
		out = append(out, b)
		i++
	}
	return out
}
