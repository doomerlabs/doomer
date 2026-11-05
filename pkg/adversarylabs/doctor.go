package adversarylabs

import (
	"context"
	"encoding/json"
	"fmt"
)

type DoctorReportResponse struct {
	ID string `json:"id"`
}

func (c Client) UploadDoctorReport(ctx context.Context, token string, report json.RawMessage) (DoctorReportResponse, error) {
	var result DoctorReportResponse
	err := c.postJSON(ctx, "/v1/doctor/reports", report, token, &result)
	if err == nil && result.ID == "" {
		err = fmt.Errorf("server returned no support report ID")
	}
	return result, err
}
