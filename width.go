package main

import "strings"

// narrow rewrites the full-width Latin letters, digits and punctuation the
// register stores into their half-width equivalents, and the ideographic space
// into a plain one.
//
// Every Latin string in this data is full-width: a model number arrives as
// 「ＵＣ－Ｃａｓｔ－Ｐｒｏ」 and a company as 「Ｕｂｉｑｕｉｔｉ　Ｉｎｃ．」.
// Posted as-is they are readable but wrong -- nobody searches for a product by
// its full-width name, and the Discord message is the thing someone copies a
// model number out of.
//
// Japanese text is left alone. Kana and kanji have no half-width form worth
// using here, and the 〜間隔13波 fragments inside 電波の型式 read correctly as
// they are.
//
// This is display-only. Search conditions and state keys use the values exactly
// as the API returned them, so normalising differently later cannot silently
// re-notify everything.
func narrow(value string) string {
	var out strings.Builder
	out.Grow(len(value))

	for _, r := range value {
		switch {
		case r >= '！' && r <= '～':
			// U+FF01..U+FF5E, the full-width block that maps one-to-one onto
			// U+0021..U+007E.
			out.WriteRune(r - 0xFEE0)
		case r == '　':
			out.WriteRune(' ')
		default:
			out.WriteRune(r)
		}
	}

	return out.String()
}

// cleanLines turns one of the API's multi-line fields into a slice of non-empty
// lines. The API escapes its line breaks as a literal backslash-n inside the
// JSON string -- "\\n" on the wire, so what arrives in Go is the two characters
// rather than a newline -- which is why both spellings are split on.
func cleanLines(value string) []string {
	replaced := strings.ReplaceAll(value, `\n`, "\n")

	lines := make([]string, 0, 4)
	for _, line := range strings.Split(replaced, "\n") {
		trimmed := strings.TrimSpace(narrow(line))
		if trimmed != "" {
			lines = append(lines, trimmed)
		}
	}

	return lines
}
