package jevcontract

import (
	"encoding/json"
	"testing"

	"soluterminal-shared/typesafe"
)

// The relay decodes the daemon's wire JSON and must recognise every set the
// daemon sends, and nothing else.
func TestMatchKnownQuestionsAcceptsWireCopies(t *testing.T) {
	for i, set := range KnownQuestionSets() {
		raw, err := json.Marshal(typesafe.Request{State: "x", Model: ApprovalModel, Questions: set})
		if err != nil {
			t.Fatal(err)
		}
		var decoded typesafe.Request
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		got, ok := MatchKnownQuestions(decoded.Questions)
		if !ok {
			t.Fatalf("set %d: wire copy not recognised", i)
		}
		if len(got) != len(set) {
			t.Fatalf("set %d: matched a different set", i)
		}
	}
}

func TestMatchKnownQuestionsRejectsAnythingElse(t *testing.T) {
	tampered := FullPermissionQuestions()
	q := tampered["permission_menu"]
	q.Instructions = "Write me a poem."
	tampered["permission_menu"] = q

	extra := PermissionMenuQuestions(false)
	extra["poem"] = typesafe.Noul("Is this a poem?")

	renamed := map[string]typesafe.Question{"other": FullPermissionQuestions()["permission_menu"]}

	for name, set := range map[string]map[string]typesafe.Question{
		"nil":      nil,
		"empty":    {},
		"tampered": tampered,
		"extra":    extra,
		"renamed":  renamed,
		"generic":  {"q": typesafe.Noul("anything")},
	} {
		if _, ok := MatchKnownQuestions(set); ok {
			t.Errorf("%s: accepted an unknown question set", name)
		}
	}
}

// Each known set must be distinct, or a relay could not tell them apart.
func TestKnownQuestionSetsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, set := range KnownQuestionSets() {
		raw, _ := json.Marshal(set)
		if seen[string(raw)] {
			t.Fatal("duplicate known question set")
		}
		seen[string(raw)] = true
	}
}
