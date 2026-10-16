package vb

import (
	"context"
	"strings"
	"testing"
)

func TestDoctorPinsReadOnlyArgv(t *testing.T) {
	client, argv := fakeVB(t, `echo '{"success":true,"data":{"format_version":1,"ok":true,"version":"test","capabilities":["lease-tokens"],"checks":[{"name":"runtime","ok":true,"fatal":false,"detail":"available"}]}}'`)
	report, err := client.Doctor(context.Background(), true)
	if err != nil || report == nil || !report.OK {
		t.Fatalf("Doctor: %+v, %v", report, err)
	}
	if got := argv()[0]; got != "--root /project --json doctor --capabilities-only" {
		t.Fatalf("argv = %q", got)
	}
}

func TestDoctorRetainsUnhealthyReport(t *testing.T) {
	client, _ := fakeVB(t, `echo '{"success":false,"data":{"format_version":1,"ok":false,"version":"test","capabilities":[],"checks":[{"name":"schema","ok":false,"fatal":true,"detail":"invalid schema","remedy":"restore schema"}]}}'; exit 1`)
	report, err := client.Doctor(context.Background(), false)
	if err != nil || report == nil || report.OK || report.Checks[0].Remedy != "restore schema" {
		t.Fatalf("Doctor: %+v, %v", report, err)
	}
}

func TestDoctorRejectsIncompleteAndContradictoryReports(t *testing.T) {
	for _, data := range []string{
		`{}`,
		`{"format_version":2,"ok":true,"version":"future","capabilities":[],"checks":[]}`,
		`{"format_version":1,"ok":true,"version":"test","capabilities":[],"checks":[]}`,
		`{"format_version":1,"ok":false,"version":"test","capabilities":[],"checks":[{"name":"schema","ok":false,"fatal":true,"detail":"invalid"}]}`,
		`{"format_version":1,"ok":true,"version":"test","capabilities":[],"checks":[{"name":"schema","ok":false,"fatal":true,"detail":"invalid"}]}`,
		`{"format_version":1,"ok":true,"version":"test","capabilities":[],"checks":[{"name":"runtime","detail":"available"}]}`,
	} {
		t.Run(data, func(t *testing.T) {
			client, _ := fakeVB(t, "echo '{\"success\":true,\"data\":"+data+"}'")
			if _, err := client.Doctor(context.Background(), false); err == nil {
				t.Fatal("unsafe doctor report accepted")
			}
		})
	}
	client, _ := fakeVB(t, `echo '{"success":false,"data":{"format_version":1,"ok":true,"version":"test","capabilities":[],"checks":[{"name":"runtime","ok":true,"fatal":false,"detail":"available"}]}}'; exit 1`)
	if _, err := client.Doctor(context.Background(), false); err == nil || !strings.Contains(err.Error(), "inconsistent") {
		t.Fatalf("contradictory exit accepted: %v", err)
	}
}

func TestDispatchCapabilitiesRequireEverySafetyInterface(t *testing.T) {
	capabilities := []string{"owner-leases", "lease-tokens", "lease-renewal", "raw-schema-validation", "validation-exit-status"}
	report := &DoctorReport{Capabilities: capabilities}
	if err := report.RequireDispatchCapabilities(); err != nil {
		t.Fatal(err)
	}
	for omit := range capabilities {
		filtered := append([]string{}, capabilities[:omit]...)
		filtered = append(filtered, capabilities[omit+1:]...)
		if err := (&DoctorReport{Capabilities: filtered}).RequireDispatchCapabilities(); err == nil {
			t.Fatalf("accepted missing %s", capabilities[omit])
		}
	}
}
