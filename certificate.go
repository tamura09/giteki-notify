package main

import (
	"sort"
	"strings"
)

// certificate is every record sharing one 証明番号 / 認証番号. The register stores
// one record per 工事設計 -- a single access point is three or four of them, one
// per band -- and posting each separately would mean three Discord messages for
// one product. The number is what a person would call "a 技適", so it is the
// unit this function notifies on.
type certificate struct {
	Number  string
	Records []record
}

// Date is the certification date. Every record under one number shares it; the
// newest is taken in case they ever do not.
func (c certificate) Date() string {
	latest := ""
	for _, item := range c.Records {
		if item.Date > latest {
			latest = item.Date
		}
	}
	return latest
}

// TypeName is the 機器の型式又は名称, narrowed for display. Taken from the first
// record: the records under one number describe one product.
func (c certificate) TypeName() string {
	for _, item := range c.Records {
		if trimmed := strings.TrimSpace(item.TypeName); trimmed != "" {
			return narrow(trimmed)
		}
	}
	return ""
}

func (c certificate) ApplicantName() string {
	for _, item := range c.Records {
		if trimmed := strings.TrimSpace(item.Name); trimmed != "" {
			return narrow(trimmed)
		}
	}
	return ""
}

// EquipmentKinds is the 特定無線設備の種別 of every record under this number,
// de-duplicated and in the order the API returned them. This is the list that
// says which bands the product was certified for.
func (c certificate) EquipmentKinds() []string {
	return distinctValues(c.Records, func(item record) string { return item.RadioEquipmentCode })
}

// TechCodes is the 技術基準適合証明等の種類 -- 工事設計認証, 技術基準適合証明, and
// whether it came through a 登録証明機関 or 相互承認(MRA).
func (c certificate) TechCodes() []string {
	return distinctValues(c.Records, func(item record) string { return item.TechCode })
}

func (c certificate) OrganNames() []string {
	return distinctValues(c.Records, func(item record) string { return item.OrganName })
}

// ElecWaves is 電波の型式、周波数及び空中線電力 across every record, flattened: one
// record's field is itself several lines.
func (c certificate) ElecWaves() []string {
	seen := map[string]struct{}{}
	values := make([]string, 0, len(c.Records)*2)

	for _, item := range c.Records {
		for _, line := range cleanLines(item.ElecWave) {
			if _, duplicate := seen[line]; duplicate {
				continue
			}
			seen[line] = struct{}{}
			values = append(values, line)
		}
	}

	return values
}

// Notes is 備考, which is where the register writes the amendment history.
func (c certificate) Notes() []string {
	seen := map[string]struct{}{}
	values := make([]string, 0, 4)

	for _, item := range c.Records {
		for _, line := range cleanLines(item.Note) {
			if _, duplicate := seen[line]; duplicate {
				continue
			}
			seen[line] = struct{}{}
			values = append(values, line)
		}
	}

	return values
}

// ExteriorAttachmentURL links the 外観写真等 PDF of the first record that has
// one. It is the only part of a certification that shows what the device
// actually is, which for an unannounced product is the whole point.
func (c certificate) ExteriorAttachmentURL(baseURL string) string {
	for _, item := range c.Records {
		if link := attachmentURL(baseURL, item); link != "" {
			return link
		}
	}
	return ""
}

func distinctValues(records []record, pick func(record) string) []string {
	seen := map[string]struct{}{}
	values := make([]string, 0, len(records))

	for _, item := range records {
		value := strings.TrimSpace(narrow(pick(item)))
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}

	return values
}

// groupByNumber collects records into certificates, newest first.
//
// The API is asked for 年月日(降順) so the records arrive newest first already;
// the sort here is what keeps that true when two numbers interleave, and it
// breaks ties on the number so the output of a run does not depend on map
// iteration order.
func groupByNumber(records []record) []certificate {
	order := make([]string, 0, len(records))
	grouped := map[string][]record{}

	for _, item := range records {
		number := strings.TrimSpace(item.Number)
		if number == "" {
			// A record with no number cannot be tracked between runs, so it
			// would be reported as new on every single run. Dropping it loses
			// nothing: the register has never produced one.
			continue
		}
		if _, known := grouped[number]; !known {
			order = append(order, number)
		}
		grouped[number] = append(grouped[number], item)
	}

	certificates := make([]certificate, 0, len(order))
	for _, number := range order {
		certificates = append(certificates, certificate{Number: number, Records: grouped[number]})
	}

	sort.SliceStable(certificates, func(i, j int) bool {
		left, right := certificates[i], certificates[j]
		if left.Date() != right.Date() {
			return left.Date() > right.Date()
		}
		return left.Number < right.Number
	})

	return certificates
}

// changeKind says why a certificate is worth a message.
type changeKind string

const (
	// changeNew is a 証明番号 this target has never seen. The thing being
	// watched for.
	changeNew changeKind = "new"

	// changeAmended is a number already seen that has gained records. The
	// register does this when a 工事設計 is added to an existing certification,
	// or when the certification is amended -- 備考 then carries the history. It
	// is reported, quietly: it says a product changed, not that one appeared.
	changeAmended changeKind = "amended"
)

// change is one certificate and what happened to it.
type change struct {
	Certificate certificate
	Kind        changeKind

	// PreviousRecordCount is how many records this number had the last time it
	// was seen. Only meaningful for changeAmended.
	PreviousRecordCount int
}
