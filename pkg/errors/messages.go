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

	// model
	CodeModelNotFound:        "model not found",
	CodeModelExists:          "model already exists",
	CodeModelVersionNotFound: "model version not found",
	CodeModelPathInvalid:     "model weight path invalid",
	CodeModelUnauthorized:    "model not authorized for tenant",

	// image
	CodeImageNotFound:      "image not found",
	CodeImageExists:        "image already exists",
	CodeImageDigestInvalid: "image digest invalid",
	CodeImageIncompatible:  "image incompatible with target",
	CodeImageWarmupFailed:  "image warmup failed",

	// infer
	CodeInferServiceNotFound:     "inference service not found",
	CodeInferServiceExists:       "inference service already exists",
	CodeInferServiceStateInvalid: "inference service state invalid",
	CodeInferEndpointNotFound:    "inference endpoint not found",
	CodeInferReplicasInvalid:     "inference replicas invalid",
	CodeInferEngineUnsupported:   "inference engine unsupported",

	// metering
	CodeMeteringEventInvalid:    "metering event invalid",
	CodeMeteringVoucherError:    "metering voucher error",
	CodeMeteringVoucherNotFound: "metering voucher not found",
	CodeMeteringRangeInvalid:    "metering range invalid",

	// billing
	CodePriceNotFound:       "price not found",
	CodeInsufficientFunds:   "insufficient funds",
	CodeAccountNotFound:     "account not found",
	CodeBillNotFound:        "bill not found",
	CodeSettlementFailed:    "settlement failed",
	CodeHoldFailed:          "funds hold failed",
	CodePriceInvalid:        "price invalid",
	CodeBillingRangeInvalid: "billing range invalid",
}

// Message returns the canonical message for a code. Unknown codes get a
// generic internal-error message so clients never see an empty string.
func Message(code Code) string {
	if msg, ok := messages[code]; ok {
		return msg
	}
	return "internal error"
}
