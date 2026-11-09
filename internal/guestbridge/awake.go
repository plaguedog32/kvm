package guestbridge

import "context"

type awakeReports struct {
	Reports
	asleep func() bool
}

// WhileAwakeReports guards every KVM write, including ACKs, against implicit
// remote wakeup by the existing wake HID collection. Explicit wake is separate.
func WhileAwakeReports(raw Reports, asleep func() bool) Reports { return &awakeReports{raw, asleep} }
func (r *awakeReports) WriteReport(ctx context.Context, report [ReportSize]byte) error {
	if r.asleep != nil && r.asleep() {
		return ErrAsleep
	}
	return r.Reports.WriteReport(ctx, report)
}
