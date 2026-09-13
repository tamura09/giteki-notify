package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

var testNow = time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)

func newTestApp() *app {
	return &app{
		httpClient: &http.Client{Timeout: 5 * time.Second},
		now:        func() time.Time { return testNow },
		userAgent:  "giteki-notify-test",
	}
}

// fakeParameters answers GetParameter from a map, so a test never needs AWS.
type fakeParameters struct {
	values map[string]string
	calls  int
}

func (f *fakeParameters) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	f.calls++
	value, ok := f.values[*in.Name]
	if !ok {
		return nil, &ssmtypes.ParameterNotFound{}
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Value: &value}}, nil
}

// fakeObjects is an in-memory stand-in for the state bucket. A missing key comes
// back as NoSuchKey, which is what the real bucket returns to a caller holding
// s3:ListBucket -- the case loadState has to tell apart from a denial.
type fakeObjects struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
}

func newFakeObjects() *fakeObjects {
	return &fakeObjects{objects: map[string][]byte{}}
}

func (f *fakeObjects) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	body, ok := f.objects[*in.Key]
	if !ok {
		return nil, &s3types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(body))}, nil
}

func (f *fakeObjects) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	body, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	f.objects[*in.Key] = body
	f.puts++
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeObjects) put(t *testing.T, key string, state *notifierState) {
	t.Helper()

	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("encode state: %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = raw
}

func (f *fakeObjects) stored(t *testing.T, key string) notifierState {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	raw, ok := f.objects[key]
	if !ok {
		t.Fatalf("no state stored under %s", key)
	}
	var state notifierState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decode stored state: %v", err)
	}
	return state
}

// recordingWebhook stands in for Discord and keeps every payload it was sent.
type recordingWebhook struct {
	server   *httptest.Server
	mu       sync.Mutex
	payloads []webhookPayload
	status   int
}

func newRecordingWebhook() *recordingWebhook {
	hook := &recordingWebhook{status: http.StatusNoContent}
	// TLS, because the function refuses to post to anything but https -- the
	// check that keeps a half-configured parameter from being POSTed at.
	hook.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload webhookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		hook.mu.Lock()
		hook.payloads = append(hook.payloads, payload)
		status := hook.status
		hook.mu.Unlock()

		w.WriteHeader(status)
	}))
	return hook
}

func (h *recordingWebhook) close() { h.server.Close() }

func (h *recordingWebhook) url() string { return h.server.URL }

// client trusts the test server's certificate. Plain http targets work through
// it too, so one client can reach both the API stub and the webhook.
func (h *recordingWebhook) client() *http.Client { return h.server.Client() }

func (h *recordingWebhook) received() []webhookPayload {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]webhookPayload(nil), h.payloads...)
}

func (h *recordingWebhook) setStatus(status int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status = status
}

// stubAPI stands in for the 技適 Web-API. It serves records from a slice, honours
// the SC and DC conditions so the paging loop is exercised for real, and records
// every request it was sent.
type stubAPI struct {
	server *httptest.Server

	mu         sync.Mutex
	records    []record
	requests   []string
	userAgents []string
	// lastUpdateDate is the register's データ更新日.
	lastUpdateDate string
	// perPage is how many records one response carries. The real API takes a
	// code rather than a count for DC, so this stands in for it -- and lets a
	// test make the paging loop run without pretending 1000 records exist.
	perPage int
	// failWith, when set, replaces every answer with the API's own error shape.
	failWith *apiErrorRow
	status   int
}

func newStubAPI(records []record) *stubAPI {
	stub := &stubAPI{records: records, lastUpdateDate: "2026-09-11", status: http.StatusOK, perPage: pageSize}

	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		stub.requests = append(stub.requests, r.URL.String())
		stub.userAgents = append(stub.userAgents, r.Header.Get("User-Agent"))
		failure := stub.failWith
		status := stub.status
		perPage := stub.perPage
		all := append([]record(nil), stub.records...)
		lastUpdate := stub.lastUpdateDate
		stub.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		if failure != nil {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(fmt.Sprintf(
				`{"errs":{"errPost":"ERR","errCount":"1"},"err":[{"errMsg":%q,"errCd":%q}]}`,
				failure.ErrMsg, failure.ErrCd,
			)))
			return
		}

		startCount, _ := strconv.Atoi(r.URL.Query().Get("SC"))
		if startCount < 1 {
			startCount = 1
		}

		if perPage < 1 {
			perPage = pageSize
		}

		page := make([]recordEnvelope, 0, perPage)
		for index := startCount - 1; index < len(all) && len(page) < perPage; index++ {
			page = append(page, recordEnvelope{GitekiInfo: all[index]})
		}

		body := map[string]any{
			"gitekiInformation": map[string]string{
				"totalCount":     strconv.Itoa(len(all)),
				"lastUpdateDate": lastUpdate,
			},
		}
		// A zero-result search omits the giteki key entirely, which is what the
		// live API does -- the behaviour fetchRecords has to survive.
		if len(page) > 0 {
			body["giteki"] = page
		}

		_ = json.NewEncoder(w).Encode(body)
	}))

	return stub
}

func (s *stubAPI) close() { s.server.Close() }

// baseURL is the stub's equivalent of https://www.tele.soumu.go.jp/giteki.
func (s *stubAPI) baseURL() string { return s.server.URL }

func (s *stubAPI) setRecords(records []record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = records
}

// setPerPage shrinks the page so a test can exercise the paging loop.
func (s *stubAPI) setPerPage(perPage int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.perPage = perPage
}

func (s *stubAPI) setFailure(row *apiErrorRow, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failWith = row
	s.status = status
}

func (s *stubAPI) requestedURLs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func (s *stubAPI) seenUserAgents() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.userAgents...)
}

// testRecord builds a record the way the register would: Latin text full-width,
// line breaks inside a field escaped as a literal backslash-n.
func testRecord(number, date, typeName, equipmentKind, elecWave string) record {
	return record{
		TechCode:           "登録証明機関による工事設計認証",
		Number:             number,
		Date:               date,
		Name:               "Ｕｂｉｑｕｉｔｉ　Ｉｎｃ．",
		RadioEquipmentCode: equipmentKind,
		TypeName:           typeName,
		ElecWave:           elecWave,
		SpuriousRules:      "新スプリアス規定",
		FqMaintainFunc:     "無",
		BodySar:            "―",
		OrganName:          "(一社)ＴＡＣ",
		AttachmentFileName: number + "_01_002.pdf",
		AttachmentFileKey:  "020_N_1_250925N020_%E8%AA%8D%E8%A8%BC_68_*****_*****",
		AttachmentCount1:   "1",
		AttachmentCount2:   "1",
	}
}

// fieldValue returns the named field of the payload's first embed.
func fieldValue(t *testing.T, payload webhookPayload, name string) string {
	t.Helper()

	if len(payload.Embeds) == 0 {
		t.Fatalf("payload has no embeds")
	}
	for _, field := range payload.Embeds[0].Fields {
		if field.Name == name {
			return field.Value
		}
	}
	t.Fatalf("payload has no %q field; fields: %s", name, strings.Join(fieldNames(payload), ", "))
	return ""
}

func hasField(payload webhookPayload, name string) bool {
	if len(payload.Embeds) == 0 {
		return false
	}
	for _, field := range payload.Embeds[0].Fields {
		if field.Name == name {
			return true
		}
	}
	return false
}

func fieldNames(payload webhookPayload) []string {
	if len(payload.Embeds) == 0 {
		return nil
	}
	names := make([]string, 0, len(payload.Embeds[0].Fields))
	for _, field := range payload.Embeds[0].Fields {
		names = append(names, field.Name)
	}
	return names
}
