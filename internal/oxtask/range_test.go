package oxtask

import (
	"testing"

	"hermex/internal/mapi"
)

// TestToPropsStoresOnlyTheDefinedValues feeds the task model values that fit a
// MAPI long but name nothing. The ranges come from the specifications every
// client reads them by: PidTagImportance 0-2 and PidTagSensitivity 0-3
// ([MS-OXCMSG] 2.2.1.11, 2.2.1.13), PidLidTaskStatus 0-4 and
// PidLidPercentComplete 0.0-1.0 ([MS-OXOTASK] 2.2.2.2.2, 2.2.2.2.3). The model is
// filled from client input (the webmail task form passes its priority and
// status through), so a value outside these would be stored for Outlook and
// every other protocol to read as a level that does not exist.
func TestToPropsStoresOnlyTheDefinedValues(t *testing.T) {
	r := newFakeResolver()
	task := New()
	task.Importance = 3
	task.Sensitivity = 4
	task.Status = 5
	task.PercentComplete = 1.5
	props, err := ToProps(task, r.resolve)
	mustNoErr(t, err, "ToProps")

	if v, ok := props.Get(mapi.PrImportance); ok {
		t.Errorf("importance 3 was stored as %v", v)
	}
	if v, ok := props.Get(mapi.PrSensitivity); ok {
		t.Errorf("sensitivity 4 was stored as %v", v)
	}
	statusTag := mapi.MakeTag(r.ids[mapi.NameTaskStatus], mapi.PtLong)
	percentTag := mapi.MakeTag(r.ids[mapi.NamePercentComplete], mapi.PtDouble)
	wantEq(t, mustGet(t, props, statusTag, "status"), any(int32(0)), "the status stored for an undefined status on an open task")
	wantEq(t, mustGet(t, props, percentTag, "percent"), any(0.0), "the percentage stored for an undefined percentage on an open task")

	// The top of each range is still stored as itself.
	task = New()
	task.Importance, task.Sensitivity, task.Status, task.PercentComplete = 2, 3, 4, 1.0
	props, err = ToProps(task, r.resolve)
	mustNoErr(t, err, "ToProps")
	wantEq(t, mustGet(t, props, mapi.PrImportance, "importance"), any(int32(2)), "importance 2 (high)")
	wantEq(t, mustGet(t, props, mapi.PrSensitivity, "sensitivity"), any(int32(3)), "sensitivity 3 (confidential)")
	wantEq(t, mustGet(t, props, statusTag, "status"), any(int32(4)), "status 4 (deferred)")
	wantEq(t, mustGet(t, props, percentTag, "percent"), any(1.0), "percentage 1.0")
	// Below each range is as undefined as above it.
	task = New()
	task.Status, task.PercentComplete = -1, -0.5
	props, err = ToProps(task, r.resolve)
	mustNoErr(t, err, "ToProps")
	wantEq(t, mustGet(t, props, statusTag, "status"), any(int32(0)), "the status stored for a negative status")
	wantEq(t, mustGet(t, props, percentTag, "percent"), any(0.0), "the percentage stored for a negative percentage")
}
