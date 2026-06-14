package mapi

import "testing"

// TestAppointmentProptags pins the appointment PidTag date properties to their
// MS-OXPROPS id and type. A wrong id or a non-PtSysTime type would store the
// start/end where no calendar client would read it.
func TestAppointmentProptags(t *testing.T) {
	cases := []struct {
		name   string
		tag    PropTag
		wantID uint16
		wantTy PropType
	}{
		{"StartDate", PrStartDate, 0x0060, PtSysTime},
		{"EndDate", PrEndDate, 0x0061, PtSysTime},
	}
	for _, c := range cases {
		if c.tag.ID() != c.wantID {
			t.Errorf("%s: id 0x%04X, want 0x%04X", c.name, c.tag.ID(), c.wantID)
		}
		if c.tag.Type() != c.wantTy {
			t.Errorf("%s: type 0x%04X, want 0x%04X", c.name, c.tag.Type(), c.wantTy)
		}
	}
}

// TestAppointmentNamedProps pins the calendar named properties to the (GUID, LID)
// pair the store's allocator maps to a property id. These span three namespaces
// — PSETID_Appointment, PSETID_Common (reminders), and PSETID_Meeting (the global
// object id) — so each case carries its own expected GUID. A wrong GUID or LID
// would resolve a different property than the iCalendar converter intends.
func TestAppointmentNamedProps(t *testing.T) {
	// PSETID_Appointment and PSETID_Meeting are distinct GUID families; the latter
	// is a literal, not a member of the {...-C000-...-46} set. Pin both.
	if PsetidAppointment.Data1 != 0x00062002 {
		t.Errorf("PsetidAppointment Data1 0x%08X, want 0x00062002", PsetidAppointment.Data1)
	}
	wantMeeting := GUID{Data1: 0x6ED8DA90, Data2: 0x450B, Data3: 0x101B, Data4: [8]byte{0x98, 0xDA, 0x00, 0xAA, 0x00, 0x3F, 0x13, 0x05}}
	if PsetidMeeting != wantMeeting {
		t.Errorf("PsetidMeeting %v, want %v", PsetidMeeting, wantMeeting)
	}

	cases := []struct {
		name     string
		pn       PropertyName
		wantLID  uint32
		wantGUID GUID
	}{
		{"AppointmentSequence", NameAppointmentSequence, 0x8201, PsetidAppointment},
		{"BusyStatus", NameBusyStatus, 0x8205, PsetidAppointment},
		{"AppointmentLocation", NameAppointmentLocation, 0x8208, PsetidAppointment},
		{"AppointmentStartWhole", NameAppointmentStartWhole, 0x820D, PsetidAppointment},
		{"AppointmentEndWhole", NameAppointmentEndWhole, 0x820E, PsetidAppointment},
		{"AppointmentSubType", NameAppointmentSubType, 0x8215, PsetidAppointment},
		{"ReminderDelta", NameReminderDelta, 0x8501, PsetidCommon},
		{"ReminderSet", NameReminderSet, 0x8503, PsetidCommon},
		{"GlobalObjectId", NameGlobalObjectId, 0x0003, PsetidMeeting},
	}
	for _, c := range cases {
		if c.pn.Kind != MnidID {
			t.Errorf("%s: kind %v, want MnidID", c.name, c.pn.Kind)
		}
		if c.pn.LID != c.wantLID {
			t.Errorf("%s: LID 0x%04X, want 0x%04X", c.name, c.pn.LID, c.wantLID)
		}
		if c.pn.GUID != c.wantGUID {
			t.Errorf("%s: GUID %v, want %v", c.name, c.pn.GUID, c.wantGUID)
		}
	}
}
