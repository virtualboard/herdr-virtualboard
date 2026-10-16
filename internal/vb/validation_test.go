package vb

import (
	"context"
	"testing"
)

func TestValidationRetainsFailureDetailsAcrossCLIVersions(t *testing.T) {
	// Older vb releases exit zero on success=false. Newer releases return
	// validation's nonzero exit while preserving the same detailed report.
	for _, code := range []string{"0", "1"} {
		t.Run(code, func(t *testing.T) {
			client, _ := fakeVB(t, `echo '{"success":false,"message":"validation complete","data":{"id":"FTR-0001","status":"backlog","errors":["status requires directory backlog"]}}'; exit `+code)
			result, err := client.Validate(context.Background(), "FTR-0001", false)
			if err != nil {
				t.Fatal(err)
			}
			if result.OK() || result.Features.Invalid != 1 || len(result.Features.Results["FTR-0001"].Errors) != 1 {
				t.Fatalf("invalid individual spec was accepted or lost its diagnostics: %+v", result)
			}
		})
	}
}

func TestValidationNormalizesSuccessfulIndividualSpec(t *testing.T) {
	client, _ := fakeVB(t, `echo '{"success":true,"data":{"id":"FTR-0001","status":"review","errors":[]}}'`)
	result, err := client.Validate(context.Background(), "FTR-0001", false)
	if err != nil || !result.OK() || result.Features.Total != 1 || result.Features.Valid != 1 {
		t.Fatalf("individual validation result not normalized: %+v, %v", result, err)
	}
}

func TestValidationRejectsIncompleteAndContradictoryReports(t *testing.T) {
	for _, data := range []string{
		`{}`, `null`, `{"id":"FTR-0001","status":"backlog"}`,
		`{"id":"FTR-0001","errors":[]}`,
		`{"features":{}}`,
		`{"features":{"total":1,"valid":1,"invalid":0,"results":{}}}`,
		`{"features":{"total":1,"valid":1,"invalid":0,"results":{"FTR-0001":{"status":"review","errors":["bad"]}}}}`,
		`{"features":{"total":-1,"valid":0,"invalid":-1,"results":{}}}`,
		`{"features":{"total":1,"valid":1,"invalid":0,"results":{"FTR-0001":{"status":"review"}}}}`,
	} {
		t.Run(data, func(t *testing.T) {
			client, _ := fakeVB(t, "echo '{\"success\":true,\"data\":"+data+"}'")
			if _, err := client.Validate(context.Background(), "FTR-0001", false); err == nil {
				t.Fatal("incomplete validation accepted")
			}
		})
	}
	client, _ := fakeVB(t, `echo '{"success":false,"data":{"id":"FTR-0001","status":"review","errors":[]}}'; exit 1`)
	if _, err := client.Validate(context.Background(), "FTR-0001", false); err == nil {
		t.Fatal("failed process accepted as successful validation")
	}
}

func TestValidationAcceptsCompleteGroupAndNullErrors(t *testing.T) {
	client, _ := fakeVB(t, `echo '{"success":true,"data":{"features":{"total":1,"valid":1,"invalid":0,"results":{"FTR-0001":{"status":"review","errors":null}}}}}'`)
	report, err := client.Validate(context.Background(), "FTR-0001", false)
	if err != nil || !report.OK() {
		t.Fatalf("complete report: %+v %v", report, err)
	}
}
