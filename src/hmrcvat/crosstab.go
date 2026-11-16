package hmrcvat

import "fmt"

// closeLabel names a close's period for a message: its window, else the
// legacy day, else "no date".
func closeLabel(r EODReportForExport) string {
	switch {
	case r.From != "" && r.To != "":
		return r.From + " to " + r.To
	case r.To != "":
		return "to " + r.To
	case r.Day != "":
		return r.Day
	}
	return "no date"
}

// pounds formats pence as a signed pounds string ("£50.00", "-£12.00").
func pounds(p int64) string {
	if p < 0 {
		return "-£" + Amount(-p).String()
	}
	return "£" + Amount(p).String()
}

// crossTabProblem returns "" when c's cross-tab can account for its turnover,
// else the refusal clause naming the Z, its window and the unexplained amount.
// Core's identities are sum(cross-tab gross) == Gross - RefundTotal -
// VouchersIssued (multi-purpose vouchers only) and sum(cross-tab tax) ==
// TaxNet, so an empty cross-tab is fine exactly when both are zero — then
// Box 1 (sum of tax) and Box 6 (sum of gross - tax) are both exactly 0. The
// gross gap alone is not enough: a sale and an equal return at different VAT
// rates net it to zero while VAT is still due. consequence completes "so ..."
// for the caller.
func crossTabProblem(c EODCloseExport, consequence string) string {
	r := c.Report
	gap := r.Gross - r.RefundTotal - r.VouchersIssued
	if len(r.MethodTaxBands) != 0 || (gap == 0 && r.TaxNet == 0) {
		return ""
	}
	return fmt.Sprintf("close Z%d (%s) has turnover of %s (VAT %s) not covered by any VAT cross-tab (method_tax_bands): gross %s, refunds %s, multi-purpose vouchers issued %s — archived before the cross-tab existed, so %s; narrow the range to closes that carry one",
		c.ZNumber, closeLabel(r), pounds(gap), pounds(r.TaxNet), pounds(r.Gross), pounds(r.RefundTotal), pounds(r.VouchersIssued), consequence)
}
