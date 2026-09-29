package mapi

// Search folder criteria flags, the SearchFlags a client passes to
// RopSetSearchCriteria ([MS-OXCFOLD] 2.2.1.4.1).
const (
	SearchStop              uint32 = 0x00000001 // STOP_SEARCH
	SearchRestart           uint32 = 0x00000002 // RESTART_SEARCH
	SearchRecursive         uint32 = 0x00000004 // RECURSIVE_SEARCH
	SearchShallow           uint32 = 0x00000008 // SHALLOW_SEARCH
	SearchContentIndexed    uint32 = 0x00010000 // CONTENT_INDEXED_SEARCH
	SearchNonContentIndexed uint32 = 0x00020000 // NON_CONTENT_INDEXED_SEARCH
	SearchStatic            uint32 = 0x00040000 // STATIC_SEARCH
)

// Search folder state flags, the SearchFlags RopGetSearchCriteria returns
// ([MS-OXCFOLD] 2.2.1.5.2).
const (
	SearchStateRunning   uint32 = 0x00000001 // SEARCH_RUNNING
	SearchStateRebuild   uint32 = 0x00000002 // SEARCH_REBUILD
	SearchStateRecursive uint32 = 0x00000004 // SEARCH_RECURSIVE
	SearchStateComplete  uint32 = 0x00001000 // SEARCH_COMPLETE
	SearchStateStatic    uint32 = 0x00010000 // SEARCH_STATIC
)
