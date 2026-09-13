package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestListURLCarriesEveryCondition(t *testing.T) {
	built := listURL("https://example.test/giteki/", target{
		ID:            "ubiquiti",
		ApplicantName: "Ubiquiti",
		TypeName:      "U7",
	}, 1001)

	parsed, err := url.Parse(built)
	if err != nil {
		t.Fatalf("parse built URL: %v", err)
	}
	if parsed.Path != "/giteki/list" {
		t.Fatalf("path = %q, want /giteki/list (the trailing slash on the base URL must not double up)", parsed.Path)
	}

	conditions := parsed.Query()
	for name, want := range map[string]string{
		"OF":  outputFormatJSON,
		"SC":  "1001",
		"DC":  pageSizeCode,
		"SK":  sortKeyDateDescending,
		"NAM": "Ubiquiti",
		"TN":  "U7",
	} {
		if got := conditions.Get(name); got != want {
			t.Errorf("condition %s = %q, want %q", name, got, want)
		}
	}
}

func TestListURLOmitsEmptyConditions(t *testing.T) {
	built := listURL("https://example.test/giteki", target{ID: "ubiquiti", ApplicantName: "Ubiquiti"}, 1)

	if strings.Contains(built, "TN=") {
		t.Errorf("built URL carries an empty TN condition: %s", built)
	}
}

func TestFetchRecordsPagesUntilTotalIsReached(t *testing.T) {
	records := make([]record, 0, 25)
	for index := 0; index < 25; index++ {
		number := "020-2502" + strconv.Itoa(index)
		records = append(records, testRecord(number, "2026-09-01", "Ｕ７", "第２条第１９号に規定する特定無線設備", "Ｇ１Ｄ"))
	}

	stub := newStubAPI(records)
	defer stub.close()
	// Ten records per response, so collecting 25 takes three requests. The real
	// API serves 1000 at a time, which a manufacturer's whole history fits into
	// -- this is the loop that covers the one that does not.
	stub.setPerPage(10)

	client := newTestApp()
	result, err := client.fetchRecords(context.Background(), stub.baseURL(), target{ID: "ubiquiti", ApplicantName: "Ubiquiti"})
	if err != nil {
		t.Fatalf("fetchRecords: %v", err)
	}

	if len(result.Records) != 25 {
		t.Fatalf("collected %d records, want 25", len(result.Records))
	}
	if result.TotalCount != 25 {
		t.Errorf("TotalCount = %d, want 25", result.TotalCount)
	}
	if result.LastUpdateDate != "2026-09-11" {
		t.Errorf("LastUpdateDate = %q, want 2026-09-11", result.LastUpdateDate)
	}

	requested := stub.requestedURLs()
	if len(requested) != 3 {
		t.Fatalf("made %d requests, want 3: %v", len(requested), requested)
	}
	for index, want := range []string{"SC=1", "SC=11", "SC=21"} {
		if !strings.Contains(requested[index], want) {
			t.Errorf("request %d = %s, want it to carry %s", index, requested[index], want)
		}
	}
}

func TestFetchRecordsSendsTheConfiguredUserAgent(t *testing.T) {
	stub := newStubAPI(nil)
	defer stub.close()

	client := newTestApp()
	if _, err := client.fetchRecords(context.Background(), stub.baseURL(), target{ID: "ubiquiti", ApplicantName: "Ubiquiti"}); err != nil {
		t.Fatalf("fetchRecords: %v", err)
	}

	// The API answers 403 with an HTML error page to a User-Agent it does not
	// like, so sending one is not optional.
	for _, agent := range stub.seenUserAgents() {
		if agent != "giteki-notify-test" {
			t.Fatalf("User-Agent = %q, want giteki-notify-test", agent)
		}
	}
}

func TestFetchRecordsAcceptsAZeroResultSearch(t *testing.T) {
	stub := newStubAPI(nil)
	defer stub.close()

	client := newTestApp()
	result, err := client.fetchRecords(context.Background(), stub.baseURL(), target{ID: "nobody", ApplicantName: "ZZZNOSUCHVENDOR"})
	if err != nil {
		// The live API omits the giteki key entirely for a search that matches
		// nothing, which must not read as a malformed response.
		t.Fatalf("fetchRecords on an empty result: %v", err)
	}
	if len(result.Records) != 0 || result.TotalCount != 0 {
		t.Fatalf("result = %+v, want no records", result)
	}
}

func TestFetchRecordsReportsTheAPIsOwnError(t *testing.T) {
	stub := newStubAPI(nil)
	defer stub.close()
	stub.setFailure(&apiErrorRow{ErrCd: "ER00014", ErrMsg: "年月日（始）（DS）に正しい日付（YYYYMMDD）を指定して下さい。"}, http.StatusBadRequest)

	client := newTestApp()
	_, err := client.fetchRecords(context.Background(), stub.baseURL(), target{ID: "ubiquiti", ApplicantName: "Ubiquiti"})
	if err == nil {
		t.Fatal("fetchRecords succeeded on an API error")
	}

	var failure *apiError
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v (%T), want an *apiError", err, err)
	}
	if !strings.Contains(failure.Error(), "ER00014") {
		t.Errorf("error %q does not name the error code", failure.Error())
	}
}

func TestFetchRecordsDoesNotRetryARejectedCondition(t *testing.T) {
	stub := newStubAPI(nil)
	defer stub.close()
	stub.setFailure(&apiErrorRow{ErrCd: "ER00009", ErrMsg: "番号(NUM)を正しく設定して下さい。"}, http.StatusBadRequest)

	client := newTestApp()
	if _, err := client.fetchRecords(context.Background(), stub.baseURL(), target{ID: "ubiquiti", ApplicantName: "Ubiquiti"}); err == nil {
		t.Fatal("fetchRecords succeeded on an API error")
	}

	// Repeating a condition the API already rejected changes nothing, so the
	// retry loop must not.
	if requests := stub.requestedURLs(); len(requests) != 1 {
		t.Fatalf("made %d requests, want 1: %v", len(requests), requests)
	}
}

func TestAttachmentURLKeepsTheKeyAsGiven(t *testing.T) {
	item := testRecord("020-250266", "2026-09-01", "Ｕ７", "第２条第１９号に規定する特定無線設備", "Ｇ１Ｄ")

	built := attachmentURL("https://www.tele.soumu.go.jp/giteki", item)

	// The key arrives part-encoded from the list API. Encoding it again would
	// escape the percent signs and the key would stop matching.
	if !strings.Contains(built, "AFK="+item.AttachmentFileKey) {
		t.Errorf("built URL = %s, want it to carry the key verbatim", built)
	}
	if !strings.HasSuffix(built, "&AFT="+attachmentTypeExterior) {
		t.Errorf("built URL = %s, want it to ask for 外観写真等", built)
	}
}

func TestAttachmentURLIsEmptyWithoutAnExteriorPhoto(t *testing.T) {
	item := testRecord("020-250266", "2026-09-01", "Ｕ７", "第２条第１９号に規定する特定無線設備", "Ｇ１Ｄ")
	item.AttachmentCount1 = ""

	if built := attachmentURL("https://www.tele.soumu.go.jp/giteki", item); built != "" {
		t.Errorf("built URL = %s, want none: a record with no 外観写真 has nothing to link to", built)
	}

	item = testRecord("020-250266", "2026-09-01", "Ｕ７", "第２条第１９号に規定する特定無線設備", "Ｇ１Ｄ")
	item.AttachmentFileKey = ""
	if built := attachmentURL("https://www.tele.soumu.go.jp/giteki", item); built != "" {
		t.Errorf("built URL = %s, want none when there is no key", built)
	}
}
