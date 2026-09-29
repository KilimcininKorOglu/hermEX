package ews

import "hermex/internal/mapi"

// fieldPath is the MAPI property, or the properties, an EWS FieldURI names in a
// restriction ([MS-OXWSSRCH] 2.2.4.2). A field that stands for one person (the
// sender, the organizer) names each property that carries part of it, and a test
// of the field is a test of any of them. A field kept as a named property carries
// its name and type instead of a tag.
type fieldPath struct {
	tags  []mapi.PropTag
	named *mapi.PropertyName
	typ   mapi.PropType
}

// namedField is a fieldPath kept as a named property.
func namedField(name mapi.PropertyName, typ mapi.PropType) fieldPath {
	return fieldPath{named: &name, typ: typ}
}

// tagField is a fieldPath kept under one or more tags.
func tagField(tags ...mapi.PropTag) fieldPath {
	return fieldPath{tags: tags}
}

// fieldPaths are the FieldURIs a restriction may test. A FieldURI outside this set
// is answered ErrorUnsupportedPathForQuery rather than ignored, because a filter
// dropped in silence returns items the client asked to leave out.
var fieldPaths = map[string]fieldPath{
	"item:Subject":                       tagField(mapi.PrSubject),
	"item:Body":                          tagField(mapi.PrBody),
	"item:ItemClass":                     tagField(mapi.PrMessageClass),
	"item:DateTimeReceived":              tagField(mapi.PrMessageDeliveryTime),
	"item:DateTimeSent":                  tagField(mapi.PrClientSubmitTime),
	"item:DateTimeCreated":               tagField(mapi.PrCreationTime),
	"item:LastModifiedTime":              tagField(mapi.PrLastModificationTime),
	"item:Importance":                    tagField(mapi.PrImportance),
	"item:Sensitivity":                   tagField(mapi.PrSensitivity),
	"item:Size":                          tagField(mapi.PrMessageSize),
	"item:HasAttachments":                tagField(mapi.PrHasAttachments),
	"item:DisplayTo":                     tagField(mapi.PrDisplayTo),
	"item:DisplayCc":                     tagField(mapi.PrDisplayCc),
	"item:InReplyTo":                     tagField(mapi.PrInReplyToID),
	"item:IsAssociated":                  tagField(mapi.PrAssociated),
	"item:ConversationId":                tagField(mapi.PrConversationId),
	"item:InternetMessageHeaders":        tagField(mapi.PrTransportMessageHeaders),
	"item:Categories":                    namedField(mapi.NameKeywords, mapi.PtMvUnicode),
	"item:ReminderIsSet":                 namedField(mapi.NameReminderSet, mapi.PtBoolean),
	"item:ReminderDueBy":                 namedField(mapi.NameReminderTime, mapi.PtSysTime),
	"item:ReminderMinutesBeforeStart":    namedField(mapi.NameReminderDelta, mapi.PtLong),
	"message:IsRead":                     tagField(mapi.PrRead),
	"message:InternetMessageId":          tagField(mapi.PrInternetMessageID),
	"message:References":                 tagField(mapi.PrInternetReferences),
	"message:ConversationTopic":          tagField(mapi.PrConversationTopic),
	"message:ConversationIndex":          tagField(mapi.PrConversationIndex),
	"message:IsReadReceiptRequested":     tagField(mapi.PrReadReceiptRequested),
	"message:IsDeliveryReceiptRequested": tagField(mapi.PrOriginatorDeliveryReportRequested),
	"message:IsResponseRequested":        tagField(mapi.PrResponseRequested),
	"message:From":                       tagField(mapi.PrSentRepresentingName, mapi.PrSentRepresentingEmailAddress, mapi.PrSentRepresentingSmtpAddress),
	"message:Sender":                     tagField(mapi.PrSenderName, mapi.PrSenderEmailAddress, mapi.PrSenderSmtpAddress),
	"calendar:Start":                     namedField(mapi.NameAppointmentStartWhole, mapi.PtSysTime),
	"calendar:End":                       namedField(mapi.NameAppointmentEndWhole, mapi.PtSysTime),
	"calendar:Location":                  namedField(mapi.NameAppointmentLocation, mapi.PtUnicode),
	"calendar:IsAllDayEvent":             namedField(mapi.NameAppointmentSubType, mapi.PtBoolean),
	"calendar:IsRecurring":               namedField(mapi.NameRecurring, mapi.PtBoolean),
	"calendar:LegacyFreeBusyStatus":      namedField(mapi.NameBusyStatus, mapi.PtLong),
	"calendar:MyResponseType":            namedField(mapi.NameResponseStatus, mapi.PtLong),
	"calendar:Organizer":                 tagField(mapi.PrSenderName, mapi.PrSenderEmailAddress, mapi.PrSenderSmtpAddress),
	"task:DueDate":                       namedField(mapi.NameTaskDueDate, mapi.PtSysTime),
	"task:StartDate":                     namedField(mapi.NameTaskStartDate, mapi.PtSysTime),
	"task:CompleteDate":                  namedField(mapi.NameTaskDateCompleted, mapi.PtSysTime),
	"task:IsComplete":                    namedField(mapi.NameTaskComplete, mapi.PtBoolean),
	"task:Status":                        namedField(mapi.NameTaskStatus, mapi.PtLong),
	"task:PercentComplete":               namedField(mapi.NamePercentComplete, mapi.PtDouble),
	"task:Owner":                         namedField(mapi.NameTaskOwner, mapi.PtUnicode),
	"contacts:DisplayName":               tagField(mapi.PrDisplayName),
	"contacts:FileAs":                    namedField(mapi.NameFileAs, mapi.PtUnicode),
	"contacts:GivenName":                 tagField(mapi.PrGivenName),
	"contacts:Surname":                   tagField(mapi.PrSurname),
	"contacts:MiddleName":                tagField(mapi.PrMiddleName),
	"contacts:Initials":                  tagField(mapi.PrInitials),
	"contacts:Generation":                tagField(mapi.PrGeneration),
	"contacts:Nickname":                  tagField(mapi.PrNickname),
	"contacts:CompanyName":               tagField(mapi.PrCompanyName),
	"contacts:Department":                tagField(mapi.PrDepartmentName),
	"contacts:JobTitle":                  tagField(mapi.PrTitle),
	"contacts:Manager":                   tagField(mapi.PrManagerName),
	"contacts:AssistantName":             tagField(mapi.PrAssistant),
	"contacts:OfficeLocation":            tagField(mapi.PrOfficeLocation),
	"contacts:Profession":                tagField(mapi.PrProfession),
	"contacts:SpouseName":                tagField(mapi.PrSpouseName),
	"contacts:Birthday":                  tagField(mapi.PrBirthday),
	"contacts:WeddingAnniversary":        tagField(mapi.PrWeddingAnniversary),
	"contacts:BusinessHomePage":          tagField(mapi.PrBusinessHomePage),
	"folder:DisplayName":                 tagField(mapi.PrDisplayName),
	"folder:FolderClass":                 tagField(mapi.PrContainerClass),
	"folder:TotalCount":                  tagField(mapi.PrContentCount),
	"folder:UnreadCount":                 tagField(mapi.PrContentUnreadCount),
}

// enumFields are the fields whose EWS value is a name, and the value each name
// stands for in the property ([MS-OXWSCDATA] ImportanceChoicesType,
// SensitivityChoicesType, LegacyFreeBusyType, TaskStatusType, ResponseTypeType).
var enumFields = map[string]map[string]any{
	"item:Importance":  {"Low": int32(0), "Normal": int32(1), "High": int32(2)},
	"item:Sensitivity": {"Normal": int32(0), "Personal": int32(1), "Private": int32(2), "Confidential": int32(3)},
	"calendar:LegacyFreeBusyStatus": {
		"Free": int32(0), "Tentative": int32(1), "Busy": int32(2), "OOF": int32(3), "WorkingElsewhere": int32(4),
	},
	"task:Status": {
		"NotStarted": int32(0), "InProgress": int32(1), "Completed": int32(2), "WaitingOnOthers": int32(3), "Deferred": int32(4),
	},
	"calendar:MyResponseType": {
		"Unknown": int32(0), "Organizer": int32(1), "Tentative": int32(2), "Accept": int32(3), "Decline": int32(4), "NoResponseReceived": int32(5),
	},
}
