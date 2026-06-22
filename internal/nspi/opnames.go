package nspi

import "strconv"

// OperationName returns the NSPI operation name for an RPC opnum, for activity
// logging (so the central log shows "ResolveNames" rather than an opaque number).
// An unrecognized opnum yields a numeric fallback, keeping a new or malformed call
// legible without masking it.
func OperationName(opnum uint16) string {
	switch opnum {
	case opNspiBind:
		return "Bind"
	case opNspiUnbind:
		return "Unbind"
	case opNspiUpdateStat:
		return "UpdateStat"
	case opNspiQueryRows:
		return "QueryRows"
	case opNspiSeekEntries:
		return "SeekEntries"
	case opNspiGetMatches:
		return "GetMatches"
	case opNspiResortRestriction:
		return "ResortRestriction"
	case opNspiDNToMId:
		return "DNToMId"
	case opNspiGetPropList:
		return "GetPropList"
	case opNspiGetProps:
		return "GetProps"
	case opNspiCompareMIds:
		return "CompareMIds"
	case opNspiModProps:
		return "ModProps"
	case opNspiGetSpecialTable:
		return "GetSpecialTable"
	case opNspiGetTemplateInfo:
		return "GetTemplateInfo"
	case opNspiModLinkAtt:
		return "ModLinkAtt"
	case opNspiQueryColumns:
		return "QueryColumns"
	case opNspiResolveNames:
		return "ResolveNames"
	case opNspiResolveNamesW:
		return "ResolveNamesW"
	default:
		return "op" + strconv.Itoa(int(opnum))
	}
}
