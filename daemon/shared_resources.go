package main

import (
	"errors"

	"slices"
)

// admissionCPUSomeBlock and admissionCPUFullBlock are the measured CPU PSI
// percentages at which fresh pressure telemetry blocks new heavy work. They
// also define the exact inverse clear predicate used by the controller.
const (
	admissionCPUSomeBlock = 50.0
	admissionCPUFullBlock = 50.0
)

func admissionErrorClassOf(err error) string {
	var admissionErr *resourceAdmissionError
	if errors.As(err, &admissionErr) {
		return admissionErr.class
	}
	return ""
}

type resourceAdmissionError struct {
	detail     string
	cause      error
	class      string
	conditions []string
}

func admissionErrorConditionsOf(err error) []string {
	var admissionErr *resourceAdmissionError
	if errors.As(err, &admissionErr) {
		return slices.Clone(admissionErr.conditions)
	}
	return nil
}

func (e *resourceAdmissionError) Error() string {
	if e.cause == nil {
		return e.detail
	}
	return e.detail + ": " + e.cause.Error()
}

func (e *resourceAdmissionError) Unwrap() error { return e.cause }
