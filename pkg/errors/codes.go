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

	// Tenancy error codes (organizations and projects). Allocated in
	// the auth block because tenancy refines the org/project context
	// that auth introduced (10005/10006).
	CodeOrganizationExists   Code = 10015 // ORGANIZATION_EXISTS
	CodeProjectExists        Code = 10016 // PROJECT_EXISTS
	CodeOrganizationDisabled Code = 10017 // ORGANIZATION_DISABLED
	CodeProjectDisabled      Code = 10018 // PROJECT_DISABLED (reserved: no v1 write path gates on project state)
	CodeTenancyInvalid       Code = 10019 // TENANCY_INVALID

	// SSO federation error codes (feature #7).
	CodeSSOProviderExists     Code = 10020 // SSO_PROVIDER_EXISTS
	CodeSSOProviderNotFound   Code = 10021 // SSO_PROVIDER_NOT_FOUND
	CodeSSOProviderDisabled   Code = 10022 // SSO_PROVIDER_DISABLED
	CodeSSOInvalidState       Code = 10023 // SSO_INVALID_STATE
	CodeSSOAuthFailed         Code = 10024 // SSO_AUTH_FAILED
	CodeSSONoAccount          Code = 10025 // SSO_NO_ACCOUNT
	CodeIdentityBindingExists Code = 10026 // IDENTITY_BINDING_EXISTS
	CodeSessionInvalid        Code = 10027 // SESSION_INVALID
	CodeSSOProviderInvalid    Code = 10028 // SSO_PROVIDER_INVALID
	CodeMemberExists          Code = 10029 // MEMBER_EXISTS
	CodeMemberNotFound        Code = 10030 // MEMBER_NOT_FOUND
	CodeRoleInvalid           Code = 10031 // ROLE_INVALID
	CodeOwnerProtected        Code = 10032 // OWNER_PROTECTED
	CodeInvitationNotFound    Code = 10033 // INVITATION_NOT_FOUND
	CodeInvitationExpired     Code = 10034 // INVITATION_EXPIRED
	CodeInvitationExists      Code = 10035 // INVITATION_EXISTS
	CodeForbidden             Code = 10036 // FORBIDDEN
	CodeRateLimitExceeded     Code = 10037 // RATE_LIMIT_EXCEEDED
	CodeRealmMismatch         Code = 10038 // REALM_MISMATCH
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
	// CodeAcceleratorNodeNotFound is returned by the accelerator
	// inventory when a node id is absent from the projection cache
	// (feature #18, AD6). It lives in the image block because the
	// accelerator inventory is the node/card-type inventory the image
	// module defers to.
	CodeAcceleratorNodeNotFound Code = 10208 // ACCELERATOR_NODE_NOT_FOUND

	// Compatibility matrix error codes (feature #19, AD8). They live in
	// the image block because the matrix is owned by the image module.
	CodeCompatibilityCellNotFound     Code = 10209 // COMPATIBILITY_CELL_NOT_FOUND
	CodeCompatibilityStatusInvalid    Code = 10210 // COMPATIBILITY_STATUS_INVALID
	CodeCompatibilityDimensionInvalid Code = 10211 // COMPATIBILITY_DIMENSION_INVALID
	// CodeCompatibilityUnsupported is returned by the infer deploy-time
	// enforcement when a (model, engine, card_type) combination is
	// unsupported (feature #19, AD13).
	CodeCompatibilityUnsupported Code = 10212 // COMPATIBILITY_UNSUPPORTED
)

// infer module error codes.
const (
	CodeInferServiceNotFound     Code = 10301 // INFER_SERVICE_NOT_FOUND
	CodeInferServiceExists       Code = 10302 // INFER_SERVICE_EXISTS
	CodeInferServiceStateInvalid Code = 10303 // INFER_SERVICE_STATE_INVALID
	CodeInferEndpointNotFound    Code = 10304 // INFER_ENDPOINT_NOT_FOUND
	CodeInferReplicasInvalid     Code = 10305 // INFER_REPLICAS_INVALID
	CodeInferEngineUnsupported   Code = 10306 // INFER_ENGINE_UNSUPPORTED
	CodeAutoscalingPolicyInvalid Code = 10307 // AUTOSCALING_POLICY_INVALID

	// Load-testing error codes (feature #20, AD8).
	CodeLoadTestNotFound     Code = 10308 // LOAD_TEST_NOT_FOUND
	CodeLoadTestConfigInvalid Code = 10309 // LOAD_TEST_CONFIG_INVALID
	CodeLoadTestStateInvalid  Code = 10310 // LOAD_TEST_STATE_INVALID
	CodeLoadTestTargetInvalid Code = 10311 // LOAD_TEST_TARGET_INVALID
)

// metering module error codes.
const (
	CodeMeteringEventInvalid    Code = 10401 // METERING_EVENT_INVALID
	CodeMeteringVoucherError    Code = 10402 // METERING_VOUCHER_ERROR
	CodeMeteringVoucherNotFound Code = 10403 // METERING_VOUCHER_NOT_FOUND
	CodeMeteringRangeInvalid    Code = 10404 // METERING_RANGE_INVALID
	CodeRequestLogNotFound      Code = 10405 // REQUEST_LOG_NOT_FOUND
)

// billing module error codes.
const (
	CodePriceNotFound         Code = 10501 // PRICE_NOT_FOUND
	CodeInsufficientFunds     Code = 10502 // INSUFFICIENT_FUNDS
	CodeAccountNotFound       Code = 10503 // ACCOUNT_NOT_FOUND
	CodeBillNotFound          Code = 10504 // BILL_NOT_FOUND
	CodeSettlementFailed      Code = 10505 // SETTLEMENT_FAILED
	CodeHoldFailed            Code = 10506 // HOLD_FAILED
	CodePriceInvalid          Code = 10507 // PRICE_INVALID
	CodeBillingRangeInvalid   Code = 10508 // BILLING_RANGE_INVALID
	CodeAccountInvalid        Code = 10509 // ACCOUNT_INVALID
	CodeTransactionInvalid    Code = 10510 // TRANSACTION_INVALID
	CodePaymentChannelInvalid Code = 10511 // PAYMENT_CHANNEL_INVALID
	CodePaymentIntentInvalid  Code = 10512 // PAYMENT_INTENT_INVALID
	CodeInvoiceNotFound       Code = 10513 // INVOICE_NOT_FOUND
	CodeAutoRechargeInvalid   Code = 10514 // AUTO_RECHARGE_INVALID
)

// audit module error codes.
const (
	CodeAuditEventNotFound Code = 10601 // AUDIT_EVENT_NOT_FOUND
	CodeAuditExportInvalid Code = 10602 // AUDIT_EXPORT_INVALID
	CodeAuditRangeInvalid  Code = 10603 // AUDIT_RANGE_INVALID
)
