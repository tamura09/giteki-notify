package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const testAPIBase = "https://www.tele.soumu.go.jp/giteki"

func newCertificateChange() change {
	return change{Certificate: groupByNumber(ucCastPro())[0], Kind: changeNew}
}

func TestBuildPayloadCarriesTheCertificationDetails(t *testing.T) {
	watched := target{ID: "ubiquiti", Label: "Ubiquiti", ApplicantName: "Ubiquiti"}

	payload := buildPayload(watched, newCertificateChange(), testAPIBase, "", testNow, "2026-09-11")

	if len(payload.Embeds) != 1 {
		t.Fatalf("payload has %d embeds, want 1", len(payload.Embeds))
	}
	embedded := payload.Embeds[0]

	if !strings.Contains(embedded.Title, "Ubiquiti") || !strings.Contains(embedded.Title, "UC-Cast-Pro") {
		t.Errorf("title = %q, want it to name the label and the model", embedded.Title)
	}
	if fieldValue(t, payload, "番号") != "020-250266" {
		t.Errorf("番号 = %q, want 020-250266", fieldValue(t, payload, "番号"))
	}
	if fieldValue(t, payload, "年月日") != "2026-08-25" {
		t.Errorf("年月日 = %q, want 2026-08-25", fieldValue(t, payload, "年月日"))
	}
	if got := fieldValue(t, payload, "氏名又は名称"); got != "Ubiquiti Inc." {
		t.Errorf("氏名又は名称 = %q, want the narrowed name", got)
	}
	if got := fieldValue(t, payload, "特定無線設備の種別"); !strings.Contains(got, "第2条第19号の3") {
		t.Errorf("特定無線設備の種別 = %q, want every kind listed", got)
	}
	if got := fieldValue(t, payload, "電波の型式、周波数及び空中線電力"); !strings.Contains(got, "5.18~5.32GHz") {
		t.Errorf("電波の型式 = %q, want the 5 GHz line", got)
	}
	if got := embedded.Footer.Text; !strings.Contains(got, "2026-09-11") {
		t.Errorf("footer = %q, want the register's data update date", got)
	}
	if embedded.Color != colorNew {
		t.Errorf("color = %#x, want %#x", embedded.Color, colorNew)
	}
}

func TestBuildPayloadLinksTheExteriorPhotograph(t *testing.T) {
	watched := target{ID: "ubiquiti", Label: "Ubiquiti", ApplicantName: "Ubiquiti"}

	payload := buildPayload(watched, newCertificateChange(), testAPIBase, "", testNow, "2026-09-11")

	// The 外観写真 PDF is the only part of a certification that shows what the
	// device is, which for an unannounced product is the point of the alert.
	if !strings.Contains(payload.Embeds[0].URL, "/file?AFK=") {
		t.Errorf("embed URL = %q, want the attachment API", payload.Embeds[0].URL)
	}
	if got := fieldValue(t, payload, "リンク"); !strings.Contains(got, "外観写真等") || !strings.Contains(got, searchPageURL) {
		t.Errorf("リンク = %q, want both the photo and the search page", got)
	}
}

func TestBuildPayloadFallsBackToTheSearchPageWithoutAnAttachment(t *testing.T) {
	stripped := ucCastPro()
	for index := range stripped {
		stripped[index].AttachmentFileKey = ""
		stripped[index].AttachmentCount1 = ""
	}
	item := change{Certificate: groupByNumber(stripped)[0], Kind: changeNew}

	payload := buildPayload(target{ID: "ubiquiti", Label: "Ubiquiti"}, item, testAPIBase, "", testNow, "")

	if payload.Embeds[0].URL != "" {
		t.Errorf("embed URL = %q, want none when there is no attachment", payload.Embeds[0].URL)
	}
	if got := fieldValue(t, payload, "リンク"); !strings.Contains(got, searchPageURL) {
		t.Errorf("リンク = %q, want the search page", got)
	}
}

func TestBuildPayloadMentionsTheRoleOnlyForANewCertification(t *testing.T) {
	watched := target{ID: "ubiquiti", Label: "Ubiquiti"}

	newPayload := buildPayload(watched, newCertificateChange(), testAPIBase, "123456789", testNow, "")
	if newPayload.Content != "<@&123456789>" {
		t.Errorf("content = %q, want the role mention", newPayload.Content)
	}
	if len(newPayload.AllowedMentions.Roles) != 1 {
		t.Errorf("allowed_mentions.roles = %v, want the role allowed through by id", newPayload.AllowedMentions.Roles)
	}

	amended := change{Certificate: groupByNumber(ucCastPro())[0], Kind: changeAmended, PreviousRecordCount: 2}
	amendedPayload := buildPayload(watched, amended, testAPIBase, "123456789", testNow, "")
	if amendedPayload.Content != "" {
		t.Errorf("content = %q, want no mention for an amendment", amendedPayload.Content)
	}
	if len(amendedPayload.AllowedMentions.Roles) != 0 {
		t.Errorf("allowed_mentions.roles = %v, want none", amendedPayload.AllowedMentions.Roles)
	}
}

func TestBuildPayloadAlwaysSendsAnEmptyParseList(t *testing.T) {
	payload := buildPayload(target{ID: "ubiquiti"}, newCertificateChange(), testAPIBase, "", testNow, "")

	// An empty parse list is what stops a 備考 line or a model name containing
	// @everyone from pinging the server, and it has to be present rather than
	// omitted -- which is why the field has no omitempty.
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if !strings.Contains(string(encoded), `"allowed_mentions":{"parse":[]}`) {
		t.Errorf("encoded payload = %s, want an explicit empty parse list", encoded)
	}
}

func TestBuildPayloadShowsTheRecordCountOnAnAmendment(t *testing.T) {
	amended := change{Certificate: groupByNumber(ucCastPro())[0], Kind: changeAmended, PreviousRecordCount: 2}

	payload := buildPayload(target{ID: "ubiquiti", Label: "Ubiquiti"}, amended, testAPIBase, "", testNow, "")

	if !strings.Contains(payload.Embeds[0].Title, "更新") {
		t.Errorf("title = %q, want it to say the certification was updated", payload.Embeds[0].Title)
	}
	if got := fieldValue(t, payload, "工事設計"); got != "2 件 → 3 件" {
		t.Errorf("工事設計 = %q, want 2 件 → 3 件", got)
	}
}

func TestBuildPayloadOmitsFieldsWithNothingInThem(t *testing.T) {
	bare := []record{{Number: "020-250266", Date: "2026-08-25"}}
	item := change{Certificate: groupByNumber(bare)[0], Kind: changeNew}

	payload := buildPayload(target{ID: "ubiquiti"}, item, testAPIBase, "", testNow, "")

	for _, name := range []string{"氏名又は名称", "特定無線設備の種別", "備考", "認証機関"} {
		if hasField(payload, name) {
			t.Errorf("payload carries an empty %s field", name)
		}
	}
	// Discord rejects a field with an empty value outright, so the ones that do
	// have a value still have to be there.
	if fieldValue(t, payload, "番号") != "020-250266" {
		t.Error("番号 went missing")
	}
}

func TestBuildPayloadTruncatesAnOversizedField(t *testing.T) {
	long := make([]record, 0, 40)
	for index := 0; index < 40; index++ {
		long = append(long, testRecord("020-250266", "2026-08-25", "Ｕ７",
			"第２条第１９号に規定する特定無線設備",
			strings.Repeat("Ｇ１Ｄ　2412～2472ＭＨz(５ＭＨz間隔13波)　", 3)+string(rune('Ａ'+index%26)),
		))
	}
	item := change{Certificate: groupByNumber(long)[0], Kind: changeNew}

	payload := buildPayload(target{ID: "ubiquiti"}, item, testAPIBase, "", testNow, "")

	value := fieldValue(t, payload, "電波の型式、周波数及び空中線電力")
	if len([]rune(value)) > discordEmbedFieldValueLimit {
		t.Errorf("field is %d runes, over Discord's %d limit", len([]rune(value)), discordEmbedFieldValueLimit)
	}
	if !strings.HasSuffix(value, "…") {
		t.Errorf("truncated field = %q, want it to end in an ellipsis", value[len(value)-10:])
	}
}

func TestBuildSummaryPayloadListsWhatWasHeldBack(t *testing.T) {
	held := []change{
		{Certificate: groupByNumber(ucCastPro())[0], Kind: changeNew},
		{Certificate: groupByNumber([]record{testRecord("201-250579", "2026-08-14", "Ｕ７－Ｐｒｏ－ＸＧＳ", "第２条第１９号に規定する特定無線設備", "Ｇ１Ｄ")})[0], Kind: changeNew},
	}

	payload := buildSummaryPayload(target{ID: "ubiquiti", Label: "Ubiquiti"}, held, testNow, "2026-09-11")

	description := payload.Embeds[0].Description
	for _, want := range []string{"020-250266", "UC-Cast-Pro", "201-250579", "U7-Pro-XGS"} {
		if !strings.Contains(description, want) {
			t.Errorf("summary does not mention %s: %s", want, description)
		}
	}
	if !strings.Contains(payload.Embeds[0].Title, "2 件") {
		t.Errorf("title = %q, want the count", payload.Embeds[0].Title)
	}
	if payload.Content != "" {
		t.Errorf("content = %q, want a summary not to ping", payload.Content)
	}
}
