package main

import (
	"reflect"
	"testing"
)

func TestNarrowRewritesFullWidthLatinAndLeavesJapaneseAlone(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		given string
		want  string
	}{
		{
			name:  "model number",
			given: "ＵＣ－Ｃａｓｔ－Ｐｒｏ",
			want:  "UC-Cast-Pro",
		},
		{
			name:  "company name with an ideographic space",
			given: "Ｕｂｉｑｕｉｔｉ　Ｉｎｃ．",
			want:  "Ubiquiti Inc.",
		},
		{
			name: "mixed full-width and half-width, as the register stores 電波の型式",
			// The digits here really are half-width in the live data while the
			// letters are not.
			given: "Ｇ１Ｄ　2412～2472ＭＨz(５ＭＨz間隔13波)　0.008157Ｗ／ＭＨz",
			want:  "G1D 2412~2472MHz(5MHz間隔13波) 0.008157W/MHz",
		},
		{
			name:  "Japanese is left as it is",
			given: "第２条第１９号に規定する特定無線設備",
			want:  "第2条第19号に規定する特定無線設備",
		},
		{
			name:  "a dash that is not in the full-width block survives",
			given: "―",
			want:  "―",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := narrow(testCase.given); got != testCase.want {
				t.Errorf("narrow(%q) = %q, want %q", testCase.given, got, testCase.want)
			}
		})
	}
}

func TestCleanLinesSplitsTheRegistersEscapedLineBreaks(t *testing.T) {
	// The API escapes its line breaks, so what arrives in Go is a backslash
	// followed by an n rather than a newline.
	given := `Ｇ１Ｄ　2412ＭＨz\nＤ１Ｄ　2422ＭＨz\n`

	got := cleanLines(given)
	want := []string{"G1D 2412MHz", "D1D 2422MHz"}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("cleanLines(%q) = %q, want %q", given, got, want)
	}
}

func TestCleanLinesAlsoSplitsRealNewlines(t *testing.T) {
	got := cleanLines("first\n\nsecond")
	want := []string{"first", "second"}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("cleanLines = %q, want %q", got, want)
	}
}

func TestCleanLinesOnAnEmptyFieldIsEmpty(t *testing.T) {
	if got := cleanLines("   "); len(got) != 0 {
		t.Errorf("cleanLines on blank input = %q, want nothing", got)
	}
}
