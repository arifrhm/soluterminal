package jev

import (
	"errors"
	"math"

	"soluterminal-shared/jevcontract"
	"soluterminal-shared/typesafe"
)

// ApprovalModel and ApprovalThreshold were frozen before the 2026-09-28
// held-out trial. They live in soluterminal-shared/jevcontract so the cloud
// relay can allow-list exactly the requests the daemon sends.
const (
	ApprovalModel     = jevcontract.ApprovalModel
	ApprovalThreshold = jevcontract.ApprovalThreshold
)

// ApprovalQuestions is the fixed cautious ARIA rubric. See jevcontract.
func ApprovalQuestions() map[string]typesafe.Question {
	return jevcontract.ApprovalQuestions()
}

// PermissionMenuQuestions resolves whether a proved menu grants its visible
// action. See jevcontract.
func PermissionMenuQuestions(cautious bool) map[string]typesafe.Question {
	return jevcontract.PermissionMenuQuestions(cautious)
}

// FullPermissionQuestions classifies the selected grant in full mode. See
// jevcontract.
func FullPermissionQuestions() map[string]typesafe.Question {
	return jevcontract.FullPermissionQuestions()
}

func InterpretPermissionMenu(response *typesafe.Response, threshold float64) (bool, error) {
	if response == nil || math.IsNaN(threshold) || threshold <= 0 || threshold > 1 {
		return false, errors.New("invalid Jev permission response or threshold")
	}
	answer, ok := response.Answers["permission_menu"]
	if !ok || answer.Type != "noul" {
		return false, errors.New("missing Jev permission answer")
	}
	p, ok := answer.NoulValue()
	if !ok || math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
		return false, errors.New("invalid Jev permission probability")
	}
	return p >= threshold, nil
}

// InterpretApproval accepts only complete, well-formed Noul probabilities.
// The threshold is deliberately caller-controlled so evaluations can measure
// calibration before changing the production gate.
func InterpretApproval(response *typesafe.Response, threshold float64) (bool, error) {
	if response == nil || math.IsNaN(threshold) || threshold <= 0 || threshold > 1 {
		return false, errors.New("invalid Jev approval response or threshold")
	}
	answer, ok := response.Answers["safe_action"]
	if !ok || answer.Type != "noul" {
		return false, errors.New("missing Jev approval answer")
	}
	p, ok := answer.NoulValue()
	if !ok || math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
		return false, errors.New("invalid Jev approval probability")
	}
	return p >= threshold, nil
}
