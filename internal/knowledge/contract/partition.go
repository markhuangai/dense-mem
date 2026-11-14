package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"
)

const MaxRememberItemRunes = 16384
const MaxRememberPartitions = 100
const RememberPartitionPolicyVersion = "remember.semantic-partition.v1"

type RememberSubmittedItem struct {
	ID            string
	Index         int
	Content       string
	ContentHash   string
	PolicyVersion string
}

type EvidencePartition struct {
	SourceItemID   string `json:"source_item_id"`
	SubmittedIndex int    `json:"submitted_index"`
	Index          int    `json:"partition_index"`
	Start          int    `json:"span_start"`
	End            int    `json:"span_end"`
}

func EvidenceContentHash(content string) string {
	digest := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(digest[:])
}

// ValidateRememberPartitions is shared by preparation and commit so durable spans cannot diverge from admitted source text.
func ValidateRememberPartitions(items []RememberSubmittedItem, evidence []EvidenceInput) error {
	if len(items) == 0 {
		for _, e := range evidence {
			if e.Partition != nil {
				return fmt.Errorf("partition source items are missing")
			}
		}
		return nil
	}
	if len(items) > 20 || len(evidence) > MaxRememberPartitions {
		return fmt.Errorf("partition count exceeds server bound")
	}
	starts, counts := make([]int, len(items)), make([]int, len(items))
	seen := map[string]bool{}
	for i, item := range items {
		if item.Index != i || item.ID == "" || seen[item.ID] || !utf8.ValidString(item.Content) || utf8.RuneCountInString(item.Content) > MaxRememberItemRunes || strings.TrimSpace(item.Content) == "" || item.ContentHash != EvidenceContentHash(item.Content) || item.PolicyVersion != RememberPartitionPolicyVersion {
			return fmt.Errorf("submitted item %d is invalid", i)
		}
		seen[item.ID] = true
	}
	previous := 0
	originals := make(map[int]EvidenceInput)
	for _, e := range evidence {
		p := e.Partition
		if p == nil || p.SubmittedIndex < previous || p.SubmittedIndex < 0 || p.SubmittedIndex >= len(items) {
			return fmt.Errorf("partition source order is invalid")
		}
		item := items[p.SubmittedIndex]
		intent := e
		intent.Content, intent.ContentHash, intent.FragmentID, intent.IdempotencyKey, intent.Partition = "", "", "", "", nil
		if original, ok := originals[p.SubmittedIndex]; ok && !reflect.DeepEqual(original, intent) {
			return fmt.Errorf("partition siblings have different source or lifecycle intent")
		}
		originals[p.SubmittedIndex] = intent
		runes := []rune(item.Content)
		if p.SourceItemID != item.ID || p.Index != counts[p.SubmittedIndex] || p.Start != starts[p.SubmittedIndex] || p.End <= p.Start || p.End > len(runes) {
			return fmt.Errorf("partition source span is invalid")
		}
		if string(runes[p.Start:p.End]) != e.Content || strings.TrimSpace(e.Content) == "" || e.ContentHash != EvidenceContentHash(e.Content) {
			return fmt.Errorf("partition does not reconstruct exact source")
		}
		starts[p.SubmittedIndex], counts[p.SubmittedIndex], previous = p.End, counts[p.SubmittedIndex]+1, p.SubmittedIndex
	}
	for i, item := range items {
		if starts[i] != utf8.RuneCountInString(item.Content) || counts[i] == 0 {
			return fmt.Errorf("submitted item %d is not completely partitioned", i)
		}
	}
	return nil
}
