// Text decoding for MOBI codepages.
//
// The MOBI header declares one of two encodings: 65001 (UTF-8) or 1252
// (windows-1252). UTF-8 is handled by the standard library; the
// windows-1252 table below is transcribed from the Unicode
// consortium's public-domain mapping file
// (https://unicode.org/Public/MAPPINGS/VENDORS/MICSFT/WINDOWS/CP1252.TXT),
// with the five unassigned slots (0x81, 0x8D, 0x8F, 0x90, 0x9D)
// mapped to their C1 control counterparts, which round-trips
// byte-for-byte. No x/text dependency: the table is data, not code.

package mobi

import "strings"

// Codepages declared in the MOBI header's encoding field.
const (
	codepageUTF8   uint32 = 65001
	codepageCP1252 uint32 = 1252
)

// cp1252High maps bytes 0x80–0x9F to Unicode code points; everything
// else in windows-1252 (ASCII and the 0xA0–0xFF Latin-1 range) is its
// own code point.
var cp1252High = [32]rune{
	0x20AC, 0x0081, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
	0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x008D, 0x017D, 0x008F,
	0x0090, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x009D, 0x017E, 0x0178,
}

// decodeString decodes text bytes stored with the given MOBI codepage
// (already validated as 65001 or 1252). It is the single translation
// function for record text: later stages decode every string through
// it, doing byte-offset math on the raw bytes first.
func decodeString(codepage uint32, b []byte) string {
	if codepage == codepageUTF8 {
		return string(b)
	}
	ascii := true
	for _, c := range b {
		if c >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return string(b)
	}
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		if c >= 0x80 && c <= 0x9F {
			sb.WriteRune(cp1252High[c-0x80])
		} else {
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}
