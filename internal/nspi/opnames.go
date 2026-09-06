package nspi

import "strconv"

// operationNames maps each NSPI opnum to its name, for activity logging.
var operationNames = map[uint16]string{
	opNspiBind:              "Bind",
	opNspiUnbind:            "Unbind",
	opNspiUpdateStat:        "UpdateStat",
	opNspiQueryRows:         "QueryRows",
	opNspiSeekEntries:       "SeekEntries",
	opNspiGetMatches:        "GetMatches",
	opNspiResortRestriction: "ResortRestriction",
	opNspiDNToMId:           "DNToMId",
	opNspiGetPropList:       "GetPropList",
	opNspiGetProps:          "GetProps",
	opNspiCompareMIds:       "CompareMIds",
	opNspiModProps:          "ModProps",
	opNspiGetSpecialTable:   "GetSpecialTable",
	opNspiGetTemplateInfo:   "GetTemplateInfo",
	opNspiModLinkAtt:        "ModLinkAtt",
	opNspiQueryColumns:      "QueryColumns",
	opNspiResolveNames:      "ResolveNames",
	opNspiResolveNamesW:     "ResolveNamesW",
}

// OperationName returns the NSPI operation name for an RPC opnum, so the central
// log shows "ResolveNames" rather than an opaque number. An unrecognized opnum
// yields a numeric fallback, keeping a new or malformed call legible without
// masking it.
func OperationName(opnum uint16) string {
	if name, ok := operationNames[opnum]; ok {
		return name
	}
	return "op" + strconv.Itoa(int(opnum))
}
