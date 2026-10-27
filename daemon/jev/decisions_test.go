package jev

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"soluterminal-shared/typesafe"
)

func TestShadowModesKeepLabelsOutOfRequest(t *testing.T) {
	for _, mode := range []string{"failure", "intent", "review", "board", "risk", "selection"} {
		c := ShadowCase{ID: "case", Mode: mode, State: json.RawMessage(`{"evidence":"redacted"}`), Expected: "unknown", Candidates: map[string]string{"a": "candidate"}}
		req, err := ShadowRequest(c, "jev-pinned")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(req)
		if strings.Contains(string(b), `"expected"`) || len(req.Questions) != 1 {
			t.Fatal("label leak")
		}
	}
}
func TestShadowAbstainsAndRejectsMalformedAnswers(t *testing.T) {
	c := ShadowCase{ID: "x", Mode: "selection", State: json.RawMessage(`{}`), Candidates: map[string]string{"a": "candidate"}, Expected: "a"}
	conf := 0.4
	r := &typesafe.Response{Model: "jev-pinned", Answers: map[string]typesafe.Answer{"decision": {Type: "choice", Choice: "a", Confidence: &conf, Probabilities: map[string]float64{"a": 0.6, "unknown": 0.4}}}}
	d, err := InterpretShadow(c, r, 0.8)
	if err != nil || !d.Abstained || d.Suggested != "unknown" || d.Correct != nil {
		t.Fatalf("decision=%+v err=%v", d, err)
	}
	conf = 0.9
	d, err = InterpretShadow(c, r, 0.8)
	if err != nil || d.Correct == nil || !*d.Correct {
		t.Fatalf("decision=%+v err=%v", d, err)
	}
	for _, bad := range []float64{-1, 2, math.NaN(), math.Inf(1)} {
		if _, err := InterpretShadow(c, r, bad); err == nil {
			t.Fatal("invalid threshold accepted")
		}
	}
	a := r.Answers["decision"]
	a.Choice = "invented"
	r.Answers["decision"] = a
	if _, err := InterpretShadow(c, r, 0.8); err == nil {
		t.Fatal("invented choice accepted")
	}
	a.Choice = "a"
	a.Probabilities = map[string]float64{"a": 1}
	r.Answers["decision"] = a
	if _, err := InterpretShadow(c, r, 0.8); err == nil {
		t.Fatal("missing distribution accepted")
	}
}
func TestShadowRejectsOversizeAndUnknownModes(t *testing.T) {
	for _, c := range []ShadowCase{
		{ID: "x", Mode: "failure", State: json.RawMessage(`"` + strings.Repeat("a", MaxShadowStateBytes) + `"`)},
		{ID: "x", Mode: "approve", State: json.RawMessage(`{}`)},
		{ID: "x", Mode: "selection", State: json.RawMessage(`{}`)},
		{ID: "x", Mode: "failure", State: json.RawMessage(`null`)},
	} {
		if _, err := ShadowRequest(c, "pinned"); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
}

func validBrowserDecisionInput(kind string) BrowserDecisionInput {
	in := BrowserDecisionInput{Kind: kind, Intent: "submit the form", State: json.RawMessage(`{"settled":true}`)}
	if kind == DecisionTargetRank {
		in.Candidates = []BrowserDecisionCandidate{{ID: "submit", Role: "button", Name: "Submit", Context: "checkout form"}}
	}
	return in
}

func TestBrowserDecisionValidationBounds(t *testing.T) {
	fortyOne := make([]BrowserDecisionCandidate, 41)
	for i := range fortyOne {
		fortyOne[i].ID = string(rune('a' + i))
	}
	cases := []struct {
		name string
		in   BrowserDecisionInput
	}{
		{"unknown kind", BrowserDecisionInput{Kind: "click", Intent: "x", State: json.RawMessage(`{}`)}},
		{"blank intent", BrowserDecisionInput{Kind: DecisionActionOutcome, Intent: "  ", State: json.RawMessage(`{}`)}},
		{"intent unicode bound", BrowserDecisionInput{Kind: DecisionActionOutcome, Intent: strings.Repeat("界", 501), State: json.RawMessage(`{}`)}},
		{"missing state", BrowserDecisionInput{Kind: DecisionActionOutcome, Intent: "x"}},
		{"null state", BrowserDecisionInput{Kind: DecisionActionOutcome, Intent: "x", State: json.RawMessage(`null`)}},
		{"array state", BrowserDecisionInput{Kind: DecisionActionOutcome, Intent: "x", State: json.RawMessage(`[]`)}},
		{"oversize state", BrowserDecisionInput{Kind: DecisionActionOutcome, Intent: "x", State: json.RawMessage(`{"body":"` + strings.Repeat("x", MaxDecisionStateTokens*4) + `"}`)}},
		{"nested secret", BrowserDecisionInput{Kind: DecisionActionOutcome, Intent: "x", State: json.RawMessage(`{"rows":[{"password":"x"}]}`)}},
		{"camel secret", BrowserDecisionInput{Kind: DecisionActionOutcome, Intent: "x", State: json.RawMessage(`{"payment":{"creditCardLastFour":"1234"}}`)}},
		{"acronym camel secret", BrowserDecisionInput{Kind: DecisionActionOutcome, Intent: "x", State: json.RawMessage(`{"form":{"myPASSWORDValue":"x"}}`)}},
		{"too many candidates", BrowserDecisionInput{Kind: DecisionTargetRank, Intent: "x", State: json.RawMessage(`{}`), Candidates: fortyOne}},
		{"duplicate id", BrowserDecisionInput{Kind: DecisionTargetRank, Intent: "x", State: json.RawMessage(`{}`), Candidates: []BrowserDecisionCandidate{{ID: "a"}, {ID: "a"}}}},
		{"reserved id", BrowserDecisionInput{Kind: DecisionTargetRank, Intent: "x", State: json.RawMessage(`{}`), Candidates: []BrowserDecisionCandidate{{ID: "none"}}}},
		{"control id", BrowserDecisionInput{Kind: DecisionTargetRank, Intent: "x", State: json.RawMessage(`{}`), Candidates: []BrowserDecisionCandidate{{ID: "bad\n"}}}},
		{"id unicode bound", BrowserDecisionInput{Kind: DecisionTargetRank, Intent: "x", State: json.RawMessage(`{}`), Candidates: []BrowserDecisionCandidate{{ID: strings.Repeat("界", 201)}}}},
		{"role unicode bound", BrowserDecisionInput{Kind: DecisionTargetRank, Intent: "x", State: json.RawMessage(`{}`), Candidates: []BrowserDecisionCandidate{{ID: "a", Role: strings.Repeat("界", 101)}}}},
		{"name unicode bound", BrowserDecisionInput{Kind: DecisionTargetRank, Intent: "x", State: json.RawMessage(`{}`), Candidates: []BrowserDecisionCandidate{{ID: "a", Name: strings.Repeat("界", 501)}}}},
		{"context unicode bound", BrowserDecisionInput{Kind: DecisionTargetRank, Intent: "x", State: json.RawMessage(`{}`), Candidates: []BrowserDecisionCandidate{{ID: "a", Context: strings.Repeat("界", 1001)}}}},
		{"candidate on action", BrowserDecisionInput{Kind: DecisionActionOutcome, Intent: "x", State: json.RawMessage(`{}`), Candidates: []BrowserDecisionCandidate{{ID: "a"}}}},
		{"empty candidates on action", BrowserDecisionInput{Kind: DecisionActionOutcome, Intent: "x", State: json.RawMessage(`{}`), Candidates: []BrowserDecisionCandidate{}}},
		{"candidate on suspicious", BrowserDecisionInput{Kind: DecisionSuspiciousInstruction, Intent: "x", State: json.RawMessage(`{}`), Candidates: []BrowserDecisionCandidate{{ID: "a"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := BrowserDecisionRequest(tc.in, "jev-1.13.0"); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}

func TestBrowserDecisionJSONRejectsSuppliedEmptyOrNullNonTargetCandidates(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"action_outcome","intent":"x","state":{},"candidates":[]}`,
		`{"kind":"action_outcome","intent":"x","state":{},"candidates":null}`,
	} {
		var in BrowserDecisionInput
		if err := json.Unmarshal([]byte(raw), &in); err != nil {
			t.Fatal(err)
		}
		if _, err := BrowserDecisionRequest(in, "jev-1.13.0"); err == nil {
			t.Fatalf("supplied candidates accepted: %s", raw)
		}
	}
}

func TestBrowserDecisionAcceptsExactUnicodeAndCandidateBounds(t *testing.T) {
	in := BrowserDecisionInput{
		Kind:   DecisionTargetRank,
		Intent: strings.Repeat("界", MaxDecisionIntentRunes),
		State:  json.RawMessage(`{"ordinary_tokenized_word":true}`),
		Candidates: []BrowserDecisionCandidate{{
			ID: strings.Repeat("界", 200), Role: strings.Repeat("界", 100),
			Name: strings.Repeat("界", 500), Context: strings.Repeat("界", 1000),
		}},
	}
	if _, err := BrowserDecisionRequest(in, "jev-1.13.0"); err != nil {
		t.Fatalf("exact bounds rejected: %v", err)
	}
	forty := validBrowserDecisionInput(DecisionTargetRank)
	forty.Candidates = nil
	for i := 0; i < MaxDecisionCandidates; i++ {
		forty.Candidates = append(forty.Candidates, BrowserDecisionCandidate{ID: fmt.Sprintf("candidate-%d", i)})
	}
	if _, err := BrowserDecisionRequest(forty, "jev-1.13.0"); err != nil {
		t.Fatalf("%d candidates rejected: %v", MaxDecisionCandidates, err)
	}
}

func TestBrowserDecisionRejectsEverySecretLookingKeyToken(t *testing.T) {
	keys := []string{
		"password", "account_passwd", "clientSecret", "authToken", "card",
		"creditCard", "debit-card", "cardNumber", "paymentCVV", "myPASSWORDValue",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{"nested": []any{map[string]any{key: "x"}}})
			if err != nil {
				t.Fatal(err)
			}
			in := BrowserDecisionInput{Kind: DecisionActionOutcome, Intent: "x", State: raw}
			if _, err := BrowserDecisionRequest(in, "jev-1.13.0"); err == nil {
				t.Fatalf("secret-looking key %q accepted", key)
			}
		})
	}
}

func TestBrowserDecisionQuestionSemanticsAndFanout(t *testing.T) {
	target := validBrowserDecisionInput(DecisionTargetRank)
	target.Candidates = append(target.Candidates, BrowserDecisionCandidate{ID: "cancel", Role: "button"})
	req, err := BrowserDecisionRequest(target, "jev-1.13.0")
	if err != nil {
		t.Fatal(err)
	}
	decision := req.Questions["decision"]
	ordered := decision.Instructions.(map[string]any)["ordered_options"].([]string)
	if strings.Join(ordered, ",") != "submit,cancel,none" {
		t.Fatalf("ordered options = %v", ordered)
	}
	if len(req.Questions) != 3 || req.Questions["candidate_1"].Type != "noul" || req.Questions["candidate_2"].Type != "noul" {
		t.Fatalf("questions = %#v", req.Questions)
	}

	eight := validBrowserDecisionInput(DecisionTargetRank)
	eight.Candidates = nil
	for i := 0; i < 8; i++ {
		eight.Candidates = append(eight.Candidates, BrowserDecisionCandidate{ID: string(rune('a' + i))})
	}
	req, err = BrowserDecisionRequest(eight, "jev-1.13.0")
	if err != nil || len(req.Questions) != 9 {
		t.Fatalf("eight-candidate fanout questions=%d err=%v", len(req.Questions), err)
	}
	nine := eight
	nine.Candidates = append(nine.Candidates, BrowserDecisionCandidate{ID: "i"})
	req, err = BrowserDecisionRequest(nine, "jev-1.13.0")
	if err != nil || len(req.Questions) != 1 {
		t.Fatalf("nine-candidate fanout questions=%d err=%v", len(req.Questions), err)
	}
	large := eight
	large.State = json.RawMessage(`{"page":"` + strings.Repeat("x", maxDecisionFanoutTokens*4) + `"}`)
	req, err = BrowserDecisionRequest(large, "jev-1.13.0")
	if err != nil || len(req.Questions) != 1 {
		t.Fatalf("large-prompt fanout questions=%d err=%v", len(req.Questions), err)
	}

	action, err := BrowserDecisionRequest(validBrowserDecisionInput(DecisionActionOutcome), "jev-1.13.0")
	if err != nil {
		t.Fatal(err)
	}
	actionOptions := action.Questions["decision"].Instructions.(map[string]any)["ordered_options"].([]string)
	if strings.Join(actionOptions, ",") != "achieved,not_achieved,uncertain" {
		t.Fatalf("action options = %v", actionOptions)
	}
	suspicious, err := BrowserDecisionRequest(validBrowserDecisionInput(DecisionSuspiciousInstruction), "jev-1.13.0")
	if err != nil || suspicious.Questions["decision"].Type != "noul" {
		t.Fatalf("suspicious question = %#v err=%v", suspicious.Questions, err)
	}
	b, _ := json.Marshal(suspicious.Questions["decision"])
	for _, phrase := range []string{"exfiltrate secrets", "change its task", "bypass security", "credential vault", "approve a payment"} {
		if !strings.Contains(string(b), phrase) {
			t.Errorf("suspicious rubric missing %q", phrase)
		}
	}
}

func TestInterpretBrowserDecisionStrictAndPreservesLowConfidence(t *testing.T) {
	in := validBrowserDecisionInput(DecisionTargetRank)
	confidence := 0.12
	noul := 0.31
	response := &typesafe.Response{Model: "jev-1.13.0", Answers: map[string]typesafe.Answer{
		"decision":    {Type: "choice", Choice: "submit", Confidence: &confidence, Probabilities: map[string]float64{"submit": 0.51, "none": 0.49}},
		"candidate_1": {Type: "noul", Noul: &noul},
	}}
	answers, err := InterpretBrowserDecision(in, response)
	if err != nil {
		t.Fatal(err)
	}
	if answers.Decision.Option != "submit" || answers.Decision.Confidence == nil || *answers.Decision.Confidence != confidence {
		t.Fatalf("low-confidence answer changed: %+v", answers)
	}
	if len(answers.CandidateScores) != 1 || answers.CandidateScores[0].ID != "submit" {
		t.Fatalf("candidate scores = %+v", answers.CandidateScores)
	}

	badCases := []struct {
		name string
		edit func(map[string]typesafe.Answer)
	}{
		{"missing", func(a map[string]typesafe.Answer) { delete(a, "candidate_1") }},
		{"extra", func(a map[string]typesafe.Answer) { a["extra"] = typesafe.Answer{Type: "noul", Noul: &noul} }},
		{"wrong type", func(a map[string]typesafe.Answer) { a["decision"] = typesafe.Answer{Type: "noul", Noul: &noul} }},
		{"non finite confidence", func(a map[string]typesafe.Answer) {
			bad := math.NaN()
			x := a["decision"]
			x.Confidence = &bad
			a["decision"] = x
		}},
		{"non finite noul", func(a map[string]typesafe.Answer) {
			bad := math.Inf(1)
			a["candidate_1"] = typesafe.Answer{Type: "noul", Noul: &bad}
		}},
		{"incomplete distribution", func(a map[string]typesafe.Answer) {
			x := a["decision"]
			x.Probabilities = map[string]float64{"submit": 1}
			a["decision"] = x
		}},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			copyAnswers := make(map[string]typesafe.Answer, len(response.Answers))
			for id, answer := range response.Answers {
				copyAnswers[id] = answer
			}
			tc.edit(copyAnswers)
			if _, err := InterpretBrowserDecision(in, &typesafe.Response{Model: response.Model, Answers: copyAnswers}); err == nil {
				t.Fatal("malformed answer accepted")
			}
		})
	}
}
