package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const stateVersion = 1

// notifierState is what stops this function reporting the same certification
// every day: every 証明番号 each target has already seen.
type notifierState struct {
	Version int                    `json:"version"`
	Targets map[string]targetState `json:"targets"`
}

type targetState struct {
	// Certificates is keyed by 証明番号. Present means reported (or seeded by a
	// first run); absent means new.
	//
	// Nothing is ever removed. A certification does not disappear from the
	// register, but a search can stop returning one -- a 備考 rewrite that
	// changes the 氏名又は名称, for instance, takes the record out of an
	// applicant-name search. Forgetting it would then report the product as
	// newly certified the next time it came back.
	Certificates map[string]certificateState `json:"certificates,omitempty"`

	// LastUpdateDate is the register's own データ更新日 as of the last run. Not
	// used for any decision -- the certificate set is what decides -- but it is
	// the one field that says whether the register itself is still being
	// updated, which is worth having in the object when a target has been quiet
	// for months.
	LastUpdateDate string `json:"last_update_date,omitempty"`

	LastCheckedAt time.Time `json:"last_checked_at"`
}

type certificateState struct {
	// Date is 年月日, the certification date.
	Date string `json:"date"`

	// RecordCount is how many 工事設計 records this number covered when it was
	// last seen. A number that gains one has been amended.
	RecordCount int `json:"record_count"`

	// TypeName is kept so the stored object is readable by a person. Nothing
	// reads it back.
	TypeName string `json:"type_name,omitempty"`

	FirstSeenAt time.Time `json:"first_seen_at"`
}

func newState() *notifierState {
	return &notifierState{Version: stateVersion, Targets: map[string]targetState{}}
}

// seen reports whether this target has a record for the target at all. A target
// with no record is on its first run, which records without reporting.
func (s *notifierState) seen(targetID string) bool {
	_, ok := s.Targets[targetID]
	return ok
}

// changes returns what is worth reporting about this target's current search
// result, oldest first -- so a run that finds several certifications posts them
// in the order they were granted.
func (s *notifierState) changes(targetID string, certificates []certificate) []change {
	known := s.Targets[targetID].Certificates

	changes := make([]change, 0, 4)
	for _, current := range certificates {
		stored, alreadySeen := known[current.Number]
		switch {
		case !alreadySeen:
			changes = append(changes, change{Certificate: current, Kind: changeNew})
		case len(current.Records) > stored.RecordCount:
			changes = append(changes, change{
				Certificate:         current,
				Kind:                changeAmended,
				PreviousRecordCount: stored.RecordCount,
			})
		}
	}

	// certificates arrives newest first, because that is the order the register
	// returns and the order the paging relies on. Reporting reverses it.
	for left, right := 0, len(changes)-1; left < right; left, right = left+1, right-1 {
		changes[left], changes[right] = changes[right], changes[left]
	}

	return changes
}

// record writes the whole current result set for a target. Called with
// everything the search returned, not only what was reported, so that a
// certification held back by maxNotifications is never reported later as if it
// had just appeared.
func (s *notifierState) record(targetID string, result searchResult, certificates []certificate, at time.Time) {
	stored := s.Targets[targetID]
	if stored.Certificates == nil {
		stored.Certificates = map[string]certificateState{}
	}

	for _, current := range certificates {
		existing, known := stored.Certificates[current.Number]
		firstSeen := at
		if known {
			firstSeen = existing.FirstSeenAt
		}
		stored.Certificates[current.Number] = certificateState{
			Date:        current.Date(),
			RecordCount: len(current.Records),
			TypeName:    current.TypeName(),
			FirstSeenAt: firstSeen,
		}
	}

	if result.LastUpdateDate != "" {
		stored.LastUpdateDate = result.LastUpdateDate
	}
	stored.LastCheckedAt = at
	s.Targets[targetID] = stored
}

// prune drops records for targets that are no longer configured. Without it a
// target removed from TARGETS keeps its set forever, and a target id reused for
// a different search inherits it.
func (s *notifierState) prune(configured []target) {
	keep := make(map[string]struct{}, len(configured))
	for _, current := range configured {
		keep[current.ID] = struct{}{}
	}
	for id := range s.Targets {
		if _, ok := keep[id]; !ok {
			delete(s.Targets, id)
		}
	}
}

func (a *app) loadState(ctx context.Context, bucket, key string) (*notifierState, error) {
	out, err := a.objects.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		// The first ever run has no object. The Lambda's policy grants
		// s3:ListBucket precisely so this arrives as NoSuchKey rather than as
		// AccessDenied, which would be indistinguishable from a real
		// permissions failure and would make every run look like a first one --
		// recording the register's contents and reporting nothing, for ever.
		var noSuchKey *s3types.NoSuchKey
		var notFound *s3types.NotFound
		if errors.As(err, &noSuchKey) || errors.As(err, &notFound) {
			return newState(), nil
		}
		return nil, fmt.Errorf("read state object: %w", err)
	}
	defer out.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(out.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("read state object body: %w", err)
	}

	state := newState()
	if len(bytes.TrimSpace(raw)) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(raw, state); err != nil {
		return nil, fmt.Errorf("decode state object: %w", err)
	}
	if state.Targets == nil {
		state.Targets = map[string]targetState{}
	}
	state.Version = stateVersion

	return state, nil
}

func (a *app) saveState(ctx context.Context, bucket, key string, state *notifierState) error {
	// encoding/json writes map keys in sorted order, so a run that changed
	// nothing writes a byte-identical object and the bucket's version history
	// shows only real changes.
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}

	if _, err := a.objects.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("application/json"),
	}); err != nil {
		return fmt.Errorf("write state object: %w", err)
	}

	return nil
}
