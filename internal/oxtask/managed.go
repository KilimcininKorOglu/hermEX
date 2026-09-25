package oxtask

import "hermex/internal/mapi"

// taskTypes is the property type ToProps writes for each entry of taskNames,
// in the same order.
var taskTypes = [...]mapi.PropType{
	idxStatus:          mapi.PtLong,
	idxPercent:         mapi.PtDouble,
	idxStartDate:       mapi.PtSysTime,
	idxDueDate:         mapi.PtSysTime,
	idxDateCompleted:   mapi.PtSysTime,
	idxComplete:        mapi.PtBoolean,
	idxCommonStart:     mapi.PtSysTime,
	idxCommonEnd:       mapi.PtSysTime,
	idxReminderTime:    mapi.PtSysTime,
	idxReminderSet:     mapi.PtBoolean,
	idxKeywords:        mapi.PtMvUnicode,
	idxRecurrenceRule:  mapi.PtUnicode,
	idxOwner:           mapi.PtUnicode,
	idxAssigner:        mapi.PtUnicode,
	idxAcceptanceState: mapi.PtLong,
	idxFCreator:        mapi.PtBoolean,
	idxLastUpdate:      mapi.PtSysTime,
	idxTaskRecurrence:  mapi.PtBinary,
}

// ManagedTags lists every tag ToProps can write. An in-place update deletes the
// ones its new property set leaves out, so a field the editor cleared does not
// keep its old value, while every other property of the task survives. A name
// the store never allocated has no stored value to clear, so it is skipped and
// nothing is allocated.
func ManagedTags(resolve Resolver) ([]mapi.PropTag, error) {
	ids, err := resolve(false, taskNames)
	if err != nil {
		return nil, err
	}
	tags := []mapi.PropTag{mapi.PrMessageClass, mapi.PrSubject, mapi.PrBody, mapi.PrImportance, mapi.PrSensitivity}
	for i, id := range ids {
		if id != 0 {
			tags = append(tags, mapi.MakeTag(id, taskTypes[i]))
		}
	}
	return tags, nil
}
