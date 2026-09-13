package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

const testWebhookParameter = "/giteki-notify/discord-webhook-url"

// testEnvironment wires a run up against the stub API, the fake bucket and the
// recording webhook.
type testEnvironment struct {
	app     *app
	api     *stubAPI
	hook    *recordingWebhook
	objects *fakeObjects
}

func newTestEnvironment(t *testing.T, records []record) *testEnvironment {
	t.Helper()

	api := newStubAPI(records)
	t.Cleanup(api.close)

	hook := newRecordingWebhook()
	t.Cleanup(hook.close)

	objects := newFakeObjects()

	client := newTestApp()
	client.httpClient = hook.client()
	client.objects = objects
	client.parameters = &fakeParameters{values: map[string]string{testWebhookParameter: hook.url()}}

	t.Setenv("TARGETS", `[{"id":"ubiquiti","label":"Ubiquiti","applicant_name":"Ubiquiti"}]`)
	t.Setenv("DISCORD_WEBHOOK_PARAMETER_NAME", testWebhookParameter)
	t.Setenv("STATE_BUCKET", "giteki-notify-state")
	t.Setenv("API_BASE_URL", api.baseURL())

	return &testEnvironment{app: client, api: api, hook: hook, objects: objects}
}

func TestHandleFirstRunRecordsWithoutNotifying(t *testing.T) {
	env := newTestEnvironment(t, ucCastPro())

	if err := env.app.handle(context.Background()); err != nil {
		t.Fatalf("handle: %v", err)
	}

	// Otherwise adding a target would announce a manufacturer's entire history.
	if posts := env.hook.received(); len(posts) != 0 {
		t.Fatalf("first run posted %d messages, want none", len(posts))
	}

	stored := env.objects.stored(t, defaultStateKey)
	if _, ok := stored.Targets["ubiquiti"].Certificates["020-250266"]; !ok {
		t.Errorf("first run did not record 020-250266: %+v", stored)
	}
}

func TestHandleReportsACertificationThatAppearsAfterTheFirstRun(t *testing.T) {
	env := newTestEnvironment(t, ucCastPro())

	if err := env.app.handle(context.Background()); err != nil {
		t.Fatalf("seeding run: %v", err)
	}

	env.api.setRecords(append(
		[]record{testRecord("020-250999", "2026-09-12", "ＵＤＭ－Ｐｒｏ－Ｍａｘ", "第２条第１９号に規定する特定無線設備", "Ｇ１Ｄ　2412ＭＨz")},
		ucCastPro()...,
	))

	if err := env.app.handle(context.Background()); err != nil {
		t.Fatalf("second run: %v", err)
	}

	posts := env.hook.received()
	if len(posts) != 1 {
		t.Fatalf("posted %d messages, want 1: %+v", len(posts), posts)
	}
	if !strings.Contains(posts[0].Embeds[0].Title, "UDM-Pro-Max") {
		t.Errorf("title = %q, want the new model", posts[0].Embeds[0].Title)
	}
	if fieldValue(t, posts[0], "番号") != "020-250999" {
		t.Errorf("番号 = %q, want 020-250999", fieldValue(t, posts[0], "番号"))
	}

	// And it is not reported again on the next run.
	if err := env.app.handle(context.Background()); err != nil {
		t.Fatalf("third run: %v", err)
	}
	if posts := env.hook.received(); len(posts) != 1 {
		t.Fatalf("posted %d messages in total, want 1", len(posts))
	}
}

func TestHandleSummarisesWhatIsOverThePerRunLimit(t *testing.T) {
	env := newTestEnvironment(t, ucCastPro())
	t.Setenv("MAX_NOTIFICATIONS", "2")

	if err := env.app.handle(context.Background()); err != nil {
		t.Fatalf("seeding run: %v", err)
	}

	grown := ucCastPro()
	for index := 0; index < 5; index++ {
		number := fmt.Sprintf("020-2509%02d", index)
		grown = append(grown, testRecord(number, "2026-09-"+strconv.Itoa(10+index), "Ｕ７", "第２条第１９号に規定する特定無線設備", "Ｇ１Ｄ"))
	}
	env.api.setRecords(grown)

	if err := env.app.handle(context.Background()); err != nil {
		t.Fatalf("second run: %v", err)
	}

	posts := env.hook.received()
	// Two individual posts plus one summary covering the remaining three.
	if len(posts) != 3 {
		t.Fatalf("posted %d messages, want 3: %+v", len(posts), fieldNames(posts[0]))
	}
	if !strings.Contains(posts[2].Embeds[0].Title, "3 件") {
		t.Errorf("summary title = %q, want it to count the three held back", posts[2].Embeds[0].Title)
	}

	// Everything was recorded, held back or not: a certification kept out of
	// this run's messages must not be reported later as if it had just appeared.
	stored := env.objects.stored(t, defaultStateKey)
	if got := len(stored.Targets["ubiquiti"].Certificates); got != 6 {
		t.Errorf("recorded %d certificates, want 6", got)
	}
	if err := env.app.handle(context.Background()); err != nil {
		t.Fatalf("third run: %v", err)
	}
	if posts := env.hook.received(); len(posts) != 3 {
		t.Fatalf("posted %d messages in total, want 3", len(posts))
	}
}

func TestHandleFailsLoudlyWhenDiscordRejectsAPost(t *testing.T) {
	env := newTestEnvironment(t, ucCastPro())

	if err := env.app.handle(context.Background()); err != nil {
		t.Fatalf("seeding run: %v", err)
	}

	env.api.setRecords(append(
		[]record{testRecord("020-250999", "2026-09-12", "ＵＤＭ－Ｐｒｏ－Ｍａｘ", "第２条第１９号に規定する特定無線設備", "Ｇ１Ｄ")},
		ucCastPro()...,
	))
	env.hook.setStatus(http.StatusForbidden)

	err := env.app.handle(context.Background())
	if err == nil {
		t.Fatal("handle succeeded although Discord rejected the post")
	}
	if !strings.Contains(err.Error(), "020-250999") {
		t.Errorf("error = %q, want it to name the certification that was not posted", err)
	}
}

func TestHandleKeepsGoingWhenOneTargetFails(t *testing.T) {
	env := newTestEnvironment(t, ucCastPro())
	t.Setenv("TARGETS", `[
	  {"id":"ubiquiti","label":"Ubiquiti","applicant_name":"Ubiquiti"},
	  {"id":"broken","label":"broken","applicant_name":"Ubiquiti"}
	]`)

	// The stub fails every request, so both targets fail -- but the run still
	// reaches the save, and the error names both.
	env.api.setFailure(&apiErrorRow{ErrCd: "ER00009", ErrMsg: "番号(NUM)を正しく設定して下さい。"}, http.StatusBadRequest)

	err := env.app.handle(context.Background())
	if err == nil {
		t.Fatal("handle succeeded although every search failed")
	}
	for _, want := range []string{"ubiquiti", "broken"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name target %s", err, want)
		}
	}
	if env.objects.puts == 0 {
		t.Error("state was never written; a failing target must not lose the others' progress")
	}
}

func TestHandleRefusesAPlaceholderWebhookParameter(t *testing.T) {
	env := newTestEnvironment(t, ucCastPro())
	env.app.parameters = &fakeParameters{values: map[string]string{testWebhookParameter: "MANAGED_OUTSIDE_TERRAFORM"}}

	// Terraform creates the parameter with a placeholder and the real value is
	// put in by hand afterwards. Between those two the function must fail rather
	// than POST to a non-URL.
	err := env.app.handle(context.Background())
	if err == nil {
		t.Fatal("handle succeeded on a placeholder webhook parameter")
	}
	if !strings.Contains(err.Error(), "absolute https URL") {
		t.Errorf("error = %q, want it to explain the parameter is not a URL", err)
	}
}

func TestHandleRecordsATargetWhoseSearchMatchesNothing(t *testing.T) {
	env := newTestEnvironment(t, nil)

	if err := env.app.handle(context.Background()); err != nil {
		t.Fatalf("handle: %v", err)
	}

	// A manufacturer with no certifications yet still gets a record, so its
	// first one is reported rather than seeding the target.
	stored := env.objects.stored(t, defaultStateKey)
	if _, ok := stored.Targets["ubiquiti"]; !ok {
		t.Fatalf("target was not recorded: %+v", stored)
	}

	env.api.setRecords(ucCastPro())
	if err := env.app.handle(context.Background()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if posts := env.hook.received(); len(posts) != 1 {
		t.Fatalf("posted %d messages, want 1", len(posts))
	}
}

func TestHandleDropsAStateEntryForARemovedTarget(t *testing.T) {
	env := newTestEnvironment(t, ucCastPro())

	retired := newState()
	retired.record("retired", searchResult{}, groupByNumber(ucCastPro()), testNow)
	env.objects.put(t, defaultStateKey, retired)

	if err := env.app.handle(context.Background()); err != nil {
		t.Fatalf("handle: %v", err)
	}

	stored := env.objects.stored(t, defaultStateKey)
	if _, ok := stored.Targets["retired"]; ok {
		t.Error("a target no longer in TARGETS kept its record")
	}
}

func TestParameterValueIsCachedBetweenRuns(t *testing.T) {
	env := newTestEnvironment(t, ucCastPro())
	parameters := env.app.parameters.(*fakeParameters)

	for run := 0; run < 3; run++ {
		if err := env.app.handle(context.Background()); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
	}

	// Every read decrypts a SecureString, and Lambda reuses the execution
	// environment between runs -- so reading once per hour rather than once per
	// run is the difference in the KMS bill.
	if parameters.calls != 1 {
		t.Errorf("read the parameter %d times across three runs, want 1", parameters.calls)
	}
}

func TestLoadSettingsRequiresItsEnvironment(t *testing.T) {
	t.Setenv("TARGETS", `[{"id":"ubiquiti","applicant_name":"Ubiquiti"}]`)
	t.Setenv("DISCORD_WEBHOOK_PARAMETER_NAME", "")
	t.Setenv("STATE_BUCKET", "bucket")

	if _, err := loadSettings(); err == nil || !strings.Contains(err.Error(), "DISCORD_WEBHOOK_PARAMETER_NAME") {
		t.Fatalf("error = %v, want it to name the missing variable", err)
	}

	t.Setenv("DISCORD_WEBHOOK_PARAMETER_NAME", testWebhookParameter)
	t.Setenv("STATE_BUCKET", "")
	if _, err := loadSettings(); err == nil || !strings.Contains(err.Error(), "STATE_BUCKET") {
		t.Fatalf("error = %v, want it to name the missing bucket", err)
	}
}

func TestLoadSettingsDefaultsAndOverrides(t *testing.T) {
	t.Setenv("TARGETS", `[{"id":"ubiquiti","applicant_name":"Ubiquiti"}]`)
	t.Setenv("DISCORD_WEBHOOK_PARAMETER_NAME", testWebhookParameter)
	t.Setenv("STATE_BUCKET", "bucket")

	current, err := loadSettings()
	if err != nil {
		t.Fatalf("loadSettings: %v", err)
	}
	if current.apiBaseURL != defaultAPIBaseURL {
		t.Errorf("apiBaseURL = %q, want the default", current.apiBaseURL)
	}
	if current.stateKey != defaultStateKey {
		t.Errorf("stateKey = %q, want the default", current.stateKey)
	}
	if current.maxNotifications != defaultMaxNotifications {
		t.Errorf("maxNotifications = %d, want the default", current.maxNotifications)
	}

	t.Setenv("MAX_NOTIFICATIONS", "0")
	if _, err := loadSettings(); err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("error = %v, want a zero limit rejected: it would silence the function", err)
	}

	t.Setenv("MAX_NOTIFICATIONS", "3")
	t.Setenv("REQUEST_TIMEOUT", "45s")
	current, err = loadSettings()
	if err != nil {
		t.Fatalf("loadSettings: %v", err)
	}
	if current.maxNotifications != 3 || current.requestTimeout.String() != "45s" {
		t.Errorf("settings = %+v, want the overrides applied", current)
	}
}
