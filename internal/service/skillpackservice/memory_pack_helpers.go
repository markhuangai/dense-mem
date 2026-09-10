package skillpackservice

import memorypackapp "github.com/markhuangai/dense-mem/internal/memorypack"

func MemoryPackSortedEvidenceIDs(values map[string]MemoryPackEvidence) []string {
	return memorypackapp.MemoryPackSortedEvidenceIDs(values)
}

func MemoryPackCopyMap(value map[string]any) map[string]any {
	return memorypackapp.MemoryPackCopyMap(value)
}

func MemoryPackSupportOmissions(includeSupport bool, canonical []byte) []string {
	return memorypackapp.MemoryPackSupportOmissions(includeSupport, canonical)
}
