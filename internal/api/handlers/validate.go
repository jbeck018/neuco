package handlers

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/neuco-ai/neuco/internal/domain"
)

// Field length limits.
const (
	MaxNameLen         = 255
	MaxSlugLen         = 100
	MaxDescriptionLen  = 10_000
	MaxContentLen      = 50_000 // spec fields, context content
	MaxTitleLen        = 500
	MaxPaginationLimit = 100
)

var (
	uuidRE  = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[1-5][a-fA-F0-9]{3}-[89abAB][a-fA-F0-9]{3}-[a-fA-F0-9]{12}$`)
	emailRE = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
)

// ValidationError accumulates validation errors keyed by field name.
type ValidationError struct {
	Fields map[string]string `json:"fields"`
}

// Add records a validation message for a field.
func (v *ValidationError) Add(field, message string) {
	if v.Fields == nil {
		v.Fields = make(map[string]string)
	}
	v.Fields[field] = message
}

// HasErrors returns true when at least one field has a validation error.
func (v *ValidationError) HasErrors() bool {
	return v != nil && len(v.Fields) > 0
}

// Error implements error for ValidationError.
func (v *ValidationError) Error() string {
	if v == nil || len(v.Fields) == 0 {
		return "validation failed"
	}

	parts := make([]string, 0, len(v.Fields))
	keys := make([]string, 0, len(v.Fields))
	for field := range v.Fields {
		keys = append(keys, field)
	}
	sort.Strings(keys)
	for _, field := range keys {
		parts = append(parts, fmt.Sprintf("%s: %s", field, v.Fields[field]))
	}

	return "validation failed: " + strings.Join(parts, ", ")
}

// ValidateRequired ensures a string has a non-empty value.
func ValidateRequired(_ string, value string) string {
	if strings.TrimSpace(value) == "" {
		return "is required"
	}
	return ""
}

// ValidateMinLength ensures a string is at least min characters.
func ValidateMinLength(_ string, value string, min int) string {
	if len(value) < min {
		return fmt.Sprintf("must be at least %d characters", min)
	}
	return ""
}

// ValidateMaxLength ensures a string does not exceed max characters.
func ValidateMaxLength(_ string, value string, max int) string {
	if len(value) > max {
		return fmt.Sprintf("exceeds maximum length of %d characters", max)
	}
	return ""
}

// ValidateUUID ensures the value is a valid UUID string.
func ValidateUUID(_ string, value string) string {
	if !uuidRE.MatchString(strings.TrimSpace(value)) {
		return "must be a valid UUID"
	}
	return ""
}

// ValidateEnum ensures the value is present in the allowed set.
func ValidateEnum(_ string, value string, allowed []string) string {
	for _, a := range allowed {
		if value == a {
			return ""
		}
	}
	return fmt.Sprintf("must be one of: %s", strings.Join(allowed, ", "))
}

// ValidateEmail ensures the value looks like an email address.
func ValidateEmail(_ string, value string) string {
	if !emailRE.MatchString(strings.TrimSpace(value)) {
		return "must be a valid email address"
	}
	return ""
}

// validateStringLen checks that s does not exceed maxLen. Returns an error
// message suitable for the API response, or "" if valid.
func validateStringLen(field, s string, maxLen int) string {
	if len(s) > maxLen {
		return fmt.Sprintf("%s exceeds maximum length of %d characters", field, maxLen)
	}
	return ""
}

// Valid OrgRole values.
var validOrgRoles = map[domain.OrgRole]bool{
	domain.OrgRoleOwner:  true,
	domain.OrgRoleAdmin:  true,
	domain.OrgRoleMember: true,
	domain.OrgRoleViewer: true,
}

func isValidOrgRole(r domain.OrgRole) bool {
	return validOrgRoles[r]
}

// Valid CandidateStatus values.
var validCandidateStatuses = map[domain.CandidateStatus]bool{
	domain.CandidateStatusNew:        true,
	domain.CandidateStatusSpecced:    true,
	domain.CandidateStatusInProgress: true,
	domain.CandidateStatusReviewing:  true,
	domain.CandidateStatusAccepted:   true,
	domain.CandidateStatusRejected:   true,
	domain.CandidateStatusBacklogged: true,
	domain.CandidateStatusShipped:    true,
}

func isValidCandidateStatus(s domain.CandidateStatus) bool {
	return validCandidateStatuses[s]
}

// Valid ProjectFramework values (empty is allowed — means "not set").
var validFrameworks = map[domain.ProjectFramework]bool{
	"":                             true,
	domain.ProjectFrameworkReact:   true,
	domain.ProjectFrameworkNextJS:  true,
	domain.ProjectFrameworkVue:     true,
	domain.ProjectFrameworkSvelte:  true,
	domain.ProjectFrameworkAngular: true,
}

func isValidFramework(f domain.ProjectFramework) bool {
	return validFrameworks[f]
}

// Valid ProjectStyling values (empty is allowed).
var validStylings = map[domain.ProjectStyling]bool{
	"":                            true,
	domain.ProjectStylingTailwind: true,
	domain.ProjectStylingCSS:      true,
	domain.ProjectStylingModules:  true,
	domain.ProjectStylingStyled:   true,
	domain.ProjectStylingEmotion:  true,
}

func isValidStyling(s domain.ProjectStyling) bool {
	return validStylings[s]
}

// Valid ContextCategory values (empty defaults to "insight" in handler).
var validContextCategories = map[domain.ContextCategory]bool{
	"":                                true,
	domain.ContextCategoryInsight:     true,
	domain.ContextCategoryTheme:       true,
	domain.ContextCategoryDecision:    true,
	domain.ContextCategoryRisk:        true,
	domain.ContextCategoryOpportunity: true,
}

func isValidContextCategory(c domain.ContextCategory) bool {
	return validContextCategories[c]
}

// ptrStr dereferences a *string, returning "" if nil.
func ptrStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// clampPagination ensures limit is within [1, MaxPaginationLimit].
func clampPagination(limit int) int {
	if limit < 1 {
		return 50
	}
	if limit > MaxPaginationLimit {
		return MaxPaginationLimit
	}
	return limit
}
