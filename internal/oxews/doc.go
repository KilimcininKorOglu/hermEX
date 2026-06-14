// Package oxews converts between the MAPI object model (folders and
// oxcmail.Message items) and the EWS XML element types (MS-OXWS), mirroring the
// oxvcard and oxcical converters. The ews protocol package handles SOAP routing,
// response shaping, and store access; this package owns the <t:Folder> and
// <t:Message> element serialization and the opaque item/folder id encoding.
package oxews
