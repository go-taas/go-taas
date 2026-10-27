package errors

// messages maps each business code to its canonical, client-safe message.
//
//nolint:gosec // Canonical client-facing messages are static labels, not secrets.
var messages = map[Code]string{
	CodeOK: "OK",

	// auth
	CodeUnauthorized:         "unauthorized",
	CodeInvalidCredentials:   "invalid credentials",
	CodeUserNotFound:         "user not found",
	CodeUserExists:           "user already exists",
	CodeUserDisabled:         "user disabled",
	CodeOrganizationNotFound: "organization not found",
	CodeProjectNotFound:      "project not found",
	CodeAPIKeyNotFound:       "API key not found",
	CodeAPIKeyInvalid:        "API key invalid",
	CodeAPIKeyRevoked:        "API key revoked",
	CodeAPIKeyExpired:        "API key expired",
	CodeIDPConfigInvalid:     "identity provider config invalid",
	CodeIDPAuthFailed:        "identity provider authentication failed",
	CodeIdentityNotBound:     "external identity not bound",
	CodePasswordPolicy:       "password policy violation",

	// tenancy
	CodeOrganizationExists:   "organization already exists",
	CodeProjectExists:        "project already exists",
	CodeOrganizationDisabled: "organization disabled",
	CodeProjectDisabled:      "project disabled",
	CodeTenancyInvalid:       "tenancy invalid",

	// sso federation
	CodeSSOProviderExists:     "sso provider already exists",
	CodeSSOProviderNotFound:   "sso provider not found",
	CodeSSOProviderDisabled:   "sso provider disabled",
	CodeSSOInvalidState:       "sso invalid state",
	CodeSSOAuthFailed:         "sso authentication failed",
	CodeSSONoAccount:          "no account for this identity",
	CodeIdentityBindingExists: "identity binding already exists",
	CodeSessionInvalid:        "session invalid",
	CodeSSOProviderInvalid:    "sso provider invalid",
	CodeMemberExists:          "member already exists",
	CodeMemberNotFound:        "member not found",
	CodeRoleInvalid:           "invalid role",
	CodeOwnerProtected:        "owner is protected",
	CodeInvitationNotFound:    "invitation not found",
	CodeInvitationExpired:     "invitation expired",
	CodeInvitationExists:      "invitation already exists",
	CodeForbidden:             "forbidden",
	CodeRateLimitExceeded:     "rate limit exceeded",
	CodeRealmMismatch:         "session belongs to the other console",
	CodeAPIKeyModelNotAllowed: "model not allowed for this api key",
	CodeOrganizationInUse:     "organization still has api keys or inference services",

	// model
	CodeModelNotFound:        "model not found",
	CodeModelExists:          "model already exists",
	CodeModelVersionNotFound: "model version not found",
	CodeModelPathInvalid:     "model weight path invalid",
	// CodeModelUnauthorized is returned by both enforcement points of
	// per-tenant model authorization (deploy and call).
	CodeModelUnauthorized: "model not authorized",

	// image
	CodeImageNotFound:      "image not found",
	CodeImageExists:        "image already exists",
	CodeImageDigestInvalid: "image digest invalid",
	CodeImageIncompatible:  "image incompatible with target",
	CodeImageWarmupFailed:  "image warmup failed",
	// CodeAcceleratorNodeNotFound (feature #18, AD6).
	CodeAcceleratorNodeNotFound: "accelerator node not found",
	// Compatibility matrix (feature #19, AD8).
	CodeCompatibilityCellNotFound:     "compatibility cell not found",
	CodeCompatibilityStatusInvalid:    "compatibility status invalid",
	CodeCompatibilityDimensionInvalid: "compatibility dimension invalid",
	CodeCompatibilityUnsupported:      "compatibility combination unsupported",

	// infer
	CodeInferServiceNotFound:     "inference service not found",
	CodeInferServiceExists:       "inference service already exists",
	CodeInferServiceStateInvalid: "inference service state invalid",
	CodeInferEndpointNotFound:    "inference endpoint not found",
	CodeInferReplicasInvalid:     "inference replicas invalid",
	CodeInferEngineUnsupported:   "inference engine unsupported",
	CodeAutoscalingPolicyInvalid: "autoscaling policy invalid",
	CodeLoadTestNotFound:         "load test not found",
	CodeLoadTestConfigInvalid:    "load test config invalid",
	CodeLoadTestStateInvalid:     "load test state invalid",
	CodeLoadTestTargetInvalid:    "load test target invalid",
	CodePlaygroundKeyMissing:     "playground request needs the API key",
	CodePlaygroundRequestFailed:  "playground request failed",
	CodeInferLogsUnavailable:     "service logs unavailable",
	CodeInferAccessInvalid:       "inference service access policy invalid",

	// metering
	CodeMeteringEventInvalid:    "metering event invalid",
	CodeMeteringVoucherError:    "metering voucher error",
	CodeMeteringVoucherNotFound: "metering voucher not found",
	CodeMeteringRangeInvalid:    "metering range invalid",
	CodeRequestLogNotFound:      "request log not found",

	// billing
	CodePriceNotFound:         "price not found",
	CodeInsufficientFunds:     "insufficient funds",
	CodeAccountNotFound:       "account not found",
	CodeBillNotFound:          "bill not found",
	CodeSettlementFailed:      "settlement failed",
	CodeHoldFailed:            "funds hold failed",
	CodePriceInvalid:          "price invalid",
	CodeBillingRangeInvalid:   "billing range invalid",
	CodeAccountInvalid:        "account invalid",
	CodeTransactionInvalid:    "transaction invalid",
	CodePaymentChannelInvalid: "payment channel invalid",
	CodePaymentIntentInvalid:  "payment intent invalid",
	CodeInvoiceNotFound:       "invoice not found",
	CodeAutoRechargeInvalid:   "auto recharge invalid",

	// audit
	CodeAuditEventNotFound: "audit event not found",
	CodeAuditExportInvalid: "invalid export format",
	CodeAuditRangeInvalid:  "invalid time range",

	// webhook (feature #23, AD2)
	CodeWebhookNotFound:         "webhook not found",
	CodeWebhookConfigInvalid:    "invalid webhook config",
	CodeWebhookStateInvalid:     "invalid webhook state",
	CodeWebhookDeliveryNotFound: "webhook delivery not found",
	CodeWebhookEventTypeInvalid: "invalid event type",

	// observability (feature #24, AD2)
	CodeObservabilityModelNotFound: "model not found",

	// billing-reports (feature-25, AD7)
	CodeReportNotFound:           "report not found",
	CodeScheduleNotFound:         "schedule not found",
	CodeReportInvalidDimension:   "invalid report dimension",
	CodeReportInvalidGranularity: "invalid report granularity",
	CodeReportInvalidFrequency:   "invalid report frequency",
	CodeReportNameConflict:       "schedule name already exists",
	CodeReportNotReady:           "report not ready",

	// notification (feature #26, AD2)
	CodeNotificationNotFound:           "notification not found",
	CodeNotificationPreferencesInvalid: "invalid notification preferences",
	CodeNotificationThresholdNotFound:  "threshold not found",
	CodeNotificationThresholdInvalid:   "invalid threshold",
	CodeNotificationEventTypeInvalid:   "invalid event type",

	// tracing (feature #27, AD2)
	CodeTraceNotFound: "trace not found",

	// usage-keys (feature #28, AD2)
	CodeUsageKeyNotFound: "usage key not found",

	// cost (feature #29, AD2)
	CodeCostDimensionInvalid:       "invalid cost dimension",
	CodeCostDimensionValueNotFound: "cost dimension value not found",

	// status (feature #30, AD7)
	CodeStatusComponentNotFound: "status component not found",

	// error-analysis (feature #31, AD2)
	CodeErrorCauseNotFound: "error cause not found",

	// resourcemetrics (feature #37, AD9)
	CodeServiceMetricsInvalid: "invalid service metric or replica filter",

	// docs (feature #38, AD2)
	CodeDocsEndpointNotFound: "documentation endpoint not found",

	// cluster (feature #40, AD2)
	CodeClusterNotFound:          "cluster not found",
	CodeClusterExists:            "cluster already exists",
	CodeClusterStateInvalid:      "invalid cluster state",
	CodeClusterKubeconfigInvalid: "invalid cluster kubeconfig reference",

	// account (feature #41, AD2)
	CodeDataExportNotFound:      "data export not found",
	CodeDataExportTypeInvalid:   "invalid data export type",
	CodeDataExportFormatInvalid: "invalid data export format",
	CodeDataExportNotReady:      "data export not ready",

	// batch (feature #42, AD7)
	CodeBatchJobNotFound:     "batch job not found",
	CodeBatchJobStateInvalid: "invalid batch job state",
	CodeBatchInputInvalid:    "invalid JSONL input file",
	CodeBatchInputTooLarge:   "input file too large",
	CodeBatchModelMismatch:   "multiple models in one batch",
	CodeBatchNotCompleted:    "batch not completed",

	// prompt (feature #43, AD7)
	CodePromptNotFound:         "prompt not found",
	CodePromptVersionNotFound:  "prompt version not found",
	CodePromptNameConflict:     "prompt name already exists",
	CodePromptFolderNotFound:   "folder not found",
	CodePromptInvalidContent:   "invalid prompt content",
	CodePromptInvalidVariable:  "invalid variable",
	CodePromptTemplateNotFound: "template not found",
	CodePromptFolderNotEmpty:   "folder not empty",

	// runtime (feature #47, AD9)
	CodeRuntimeUnavailable:       "runtime unavailable",
	CodeRuntimeSchemeInvalid:     "invalid serving scheme",
	CodeRuntimeModelNotFound:     "registered model not found",
	CodeRuntimeModelExists:       "model already registered",
	CodeRuntimeEngineUnsupported: "unsupported inference engine",
	CodeRuntimeReplicasInvalid:   "invalid replica count",
	CodeRuntimeModelNameInvalid:  "invalid model name",
	CodeRuntimeSettingInvalid:    "runtime setting invalid",
	CodeRuntimeRouteNotPublished: "runtime route not published",
	CodeGatewayTokenInvalid:      "invalid data-plane gateway token",

	// provider (feature #48, AD9)
	CodeProviderNotFound:             "provider not found",
	CodeProviderNameConflict:         "provider name already exists",
	CodeProviderNameInvalid:          "invalid provider name",
	CodeProviderBaseURLInvalid:       "invalid provider base url",
	CodeProviderTypeInvalid:          "unsupported provider type",
	CodeProviderAPIKeyRequired:       "provider api key required",
	CodeProviderSecretUnavailable:    "provider secret unavailable",
	CodeProviderModelNotFound:        "provider model not found",
	CodeProviderModelExists:          "provider model already exists",
	CodeProviderModelInvalid:         "invalid provider model",
	CodeProviderDisabled:             "provider disabled",
	CodeProviderUpstreamUnauthorized: "upstream credentials rejected",
	CodeProviderUpstreamUnavailable:  "upstream provider unavailable",
	CodeProviderPublishConflict:      "model name already exists",
	CodeProviderNotPublished:         "model not published",
	CodeProviderInUse:                "provider in use",

	// routing (feature #49, design D12)
	CodeRoutingGroupNotFound:            "routing group not found",
	CodeRoutingGroupNameConflict:        "routing group name already exists",
	CodeRoutingGroupNameInvalid:         "invalid routing group name",
	CodeRoutingGroupCandidateInvalid:    "invalid routing group candidate",
	CodeRoutingGroupCandidateExists:     "routing group candidate already exists",
	CodeRoutingGroupStrategyInvalid:     "invalid routing group strategy",
	CodeRoutingGroupRuleInvalid:         "invalid routing group rule",
	CodeRoutingGroupNoEligibleCandidate: "no eligible routing group candidate",
	CodeRoutingGroupDisabled:            "routing group disabled",
	CodeRoutingGroupInUse:               "routing group in use",
	CodeRoutingGroupRuleExists:          "routing group rule already exists",
	CodeRoutingGroupNameReserved:        "routing group name reserved by a model",

	// optimization (feature #50, design D9)
	CodeRecommendationNotFound:             "recommendation not found",
	CodeRecommendationDismissed:            "recommendation already dismissed",
	CodeRecommendationDismissReasonInvalid: "invalid dismiss reason or note",
	CodeRecommendationRestoreInvalid:       "recommendation is not dismissed",
	CodeOptimizationRangeInvalid:           "invalid optimization filter or range",

	// quota (feature #51, design D4)
	CodeQuotaConcurrencyExceeded:  "organization concurrency limit exceeded",
	CodeQuotaServiceCountExceeded: "organization inference-service limit reached",
	CodeQuotaBatchJobsExceeded:    "organization daily batch-job limit reached",
	CodeQuotaOutputTokensExceeded: "max_tokens exceeds the organization cap",
	CodeQuotaInvalid:              "invalid quota value",
	CodeQuotaNotFound:             "organization quota not found",

	// vgpu (feature #52, design D4)
	CodeVGPUPoolInvalid:          "invalid vGPU pool",
	CodeVGPUSliceTemplateInvalid: "invalid slice template",
	CodeVGPUSliceUnavailable:     "the requested slice is unavailable or not granted",
	CodeVGPUPoolInUse:            "services are using this pool",
	CodeVGPUPoolNotFound:         "vGPU pool not found",

	// fault (feature #53, design D4)
	CodeFaultPolicyInvalid:    "invalid fault policy",
	CodeFaultRecoveryInvalid:  "invalid recovery action",
	CodeFaultNoActionInFlight: "no recovery action is in flight",
	CodeFaultNotFound:         "incident not found",
}

// Message returns the canonical message for a code. Unknown codes get a
// generic internal-error message so clients never see an empty string.
func Message(code Code) string {
	if msg, ok := messages[code]; ok {
		return msg
	}
	return "internal error"
}
