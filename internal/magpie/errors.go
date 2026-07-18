package magpie

import "fmt"

type ErrorCode string

const (
	ErrValidation ErrorCode = "validation_error"
	ErrPermission ErrorCode = "permission_denied"
	ErrNotFound   ErrorCode = "not_found"
	ErrConflict   ErrorCode = "conflict"
	ErrIntegrity  ErrorCode = "integrity_error"
	ErrCapacity   ErrorCode = "capacity_exceeded"
	ErrInternal   ErrorCode = "internal_error"
)

type AppError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

func (e *AppError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func appErr(code ErrorCode, format string, args ...any) *AppError {
	return &AppError{Code: code, Message: fmt.Sprintf(format, args...)}
}
