package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

const (
	defaultStateKey       = "giteki-notify/state.json"
	defaultRequestTimeout = 30 * time.Second

	// defaultMaxNotifications bounds how many certifications one run posts
	// individually. A normal day is nought to a handful; the runs that find
	// dozens are the one after a long outage and the deliberate state reset. The
	// rest of them are reported in a single summary message, so nothing is lost
	// and the channel stays readable.
	defaultMaxNotifications = 10

	// defaultUserAgent identifies this function to 総務省's API, which asks for a
	// User-Agent and may refuse requests without one.
	//
	// The shape is not cosmetic. A plain token -- "giteki-notify/1.0", or Go's
	// own default -- is answered with HTTP 403 and an HTML "該当するページが
	// ありません" error page, which looks like a wrong URL rather than a blocked
	// request. A parenthesised comment after a Mozilla/5.0 product token is
	// accepted, so this says who it is inside a shape the filter lets through.
	defaultUserAgent = "Mozilla/5.0 (compatible; giteki-notify/1.0; +https://github.com/tamura09/giteki-notify)"

	// parameterCacheTTL bounds how stale a cached parameter may be. Every run
	// reads the webhook parameter, and each read decrypts a SecureString.
	// Lambda reuses the execution environment between runs, so caching here
	// removes most of those decryptions, and an hour is well inside the time it
	// takes to roll a webhook out anyway.
	parameterCacheTTL = time.Hour
)

// parameterReader is the slice of the SSM API this function uses. Narrowed to an
// interface so the cache can be exercised without an AWS client.
type parameterReader interface {
	GetParameter(context.Context, *ssm.GetParameterInput, ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

// objectStore is the slice of the S3 API this function uses, narrowed for the
// same reason.
type objectStore interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

type cachedParameter struct {
	value  string
	readAt time.Time
}

type app struct {
	parameters parameterReader
	objects    objectStore
	httpClient *http.Client
	now        func() time.Time
	userAgent  string

	// Survives between invocations, because Lambda reuses the execution
	// environment. Guarded because nothing promises that reuse is
	// single-threaded.
	parameterMutex sync.Mutex
	parameterCache map[string]cachedParameter
}

type settings struct {
	targets              []target
	apiBaseURL           string
	webhookParameterName string
	stateBucket          string
	stateKey             string
	// mentionRoleID is optional. Without it the notifications still post; they
	// simply do not ping anyone, which for news that arrives a few times a year
	// means nobody finds out until they look at the channel.
	mentionRoleID    string
	maxNotifications int
	requestTimeout   time.Duration
}

func main() {
	ctx := context.Background()
	awsConfig, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("load AWS config: %v", err)
	}

	lambda.Start((&app{
		parameters: ssm.NewFromConfig(awsConfig),
		objects:    s3.NewFromConfig(awsConfig),
		httpClient: &http.Client{Timeout: defaultRequestTimeout},
		now:        time.Now,
		userAgent:  envOr("USER_AGENT", defaultUserAgent),
	}).handle)
}

func (a *app) handle(ctx context.Context) error {
	current, err := loadSettings()
	if err != nil {
		return err
	}
	a.httpClient.Timeout = current.requestTimeout

	rawWebhook, err := a.parameterString(ctx, current.webhookParameterName)
	if err != nil {
		return fmt.Errorf("read Discord webhook parameter: %w", err)
	}
	webhookURL, err := parseWebhookURL(rawWebhook)
	if err != nil {
		return fmt.Errorf("parse Discord webhook parameter: %w", err)
	}

	state, err := a.loadState(ctx, current.stateBucket, current.stateKey)
	if err != nil {
		return err
	}
	state.prune(current.targets)

	var problems []error
	for _, watched := range current.targets {
		if err := a.check(ctx, webhookURL, current, watched, state); err != nil {
			// One target failing must not stop the others. They are independent
			// searches, and the one that failed keeps its stored set untouched
			// so the next run picks it up where it left off.
			problems = append(problems, fmt.Errorf("target %s: %w", watched.ID, err))
		}
	}

	// Saved even when a target failed, so the targets that did succeed keep
	// their progress.
	if err := a.saveState(ctx, current.stateBucket, current.stateKey, state); err != nil {
		problems = append(problems, err)
	}

	return errors.Join(problems...)
}

// check runs one target's search and posts what is new.
//
// The target's set is only written once the posts have been delivered, so a
// failure to post is retried on the next run rather than silently swallowed.
func (a *app) check(ctx context.Context, webhookURL string, current settings, watched target, state *notifierState) error {
	result, err := a.fetchRecords(ctx, current.apiBaseURL, watched)
	if err != nil {
		return err
	}

	certificates := groupByNumber(result.Records)
	now := a.now().UTC()

	if !state.seen(watched.ID) {
		// The first run records the register as it stands without reporting it.
		// Otherwise adding a target would announce a manufacturer's entire
		// history as news -- which for Ubiquiti is over a hundred messages.
		log.Printf("target %s: first run, recording %d certificates (%d records) without notifying",
			watched.ID, len(certificates), len(result.Records))
		state.record(watched.ID, result, certificates, now)
		return nil
	}

	changes := state.changes(watched.ID, certificates)
	if len(changes) == 0 {
		log.Printf("target %s: %d certificates (%d records), nothing new", watched.ID, len(certificates), len(result.Records))
		state.record(watched.ID, result, certificates, now)
		return nil
	}

	posted, err := a.deliver(ctx, webhookURL, current, watched, changes, result.LastUpdateDate, now)

	// Recorded even when a post failed part way through: everything posted so
	// far must not be posted again, and the certificates that were not reached
	// are held back below rather than left to the next run. Their records are
	// written too -- see the comment on deliver.
	state.record(watched.ID, result, certificates, now)

	log.Printf("target %s: %d certificates (%d records), %d changes, posted %d",
		watched.ID, len(certificates), len(result.Records), len(changes), posted)

	return err
}

// deliver posts each change, oldest first, up to maxNotifications. What is left
// over is reported as one summary message.
//
// A post that fails stops this target for the run. Continuing would post later
// certifications before the one that failed -- and since the whole result set is
// recorded either way, the failed one would never be posted at all. Returning
// the error instead makes the run fail loudly, which is the signal that
// something in the channel is missing.
func (a *app) deliver(ctx context.Context, webhookURL string, current settings, watched target, changes []change, lastUpdateDate string, now time.Time) (int, error) {
	posted := 0

	for index, item := range changes {
		if index >= current.maxNotifications {
			held := changes[index:]
			payload := buildSummaryPayload(watched, held, now, lastUpdateDate)
			if err := a.postWebhook(ctx, webhookURL, payload); err != nil {
				return posted, fmt.Errorf("post summary of %d held changes: %w", len(held), err)
			}
			log.Printf("target %s: summarised %d changes beyond the per-run limit of %d",
				watched.ID, len(held), current.maxNotifications)
			return posted + 1, nil
		}

		payload := buildPayload(watched, item, current.apiBaseURL, current.mentionRoleID, now, lastUpdateDate)
		if err := a.postWebhook(ctx, webhookURL, payload); err != nil {
			return posted, fmt.Errorf("post %s %s: %w", item.Kind, item.Certificate.Number, err)
		}
		posted++
	}

	return posted, nil
}

func loadSettings() (settings, error) {
	targets, err := parseTargets(os.Getenv("TARGETS"))
	if err != nil {
		return settings{}, err
	}

	webhookParameterName, err := requiredEnv("DISCORD_WEBHOOK_PARAMETER_NAME")
	if err != nil {
		return settings{}, err
	}

	stateBucket, err := requiredEnv("STATE_BUCKET")
	if err != nil {
		return settings{}, err
	}

	requestTimeout, err := durationEnv("REQUEST_TIMEOUT", defaultRequestTimeout)
	if err != nil {
		return settings{}, err
	}

	maxNotifications, err := positiveIntEnv("MAX_NOTIFICATIONS", defaultMaxNotifications)
	if err != nil {
		return settings{}, err
	}

	return settings{
		targets:              targets,
		apiBaseURL:           envOr("API_BASE_URL", defaultAPIBaseURL),
		webhookParameterName: webhookParameterName,
		stateBucket:          stateBucket,
		stateKey:             envOr("STATE_KEY", defaultStateKey),
		mentionRoleID:        strings.TrimSpace(os.Getenv("MENTION_ROLE_ID")),
		maxNotifications:     maxNotifications,
		requestTimeout:       requestTimeout,
	}, nil
}

func (a *app) parameterString(ctx context.Context, parameterName string) (string, error) {
	if value, ok := a.cachedParameterValue(parameterName); ok {
		return value, nil
	}

	withDecryption := true
	out, err := a.parameters.GetParameter(ctx, &ssm.GetParameterInput{Name: &parameterName, WithDecryption: &withDecryption})
	if err != nil {
		return "", err
	}
	if out.Parameter == nil || out.Parameter.Value == nil {
		return "", errors.New("parameter has no value")
	}

	a.cacheParameterValue(parameterName, *out.Parameter.Value)
	return *out.Parameter.Value, nil
}

// cachedParameterValue returns the cached value while it is younger than
// parameterCacheTTL. Only successful reads are cached: a failure should be
// retried on the next run rather than remembered for an hour.
func (a *app) cachedParameterValue(parameterName string) (string, bool) {
	a.parameterMutex.Lock()
	defer a.parameterMutex.Unlock()

	cached, ok := a.parameterCache[parameterName]
	if !ok || a.now().Sub(cached.readAt) >= parameterCacheTTL {
		return "", false
	}
	return cached.value, true
}

func (a *app) cacheParameterValue(parameterName, value string) {
	a.parameterMutex.Lock()
	defer a.parameterMutex.Unlock()

	if a.parameterCache == nil {
		a.parameterCache = map[string]cachedParameter{}
	}
	a.parameterCache[parameterName] = cachedParameter{value: value, readAt: a.now()}
}

func requiredEnv(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func envOr(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func durationEnv(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return parsed, nil
}

func positiveIntEnv(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return parsed, nil
}
