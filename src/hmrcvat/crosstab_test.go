package hmrcvat

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCrossTabProblem pins the empty-cross-tab check (ut-docs#5055 item 2):
// core's identity is sum(cross-tab gross) == gross - refund_total -
// multi-purpose vouchers_issued, so a close with no cross-tab is refused only
// when that gap is non-zero. Each row runs through the bridge's Box 1/6 builder.
func TestCrossTabProblem(t *testing.T) {
	mkTax := func(gross, refund, vouchers, taxNet int64) EODCloseExport {
		return EODCloseExport{ZNumber: 43, Report: EODReportForExport{
			From: "2026-07-03T09:00:00+01:00", To: "2026-07-03T21:00:00+01:00",
			Gross: gross, RefundTotal: refund, VouchersIssued: vouchers, TaxNet: taxNet,
		}}
	}
	mk := func(gross, refund, vouchers int64) EODCloseExport { return mkTax(gross, refund, vouchers, 0) }
	rows := []struct {
		name  string
		c     EODCloseExport
		want  []string // empty: accepted
		never []string
	}{
		{"AC9 voucher-only close", mk(2500, 0, 2500), nil, nil},
		{"AC10 sales without cross-tab", mk(5000, 0, 0), []string{"Z43", "2026-07-03T09:00:00+01:00 to 2026-07-03T21:00:00+01:00", "£50.00", "method_tax_bands"}, []string{"Z43 ()", "( )"}},
		{"AC11 refunds only", mk(0, 1200, 0), []string{"Z43", "-£12.00", "method_tax_bands"}, nil},
		{"AC12 empty close", mk(0, 0, 0), nil, nil},
		{"balanced mix", mk(3000, 500, 2500), nil, nil},
		{"sales beside vouchers", mk(3000, 0, 2500), []string{"£5.00"}, nil},
		// £60 sold at 20% (VAT £10) and £60 returned at 0%: the gross gap is
		// zero but £10 VAT is due, so an empty cross-tab must be refused.
		{"offsetting sale and return at different rates", mkTax(6000, 6000, 0, 1000), []string{"Z43", "£0.00", "VAT £10.00"}, nil},
		{"balanced sale and return, same rate", mkTax(6000, 6000, 0, 0), nil, nil},
	}
	base, err := BuildSubmitRequest(fixtureCloses(), ManualBoxes{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		closes := append(fixtureCloses(), r.c)
		req, errB := BuildSubmitRequest(closes, ManualBoxes{})
		if len(r.want) == 0 {
			if errB != nil {
				t.Errorf("%s: refused: %v", r.name, errB)
				continue
			}
			if req.VATDueSales != base.VATDueSales || req.TotalValueSalesExVAT != base.TotalValueSalesExVAT {
				t.Errorf("%s: bridge boxes moved: %+v", r.name, req)
			}
			continue
		}
		for who, err := range map[string]error{"bridge": errB} {
			if err == nil {
				t.Errorf("%s/%s: accepted, want refusal", r.name, who)
				continue
			}
			for _, w := range r.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("%s/%s: %q does not mention %q", r.name, who, err, w)
				}
			}
			for _, w := range r.never {
				if strings.Contains(err.Error(), w) {
					t.Errorf("%s/%s: %q must not contain %q", r.name, who, err, w)
				}
			}
		}
	}
}

// TestEODCloseExport_DecodesVoucherFields pins the JSON tags of the fields
// the cross-tab check reads.
func TestEODCloseExport_DecodesVoucherFields(t *testing.T) {
	var c EODCloseExport
	if err := json.Unmarshal([]byte(`{"z_number":5,"report":{"gross":2500,"refund_total":0,"vouchers_issued":2500,"method_tax_bands":null}}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.Report.VouchersIssued != 2500 || c.Report.Gross != 2500 {
		t.Fatalf("decoded %+v", c.Report)
	}
	if _, err := BuildSubmitRequest([]EODCloseExport{c}, ManualBoxes{}); err != nil {
		t.Errorf("voucher-only close refused: %v", err)
	}
	var r EODCloseExport
	if err := json.Unmarshal([]byte(`{"z_number":6,"report":{"refund_total":1200,"tax_net":-200}}`), &r); err != nil || r.Report.RefundTotal != 1200 || r.Report.TaxNet != -200 {
		t.Fatalf("refund_total/tax_net not decoded: %v %+v", err, r.Report)
	}
}
