package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// defaultAPIBaseURL is 総務省電波利用ポータル's Web-API for 技術基準適合証明等
	// を受けた機器の検索.
	//
	//   https://www.tele.soumu.go.jp/j/sys/equ/tech/webapi/
	defaultAPIBaseURL = "https://www.tele.soumu.go.jp/giteki"

	// outputFormatJSON is the OF condition. 1:CSV, 2:JSON, 3:XML.
	outputFormatJSON = "2"

	// pageSizeCode is the DC condition, which takes a code rather than a count:
	// 1:10, 2:20, 3:30, 4:50, 5:100, 6:500, 7:1000. The largest one, because a
	// manufacturer's whole history is a few hundred records and fetching it in
	// one request is both faster and gentler on the API than paging through it.
	pageSizeCode = "7"
	pageSize     = 1000

	// sortKeyDateDescending is the SK condition 12:年月日(降順), so the newest
	// certifications arrive first and a run that hits maxPages still sees them.
	sortKeyDateDescending = "12"

	// maxPages bounds the paging loop. 1000 records per page, so this is 10000
	// records for one manufacturer -- far beyond anyone's real history, and the
	// only thing standing between a malformed totalCount and an endless loop.
	maxPages = 10

	// maxResponseBytes caps one response body. A full 1000-record page with the
	// long 電波の型式 strings runs around 2 MB.
	maxResponseBytes = 32 << 20

	fetchMaxAttempts = 3

	// attachmentTypeExterior is the AFT condition 1:外観写真等, the attachment
	// worth linking to: it is the one that shows what the device looks like.
	attachmentTypeExterior = "1"
)

// fetchBackoff is a variable so tests do not have to sleep through it.
var fetchBackoff = 2 * time.Second

// record is one 技術基準適合証明等 record, as the 一覧取得 API returns it. Every
// field is a string on the wire, including the counts.
type record struct {
	No                 string `json:"no"`
	TechCode           string `json:"techCode"`
	Number             string `json:"number"`
	Date               string `json:"date"`
	Name               string `json:"name"`
	RadioEquipmentCode string `json:"radioEquipmentCode"`
	TypeName           string `json:"typeName"`
	ElecWave           string `json:"elecWave"`
	SpuriousRules      string `json:"spuriousRules"`
	FqMaintainFunc     string `json:"fqMaintainFunc"`
	BodySar            string `json:"bodySar"`
	Note               string `json:"note"`
	OrganName          string `json:"organName"`
	AttachmentFileName string `json:"attachmentFileName"`
	AttachmentFileKey  string `json:"attachmentFileKey"`
	AttachmentCount1   string `json:"attachmentFileCntForCd1"`
	AttachmentCount2   string `json:"attachmentFileCntForCd2"`
}

// listResponse covers both shapes the API answers with, because the error shape
// replaces the success shape rather than accompanying a non-200 status: a
// malformed condition comes back as HTTP 400 with errs/err, and nothing else in
// the body.
type listResponse struct {
	Giteki      []recordEnvelope `json:"giteki"`
	Information struct {
		TotalCount     string `json:"totalCount"`
		LastUpdateDate string `json:"lastUpdateDate"`
	} `json:"gitekiInformation"`

	// The error header. The specification calls this object "header"; the live
	// API calls it "errs". Both are accepted, because the one that is wrong is
	// whichever one stops matching some day.
	Errs   *errorHeader  `json:"errs"`
	Header *errorHeader  `json:"header"`
	Err    []apiErrorRow `json:"err"`
}

type recordEnvelope struct {
	GitekiInfo record `json:"gitekiInfo"`
}

type errorHeader struct {
	ErrPost  string `json:"errPost"`
	ErrCount string `json:"errCount"`
}

type apiErrorRow struct {
	ErrCd  string `json:"errCd"`
	ErrMsg string `json:"errMsg"`
}

// apiError is what the API reports in its own error shape. Kept as a type so a
// condition this function built wrong is distinguishable from the API being
// down.
type apiError struct {
	StatusCode int
	Rows       []apiErrorRow
}

func (e *apiError) Error() string {
	parts := make([]string, 0, len(e.Rows))
	for _, row := range e.Rows {
		parts = append(parts, fmt.Sprintf("%s %s", row.ErrCd, row.ErrMsg))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("giteki API returned %d with an empty error list", e.StatusCode)
	}
	return fmt.Sprintf("giteki API rejected the request: %s", strings.Join(parts, "; "))
}

// searchResult is one complete search: every record matching the target, newest
// first, together with the register's own data-update date.
type searchResult struct {
	Records        []record
	TotalCount     int
	LastUpdateDate string
}

// fetchRecords runs one target's search, following the API's paging until it has
// every record.
func (a *app) fetchRecords(ctx context.Context, baseURL string, watched target) (searchResult, error) {
	result := searchResult{}

	for page := 0; page < maxPages; page++ {
		// SC (スタートカウント) is 1-based, and the next page starts after what has
		// actually been collected rather than at a multiple of pageSize. DC asks
		// for 1000 records, but nothing promises the API sends that many in one
		// response -- and assuming it did would skip every record between what
		// arrived and the next multiple, silently.
		startCount := len(result.Records) + 1

		body, err := a.get(ctx, listURL(baseURL, watched, startCount))
		if err != nil {
			return searchResult{}, err
		}

		var decoded listResponse
		if err := json.Unmarshal(body, &decoded); err != nil {
			return searchResult{}, fmt.Errorf("decode list response: %w", err)
		}
		if failure := decoded.apiError(0); failure != nil {
			return searchResult{}, failure
		}

		result.LastUpdateDate = decoded.Information.LastUpdateDate
		total, err := strconv.Atoi(strings.TrimSpace(decoded.Information.TotalCount))
		if err != nil {
			return searchResult{}, fmt.Errorf("parse totalCount %q: %w", decoded.Information.TotalCount, err)
		}
		result.TotalCount = total

		for _, envelope := range decoded.Giteki {
			result.Records = append(result.Records, envelope.GitekiInfo)
		}

		// A zero-result search omits the giteki key entirely rather than
		// sending an empty array, and a start count past the end sends an empty
		// one -- so the length of what came back, not the key's presence, is
		// what says there is no next page.
		if len(decoded.Giteki) == 0 || len(result.Records) >= total {
			return result, nil
		}
	}

	return result, fmt.Errorf("gave up after %d pages of %d records", maxPages, pageSize)
}

// apiError returns the error the response carries, or nil when it carries none.
func (r listResponse) apiError(statusCode int) *apiError {
	header := r.Errs
	if header == nil {
		header = r.Header
	}
	if header == nil && len(r.Err) == 0 {
		return nil
	}
	if header != nil && !strings.EqualFold(strings.TrimSpace(header.ErrPost), "ERR") && len(r.Err) == 0 {
		return nil
	}
	return &apiError{StatusCode: statusCode, Rows: r.Err}
}

// listURL builds a 一覧取得 API request for one page of one target's search.
func listURL(baseURL string, watched target, startCount int) string {
	conditions := url.Values{}
	conditions.Set("OF", outputFormatJSON)
	conditions.Set("SC", strconv.Itoa(startCount))
	conditions.Set("DC", pageSizeCode)
	conditions.Set("SK", sortKeyDateDescending)
	if name := strings.TrimSpace(watched.ApplicantName); name != "" {
		conditions.Set("NAM", name)
	}
	if typeName := strings.TrimSpace(watched.TypeName); typeName != "" {
		conditions.Set("TN", typeName)
	}

	return strings.TrimSuffix(baseURL, "/") + "/list?" + conditions.Encode()
}

// attachmentURL is the 添付ファイル(PDF)取得 API URL for a record's exterior
// photographs: one PDF when there is one, a ZIP when there are several.
//
// The key is returned by the list API already percent-encoded in places (the
// 証明/認証 segment arrives as %E8%AA%8D%E8%A8%BC), so it is concatenated rather
// than re-encoded. Encoding it again would escape the percent signs and the key
// would no longer match.
func attachmentURL(baseURL string, item record) string {
	key := strings.TrimSpace(item.AttachmentFileKey)
	if key == "" || attachmentCount(item.AttachmentCount1) == 0 {
		return ""
	}
	return strings.TrimSuffix(baseURL, "/") + "/file?AFK=" + key + "&AFT=" + attachmentTypeExterior
}

func attachmentCount(raw string) int {
	count, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return count
}

func (a *app) get(ctx context.Context, requestURL string) ([]byte, error) {
	var lastErr error

	for attempt := 1; attempt <= fetchMaxAttempts; attempt++ {
		body, retryable, err := a.getOnce(ctx, requestURL)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retryable || attempt == fetchMaxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(fetchBackoff):
		}
	}

	return nil, lastErr
}

// getOnce reports retryable for the failures a second attempt can plausibly
// fix: a transport error or a 5xx. A 4xx is the API telling this function its
// conditions are wrong, and repeating them changes nothing.
func (a *app) getOnce(ctx context.Context, requestURL string) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, false, err
	}
	// The API documentation asks for a User-Agent and warns that requests
	// without one may be refused. What it does not say is that the shape
	// matters: the site answers 403 with an HTML "該当するページがありません"
	// error page -- not a JSON API error -- to a plain token like
	// "giteki-notify/1.0" or Go's own "Go-http-client/2.0". A parenthesised
	// comment after a Mozilla/5.0 product token gets through, which is why the
	// default in main.go looks like a browser while still saying who it is.
	req.Header.Set("User-Agent", a.userAgent)
	req.Header.Set("Accept", "application/json,text/plain;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "ja,en;q=0.9")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, true, fmt.Errorf("fetch giteki API: %w", err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if readErr != nil {
		return nil, true, fmt.Errorf("read giteki API response: %w", readErr)
	}

	if resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf("giteki API returned %d", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// A rejected condition arrives here with the API's own error body, and
		// saying which condition was wrong is far more useful than the status
		// code alone. A 403 from the request filter has an HTML body instead,
		// which does not decode -- so that falls through to the status.
		var decoded listResponse
		if err := json.Unmarshal(body, &decoded); err == nil {
			if failure := decoded.apiError(resp.StatusCode); failure != nil {
				return nil, false, failure
			}
		}
		return nil, false, fmt.Errorf("giteki API returned %d: %s", resp.StatusCode, summarize(body))
	}

	return body, false, nil
}

// summarize is for error messages: enough of an unexpected body to recognise it
// by, on one line.
func summarize(body []byte) string {
	const limit = 200
	collapsed := strings.Join(strings.Fields(string(body)), " ")
	if len([]rune(collapsed)) > limit {
		return string([]rune(collapsed)[:limit]) + "…"
	}
	if collapsed == "" {
		return "(empty body)"
	}
	return collapsed
}
