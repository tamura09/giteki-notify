package main

import (
	"strings"
	"testing"
)

func TestParseTargetsReadsTheTerraformShape(t *testing.T) {
	targets, err := parseTargets(`[
	  {"id":"ubiquiti","label":"Ubiquiti","applicant_name":"Ubiquiti"},
	  {"id":"ubiquiti-u7","label":"Ubiquiti U7","applicant_name":"Ubiquiti","type_name":"U7"}
	]`)
	if err != nil {
		t.Fatalf("parseTargets: %v", err)
	}

	if len(targets) != 2 {
		t.Fatalf("parsed %d targets, want 2", len(targets))
	}
	if targets[0].ApplicantName != "Ubiquiti" || targets[0].Label != "Ubiquiti" {
		t.Errorf("first target = %+v", targets[0])
	}
	if targets[1].TypeName != "U7" {
		t.Errorf("second target's type_name = %q, want U7", targets[1].TypeName)
	}
}

func TestParseTargetsRejectsWhatCannotWork(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		given string
		want  string
	}{
		{name: "empty", given: "  ", want: "TARGETS is required"},
		{name: "not JSON", given: "ubiquiti", want: "parse TARGETS"},
		{name: "no targets", given: "[]", want: "holds no targets"},
		{name: "no id", given: `[{"applicant_name":"Ubiquiti"}]`, want: "has no id"},
		{
			name:  "duplicate id",
			given: `[{"id":"ubiquiti","applicant_name":"Ubiquiti"},{"id":"ubiquiti","applicant_name":"Ubiquiti Networks"}]`,
			want:  "repeats id",
		},
		{
			// A target with no condition searches the whole register: a few
			// hundred thousand records, every one of them reported as news.
			name:  "no conditions",
			given: `[{"id":"everything","label":"everything"}]`,
			want:  "neither applicant_name nor type_name",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := parseTargets(testCase.given)
			if err == nil {
				t.Fatalf("parseTargets(%q) succeeded", testCase.given)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("error = %q, want it to mention %q", err, testCase.want)
			}
		})
	}
}

func TestParseTargetsAcceptsATypeNameOnlyTarget(t *testing.T) {
	targets, err := parseTargets(`[{"id":"u7","type_name":"U7-Pro"}]`)
	if err != nil {
		t.Fatalf("parseTargets: %v", err)
	}
	if targets[0].TypeName != "U7-Pro" {
		t.Errorf("target = %+v", targets[0])
	}
}

func TestDisplayLabelFallsBackToSomethingIdentifiable(t *testing.T) {
	if got := (target{ID: "ubiquiti", ApplicantName: "Ubiquiti"}).displayLabel(); got != "Ubiquiti" {
		t.Errorf("displayLabel = %q, want the applicant name", got)
	}
	if got := (target{ID: "u7", TypeName: "U7-Pro"}).displayLabel(); got != "u7" {
		t.Errorf("displayLabel = %q, want the id", got)
	}
	if got := (target{ID: "u7", Label: "Ubiquiti U7"}).displayLabel(); got != "Ubiquiti U7" {
		t.Errorf("displayLabel = %q, want the label", got)
	}
}
