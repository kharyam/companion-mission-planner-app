package api

import (
	"encoding/json"
	"net/http"
)

// APIError is the structured error envelope KAM consumes.
type APIError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

type apiErrorEnvelope struct {
	Error APIError `json:"error"`
}

// Common error codes. Add new codes here so KAM has a stable contract.
const (
	CodeBadRequest       = "BAD_REQUEST"
	CodeUnauthorized     = "UNAUTHORIZED"
	CodeForbidden        = "FORBIDDEN"
	CodeDeviceNotFound   = "DEVICE_NOT_FOUND"
	CodeDeviceNotAuth    = "DEVICE_NOT_AUTHORIZED"
	CodeSlotNotFound     = "SLOT_NOT_FOUND"
	CodeKMZInvalid       = "KMZ_INVALID"
	CodeKMZTooLarge      = "KMZ_TOO_LARGE"
	CodeTransferFailed   = "TRANSFER_FAILED"
	CodeNotImplemented   = "NOT_IMPLEMENTED"
	CodeInternalError    = "INTERNAL_ERROR"
	CodeDeviceDisconnect = "DEVICE_DISCONNECTED"
	CodeDeviceDiskFull   = "DEVICE_DISK_FULL"
	CodeInvalidGUID      = "INVALID_GUID"
	CodeMediaUnavailable = "MEDIA_UNAVAILABLE"
	CodeMediaNotFound    = "MEDIA_NOT_FOUND"
)

func writeError(w http.ResponseWriter, status int, code, msg string, details map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiErrorEnvelope{Error: APIError{Code: code, Message: msg, Details: details}})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
