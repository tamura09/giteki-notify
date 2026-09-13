package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// target is one search to run against the 技適 Web-API. Terraform builds these
// as JSON and passes them in TARGETS, so watching another manufacturer is a
// variable change rather than a code change.
type target struct {
	// ID keys this target's record in the state object, so it has to outlive
	// the search conditions below. Changing it throws away everything this
	// target has seen: the next run records the whole current result set as if
	// it were the first run and reports none of it, which is exactly the
	// behaviour that hides a certification granted that same day.
	ID string `json:"id"`

	// Label names the target in Discord. Free text.
	Label string `json:"label"`

	// ApplicantName is the NAM condition: 「技術基準適合証明又は工事設計認証を
	// 受けた者若しくは技術基準適合自己確認の届出業者の氏名又は名称」, matched as
	// a substring by the API.
	//
	// The API normalises full-width to half-width on its side, so "Ubiquiti"
	// finds 「Ｕｂｉｑｕｉｔｉ　Ｉｎｃ．」 as well as the older
	// 「Ｕbiquiti　Ｎetworks，　Ｉnc．」 records. Keeping the query short is
	// deliberate for that reason: a company that renames itself keeps being
	// watched.
	ApplicantName string `json:"applicant_name"`

	// TypeName is the optional TN condition, 「機器の型式又は名称」, also a
	// substring match. Empty means every model.
	TypeName string `json:"type_name,omitempty"`
}

func parseTargets(raw string) ([]target, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("TARGETS is required")
	}

	var targets []target
	if err := json.Unmarshal([]byte(raw), &targets); err != nil {
		return nil, fmt.Errorf("parse TARGETS: %w", err)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("TARGETS holds no targets")
	}

	seen := make(map[string]struct{}, len(targets))
	for index, watched := range targets {
		if strings.TrimSpace(watched.ID) == "" {
			return nil, fmt.Errorf("TARGETS[%d] has no id", index)
		}
		if _, duplicate := seen[watched.ID]; duplicate {
			return nil, fmt.Errorf("TARGETS[%d] repeats id %q", index, watched.ID)
		}
		seen[watched.ID] = struct{}{}

		// A target with no condition at all would search the whole register --
		// a few hundred thousand records -- and report every certification
		// granted in Japan. That is never what was meant, and the failure mode
		// is a run that hammers someone else's API and then floods Discord.
		if strings.TrimSpace(watched.ApplicantName) == "" && strings.TrimSpace(watched.TypeName) == "" {
			return nil, fmt.Errorf("TARGETS[%d] (%s) has neither applicant_name nor type_name", index, watched.ID)
		}
	}

	return targets, nil
}

// displayLabel is what Discord shows for this target. Falls back to the search
// condition so a target configured without a label is still identifiable.
func (t target) displayLabel() string {
	if label := strings.TrimSpace(t.Label); label != "" {
		return label
	}
	if name := strings.TrimSpace(t.ApplicantName); name != "" {
		return name
	}
	return t.ID
}
