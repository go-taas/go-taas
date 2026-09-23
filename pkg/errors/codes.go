// Package errors provides the platform-wide structured error model and the
// canonical business error codes.
//
// Error code blocks are allocated per module so codes never drift between
// the REST/gRPC layers of different services:
//
//   - auth:     10001-10099
//   - model:    10101-10199
//   - image:    10201-10299
//   - infer:    10301-10399
//   - metering: 10401-10499
//   - billing:  10501-10599
//
// All services share this package so error codes stay consistent across
// the control-plane gateway and the gRPC server.
package errors

// Code is a business error code carried in the unified API response envelope.
type Code int32

// CodeOK indicates success (code == 0).
const CodeOK Code = 0

// CodeInternal is the fallback code for unexpected errors.
const CodeInternal Code = 500

// auth module error codes.
const (
	CodeUnauthorized         Code = 10001 // UNAUTHORIZED
	CodeInvalidCredentials   Code = 10002 // INVALID_CREDENTIALS
	CodeUserNotFound         Code = 10003 // USER_NOT_FOUND
	CodeUserExists           Code = 10004 // USER_EXISTS
	CodeOrganizationNotFound Code = 10005 // ORGANIZATION_NOT_FOUND
	CodeProjectNotFound      Code = 10006 // PROJECT_NOT_FOUND
	CodeAPIKeyNotFound       Code = 10007 // API_KEY_NOT_FOUND
	CodeAPIKeyInvalid        Code = 10008 // API_KEY_INVALID
	CodeAPIKeyRevoked        Code = 10009 // API_KEY_REVOKED
	CodeAPIKeyExpired        Code = 10010 // API_KEY_EXPIRED
	CodeIDPConfigInvalid     Code = 10011 // IDP_CONFIG_INVALID
	CodeIDPAuthFailed        Code = 10012 // IDP_AUTH_FAILED
	CodeIdentityNotBound     Code = 10013 // IDENTITY_NOT_BOUND
	CodePasswordPolicy       Code = 10014 // PASSWORD_POLICY_VIOLATION
)

// model module error codes.
const (
	CodeModelNotFound        Code = 10101 // MODEL_NOT_FOUND
	CodeModelExists          Code = 10102 // MODEL_EXISTS
	CodeModelVersionNotFound Code = 10103 // MODEL_VERSION_NOT_FOUND
	CodeModelPathInvalid     Code = 10104 // MODEL_PATH_INVALID
	CodeModelUnauthorized    Code = 10105 // MODEL_NOT_AUTHORIZED
)

// image module error codes.
const (
	CodeImageNotFound         Code = 10201 // IMAGE_NOT_FOUND
	CodeImageExists           Code = 10202 // IMAGE_EXISTS
	CodeImageDigestInvalid    Code = 10203 // IMAGE_DIGEST_INVALID
	CodeImageIncompatible     Code = 10204 // IMAGE_INCOMPATIBLE
	CodeImageWarmupFailed     Code = 10205 // IMAGE_WARMUP_FAILED
	CodeImageInUse            Code = 10206 // IMAGE_IN_USE
	CodeImageReferenceInvalid Code = 10207 // IMAGE_REFERENCE_INVALID
)

// infer module error codes.
const (
	CodeInferServiceNotFound     Code = 10301 // INFER_SERVICE_NOT_FOUND
	CodeInferServiceExists       Code = 10302 // INFER_SERVICE_EXISTS
	CodeInferServiceStateInvalid Code = 10303 // INFER_SERVICE_STATE_INVALID
	CodeInferEndpointNotFound    Code = 10304 // INFER_ENDPOINT_NOT_FOUND
	CodeInferReplicasInvalid     Code = 10305 // INFER_REPLICAS_INVALID
	CodeInferEngineUnsupported   Code = 10306 // INFER_ENGINE_UNSUPPORTED
)

// metering module error codes.
const (
	CodeMeteringEventInvalid    Code = 10401 // METERING_EVENT_INVALID
	CodeMeteringVoucherError    Code = 10402 // METERING_VOUCHER_ERROR
	CodeMeteringVoucherNotFound Code = 10403 // METERING_VOUCHER_NOT_FOUND
	CodeMeteringRangeInvalid    Code = 10404 // METERING_RANGE_INVALID
)

// billing module error codes.
const (
	CodePriceNotFound     Code = 10501 // PRICE_NOT_FOUND
	CodeInsufficientFunds Code = 10502 // INSUFFICIENT_FUNDS
	CodeAccountNotFound   Code = 10503 // ACCOUNT_NOT_FOUND
	CodeBillNotFound      Code = 10504 // BILL_NOT_FOUND
	CodeSettlementFailed  Code = 10505 // SETTLEMENT_FAILED
	CodeHoldFailed        Code = 10506 // HOLD_FAILED
)
