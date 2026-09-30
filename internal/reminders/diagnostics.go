package reminders

import (
	"errors"
	"icloud-reminders/internal/cloudkit"
)

// DiagnosticError preserves deliberately safe evidence across service layers.
type DiagnosticError struct {
	Public       *Error
	WriteStatus  string
	RetryClass   string
	HTTPStatus   int
	UpstreamCode string
}

func (e *DiagnosticError) Error() string { return e.Public.Error() }
func (e *DiagnosticError) Unwrap() error { return e.Public }

func beforeWrite(err error) error {
	public := PublicError(err)
	retry := "retryable_safe"
	switch public.Code {
	case "invalid_argument", "not_found", "auth_required", "icloud_access_denied", "permission_denied", "not_shared":
		retry = "not_retryable"
	}
	failure := &DiagnosticError{Public: public, WriteStatus: "not_sent", RetryClass: retry}
	var api *cloudkit.APIError
	if errors.As(err, &api) {
		failure.HTTPStatus = api.StatusCode
	}
	return failure
}

func operationFailure(err error) error {
	var existing *DiagnosticError
	if errors.As(err, &existing) {
		return existing
	}
	var write *cloudkit.WriteFailure
	if errors.As(err, &write) {
		retry := "retryable_after_read"
		if write.Status == "failed" {
			retry = "not_retryable"
		}
		return &DiagnosticError{Public: PublicError(err), WriteStatus: write.Status,
			RetryClass: retry, HTTPStatus: write.HTTPStatus, UpstreamCode: write.UpstreamCode}
	}
	var public *Error
	if errors.As(err, &public) {
		switch public.Code {
		case "invalid_argument", "not_found", "permission_denied", "not_shared", "unsupported_structure":
			return beforeWrite(err)
		}
	}
	return &DiagnosticError{Public: PublicError(err), WriteStatus: "unknown", RetryClass: "retryable_after_read"}
}

func ErrorDetails(err error, operation, requestID string, mutation bool) map[string]any {
	public := PublicError(err)
	out := map[string]any{"error_code": public.Code, "operation": operation, "request_id": requestID,
		"retry_class": "retryable_safe"}
	if mutation {
		out["write_status"], out["retry_class"] = "unknown", "retryable_after_read"
		switch public.Code {
		case "invalid_argument", "not_found", "not_shared", "permission_denied":
			out["write_status"], out["retry_class"] = "not_sent", "not_retryable"
		}
	}
	var diagnostic *DiagnosticError
	if errors.As(err, &diagnostic) {
		if mutation {
			out["write_status"] = diagnostic.WriteStatus
		}
		out["retry_class"] = diagnostic.RetryClass
		if diagnostic.HTTPStatus != 0 {
			out["upstream_status"] = diagnostic.HTTPStatus
		}
		if diagnostic.UpstreamCode != "" {
			out["upstream_error_code"] = diagnostic.UpstreamCode
		}
	}
	out["retryable"] = out["retry_class"] != "not_retryable"
	return out
}
