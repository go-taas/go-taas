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
	CodeLoadTestNotFound      Code = 10308 // LOAD_TEST_NOT_FOUND
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

// webhook module error codes (feature #23, AD2).
const (
	CodeWebhookNotFound         Code = 10701 // WEBHOOK_NOT_FOUND
	CodeWebhookConfigInvalid    Code = 10702 // WEBHOOK_CONFIG_INVALID
	CodeWebhookStateInvalid     Code = 10703 // WEBHOOK_STATE_INVALID
	CodeWebhookDeliveryNotFound Code = 10704 // WEBHOOK_DELIVERY_NOT_FOUND
	CodeWebhookEventTypeInvalid Code = 10705 // WEBHOOK_EVENT_TYPE_INVALID
)

// observability module error codes (feature #24, AD2). The block is
// 10801-10899, the next free block after webhook's 107xx.
const (
	CodeObservabilityModelNotFound Code = 10801 // OBSERVABILITY_MODEL_NOT_FOUND
)

// billing-reports module error codes (feature-25, AD7). The block is
// 10901-10999, the next free block after observability's 108xx.
const (
	CodeReportNotFound           Code = 10901 // REPORT_NOT_FOUND
	CodeScheduleNotFound         Code = 10902 // SCHEDULE_NOT_FOUND
	CodeReportInvalidDimension   Code = 10903 // REPORT_INVALID_DIMENSION
	CodeReportInvalidGranularity Code = 10904 // REPORT_INVALID_GRANULARITY
	CodeReportInvalidFrequency   Code = 10905 // REPORT_INVALID_FREQUENCY
	CodeReportNameConflict       Code = 10906 // REPORT_NAME_CONFLICT
	CodeReportNotReady           Code = 10907 // REPORT_NOT_READY
)

// notification module error codes (feature #26, AD2). The block is
// 11001-11099, the next free block after billing-reports' 109xx.
const (
	CodeNotificationNotFound           Code = 11001 // NOTIFICATION_NOT_FOUND
	CodeNotificationPreferencesInvalid Code = 11002 // NOTIFICATION_PREFERENCES_INVALID
	CodeNotificationThresholdNotFound  Code = 11003 // NOTIFICATION_THRESHOLD_NOT_FOUND
	CodeNotificationThresholdInvalid   Code = 11004 // NOTIFICATION_THRESHOLD_INVALID
	CodeNotificationEventTypeInvalid   Code = 11005 // NOTIFICATION_EVENT_TYPE_INVALID
)

// tracing module error codes (feature #27, AD2). The block is
// 11101-11199, the next free block after notification's 110xx.
const (
	CodeTraceNotFound Code = 11101 // TRACE_NOT_FOUND
)

// usage-keys module error codes (feature #28, AD2). The block is
// 11201-11299, the next free block after tracing's 111xx.
const (
	CodeUsageKeyNotFound Code = 11201 // USAGE_KEY_NOT_FOUND
)

// cost module error codes (feature #29, AD2). The block is
// 11301-11399, the next free block after usage-keys' 112xx.
const (
	CodeCostDimensionInvalid       Code = 11301 // COST_DIMENSION_INVALID
	CodeCostDimensionValueNotFound Code = 11302 // COST_DIMENSION_VALUE_NOT_FOUND
)

// status module error codes (feature #30, AD7). The block is
// 11401-11499, the next free block after cost's 113xx.
const (
	CodeStatusComponentNotFound Code = 11401 // STATUS_COMPONENT_NOT_FOUND
)

// error-analysis module error codes (feature #31, AD2). The block is
// 11501-11599, the next free block after status' 114xx.
const (
	CodeErrorCauseNotFound Code = 11501 // ERROR_CAUSE_NOT_FOUND
)

// model-versioning module error codes (feature #32, AD8). The block is
// 11601-11699, the next free block after error-analysis' 115xx. The
// feature reuses the existing model/infer codes (10101/10102/10103/
// 10301/10303) for every failure mode the design names; this block is
// reserved for any future model-versioning-specific code.
const (
	CodeModelVersionInvalid Code = 11601 // MODEL_VERSION_INVALID
)

// service-logs module error codes (feature #33, AD2). The block is
// 11701-11799, the next free block after model-versioning's 116xx.
const (
	CodeServiceLogsInvalid Code = 11701 // SERVICE_LOGS_INVALID
)

// deployment-history module error codes (feature #34, AD2). The block is
// 11801-11899, the next free block after service-logs' 117xx.
const (
	CodeDeploymentEventNotFound Code = 11801 // DEPLOYMENT_EVENT_NOT_FOUND
)

// playground-comparison module error codes (feature #35, AD2). The block
// is 11901-11999, the next free block after deployment-history's 118xx.
const (
	CodePlaygroundCompareInvalid Code = 11901 // PLAYGROUND_COMPARE_INVALID
)

// usage-cost-forecasting module error codes (feature #36, AD2). The block
// is 12001-12099, the next free block after playground-comparison's 119xx.
const (
	CodeForecastInvalid Code = 12001 // FORECAST_INVALID
)

// resourcemetrics module error codes (feature #37, AD9). The block is
// 12101-12199, the next free block after usage-cost-forecasting's 120xx.
const (
	CodeServiceMetricsInvalid Code = 12101 // SERVICE_METRICS_INVALID
)

// docs module error codes (feature #38, AD2). The block is 12201-12299,
// the next free block after resourcemetrics' 121xx.
const (
	CodeDocsEndpointNotFound Code = 12201 // DOCS_ENDPOINT_NOT_FOUND
)

// finetuning module error codes (feature #39, AD2). The block is
// 12301-12399, the next free block after docs' 122xx.
const (
	CodeFinetuneJobNotFound   Code = 12301 // FINETUNE_JOB_NOT_FOUND
	CodeFinetuneJobInvalid    Code = 12302 // FINETUNE_JOB_INVALID
	CodeFinetuneDatasetInvalid Code = 12303 // FINETUNE_DATASET_INVALID
	CodeFinetuneJobStateInvalid Code = 12304 // FINETUNE_JOB_STATE_INVALID
)

// cluster module error codes (feature #40, AD2). The block is
// 12401-12499, the next free block after finetuning's 123xx.
const (
	CodeClusterNotFound Code = 12401 // CLUSTER_NOT_FOUND
	CodeClusterInvalid  Code = 12402 // CLUSTER_INVALID
)

// account module error codes (feature #41, AD2). The block is
// 12501-12599, the next free block after cluster's 124xx.
const (
	CodeExportNotFound Code = 12501 // EXPORT_NOT_FOUND
	CodeExportInvalid  Code = 12502 // EXPORT_INVALID
)
