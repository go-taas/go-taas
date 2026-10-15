package tenancy

import (
	"context"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// Quota dimension range bounds (feature #51, design D2). 0 = unlimited;
// when set the value must sit inside the documented range.
const (
	// MaxConcurrentRequestsMax is the upper bound for the in-flight
	// request cap.
	MaxConcurrentRequestsMax = 10000
	// MaxInferenceServicesMax is the upper bound for the service cap.
	MaxInferenceServicesMax = 1000
	// MaxBatchJobsPerDayMax is the upper bound for the daily batch cap.
	MaxBatchJobsPerDayMax = 10000
	// MaxOutputTokensPerRequestMin is the lower bound for the per-request
	// output-token cap (64).
	MaxOutputTokensPerRequestMin = 64
	// MaxOutputTokensPerRequestMax is the upper bound for the
	// per-request output-token cap.
	MaxOutputTokensPerRequestMax = 32768
)

// OrgQuota is the four-dimension org resource quota (feature #51,
// FR1.1). All fields are int64 with 0 = unlimited.
type OrgQuota struct {
	MaxConcurrentRequests     int64
	MaxInferenceServices      int64
	MaxBatchJobsPerDay        int64
	MaxOutputTokensPerRequest int64
}

// OrgQuotaReader is the read-only seam through which the auth, infer
// and batch services read an organization's quota (feature #51, AD13).
// It is implemented by the tenancy repository and exposed on the
// tenancy service. A nil reader reads as unlimited (the transitional
// seam convention).
type OrgQuotaReader interface {
	// OrgQuota returns the org's four quota limits. An unknown org
	// returns the zero struct (all unlimited), never an error — a
	// missing org during verdict construction must read as unlimited,
	// never fail auth (AD12's fail-open rule).
	OrgQuota(ctx context.Context, organizationID string) (OrgQuota, error)
}

// ValidateQuota validates one quota dimension value against its range.
// It returns CodeQuotaInvalid with the dimension named in the message
// when the value is neither 0 (unlimited) nor inside the documented
// range (feature #51, FR1.4).
func ValidateQuota(dimension string, value int64) error {
	switch dimension {
	case "max_concurrent_requests":
		if value < 0 || value > MaxConcurrentRequestsMax {
			return apierrors.Newf(apierrors.CodeQuotaInvalid, "max_concurrent_requests must be 0 (unlimited) or 1-10000")
		}
	case "max_inference_services":
		if value < 0 || value > MaxInferenceServicesMax {
			return apierrors.Newf(apierrors.CodeQuotaInvalid, "max_inference_services must be 0 (unlimited) or 1-1000")
		}
	case "max_batch_jobs_per_day":
		if value < 0 || value > MaxBatchJobsPerDayMax {
			return apierrors.Newf(apierrors.CodeQuotaInvalid, "max_batch_jobs_per_day must be 0 (unlimited) or 1-10000")
		}
	case "max_output_tokens_per_request":
		if value < 0 || (value != 0 && (value < MaxOutputTokensPerRequestMin || value > MaxOutputTokensPerRequestMax)) {
			return apierrors.Newf(apierrors.CodeQuotaInvalid, "max_output_tokens_per_request must be 0 (unlimited) or 64-32768")
		}
	default:
		return apierrors.Newf(apierrors.CodeQuotaInvalid, "unknown quota dimension")
	}
	return nil
}
