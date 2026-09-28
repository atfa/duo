package project

import (
	"fmt"
	"strings"
	"time"
)

// VerificationResult is the structured outcome of a Fast-mode independent
// verification. It is a closed set on purpose: the verifier cannot express an
// ambiguous verdict, and Duo Core never has to guess a verdict from free text.
type VerificationResult string

const (
	// VerificationNone means no verification has been reported for the current
	// completion request yet.
	VerificationNone VerificationResult = ""
	// VerificationPassed means the copilot independently verified the result.
	VerificationPassed VerificationResult = "passed"
	// VerificationIssueFound means the copilot found a concrete problem; the
	// note carries the problem and the workflow returns to RUNNING.
	VerificationIssueFound VerificationResult = "issue_found"
)

// Verification is a structured Fast-mode verification record. Passed results
// are bound to the exact Austin HEAD that was verified; the binding is what
// makes delivery safe, because it lets the live path, the delivery transaction
// and crash recovery share one rule instead of three approximations.
type Verification struct {
	Status VerificationResult `json:"status,omitempty"`
	Head   string             `json:"head,omitempty"`
	Note   string             `json:"note,omitempty"`
	At     *time.Time         `json:"at,omitempty"`
}

// Targets reports whether this record is bound to exactly head, regardless of
// the current verdict. Recording a verdict uses it to reject a verdict about a
// different artifact; it must not require the record to already be passed.
func (v Verification) Targets(head string) bool {
	head = strings.TrimSpace(head)
	return head != "" && strings.TrimSpace(v.Head) == head
}

// Passed reports whether this record accepted exactly head. An empty head on
// either side never matches, so a missing artifact can never be delivered.
func (v Verification) Passed(head string) bool {
	return v.Status == VerificationPassed && v.Targets(head)
}

// Label renders the verification state for status text and the TUI.
func (v Verification) Label() string {
	switch v.Status {
	case VerificationPassed:
		return "passed"
	case VerificationIssueFound:
		return "issue_found"
	default:
		return "not requested"
	}
}

// ParseVerificationResult resolves a wire value into a verification result.
func ParseVerificationResult(value string) (VerificationResult, error) {
	switch VerificationResult(strings.ToLower(strings.TrimSpace(value))) {
	case VerificationPassed:
		return VerificationPassed, nil
	case VerificationIssueFound:
		return VerificationIssueFound, nil
	default:
		return "", fmt.Errorf("unknown verification result %q (expected passed or issue_found)", value)
	}
}
