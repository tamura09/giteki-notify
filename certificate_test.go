package main

import (
	"reflect"
	"strings"
	"testing"
)

// ucCastPro is the shape that makes grouping necessary: one 認証番号 covering
// three 工事設計 records, two of which share a 特定無線設備の種別.
func ucCastPro() []record {
	return []record{
		testRecord("020-250266", "2026-08-25", "ＵＣ－Ｃａｓｔ－Ｐｒｏ", "第２条第１９号に規定する特定無線設備", `Ｇ１Ｄ　2412～2472ＭＨz`),
		testRecord("020-250266", "2026-08-25", "ＵＣ－Ｃａｓｔ－Ｐｒｏ", "第２条第１９号に規定する特定無線設備", `Ｆ１Ｄ　2441ＭＨz`),
		testRecord("020-250266", "2026-08-25", "ＵＣ－Ｃａｓｔ－Ｐｒｏ", "第２条第１９号の３に規定する特定無線設備", `Ｄ１Ｄ，Ｇ１Ｄ　5.18～5.32ＧＨz`),
	}
}

func TestGroupByNumberCollectsRecordsAndOrdersNewestFirst(t *testing.T) {
	records := append(ucCastPro(),
		testRecord("201-250579", "2026-08-14", "Ｕ７－Ｐｒｏ－ＸＧＳ", "第２条第１９号に規定する特定無線設備", "Ｇ１Ｄ"),
	)

	grouped := groupByNumber(records)

	if len(grouped) != 2 {
		t.Fatalf("grouped into %d certificates, want 2", len(grouped))
	}
	if grouped[0].Number != "020-250266" {
		t.Errorf("first certificate = %s, want the newest (020-250266)", grouped[0].Number)
	}
	if len(grouped[0].Records) != 3 {
		t.Errorf("020-250266 covers %d records, want 3", len(grouped[0].Records))
	}
}

func TestGroupByNumberDropsRecordsWithNoNumber(t *testing.T) {
	records := []record{
		testRecord("", "2026-08-25", "Ｕ７", "第２条第１９号に規定する特定無線設備", "Ｇ１Ｄ"),
		testRecord("020-250266", "2026-08-25", "Ｕ７", "第２条第１９号に規定する特定無線設備", "Ｇ１Ｄ"),
	}

	// A record with no number cannot be tracked between runs, so keeping it
	// would mean reporting it on every single run.
	grouped := groupByNumber(records)
	if len(grouped) != 1 || grouped[0].Number != "020-250266" {
		t.Fatalf("grouped = %+v, want only 020-250266", grouped)
	}
}

func TestCertificateFlattensAndDeduplicatesItsFields(t *testing.T) {
	grouped := groupByNumber(ucCastPro())
	certificate := grouped[0]

	if got := certificate.TypeName(); got != "UC-Cast-Pro" {
		t.Errorf("TypeName = %q, want the narrowed UC-Cast-Pro", got)
	}
	if got := certificate.ApplicantName(); got != "Ubiquiti Inc." {
		t.Errorf("ApplicantName = %q, want Ubiquiti Inc.", got)
	}
	if got := certificate.Date(); got != "2026-08-25" {
		t.Errorf("Date = %q, want 2026-08-25", got)
	}

	wantKinds := []string{
		"第2条第19号に規定する特定無線設備",
		"第2条第19号の3に規定する特定無線設備",
	}
	if got := certificate.EquipmentKinds(); !reflect.DeepEqual(got, wantKinds) {
		// Two of the three records share a kind; it must appear once.
		t.Errorf("EquipmentKinds = %q, want %q", got, wantKinds)
	}

	wantWaves := []string{"G1D 2412~2472MHz", "F1D 2441MHz", "D1D,G1D 5.18~5.32GHz"}
	if got := certificate.ElecWaves(); !reflect.DeepEqual(got, wantWaves) {
		t.Errorf("ElecWaves = %q, want %q", got, wantWaves)
	}

	if got := certificate.TechCodes(); !reflect.DeepEqual(got, []string{"登録証明機関による工事設計認証"}) {
		t.Errorf("TechCodes = %q, want one entry", got)
	}
}

func TestCertificateTakesTheLatestDate(t *testing.T) {
	records := []record{
		testRecord("020-250266", "2026-08-25", "Ｕ７", "第２条第１９号に規定する特定無線設備", "Ｇ１Ｄ"),
		testRecord("020-250266", "2026-09-01", "Ｕ７", "第２条第１９号に規定する特定無線設備", "Ｇ１Ｄ"),
	}

	if got := groupByNumber(records)[0].Date(); got != "2026-09-01" {
		t.Errorf("Date = %q, want the later 2026-09-01", got)
	}
}

func TestCertificateExteriorAttachmentURLSkipsRecordsWithout(t *testing.T) {
	first := testRecord("020-250266", "2026-08-25", "Ｕ７", "第２条第１９号に規定する特定無線設備", "Ｇ１Ｄ")
	first.AttachmentFileKey = ""
	first.AttachmentCount1 = ""
	second := testRecord("020-250266", "2026-08-25", "Ｕ７", "第２条第１９号の３に規定する特定無線設備", "Ｄ１Ｄ")

	certificate := groupByNumber([]record{first, second})[0]

	link := certificate.ExteriorAttachmentURL("https://www.tele.soumu.go.jp/giteki")
	if link == "" {
		t.Fatal("ExteriorAttachmentURL is empty; the second record has a key")
	}
	if want := "AFK=" + second.AttachmentFileKey; !strings.Contains(link, want) {
		t.Errorf("link = %s, want it to carry %s", link, want)
	}
}
