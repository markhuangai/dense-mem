package evaluation

type PartitionCase struct {
	Name  string
	Text  string
	Parts int
}

func PartitionCases() []PartitionCase {
	return []PartitionCase{
		{"pronoun", "Mira maintains Atlas. She approves its production releases.", 1},
		{"qualification", "Atlas retains logs for thirty days. This applies only to the paid plan.", 1},
		{"negation", "Do not restart the primary while replication is behind. Restart it only after the replica catches up.", 1},
		{"causal explanation", "Atlas rejected the import. The source checksum did not match, so the import must be regenerated.", 1},
		{"question answer", "Question: Where does Atlas store Evidence?\nAnswer: Atlas stores Evidence in PostgreSQL.", 1},
		{"table header", "Atlas service ports:\n| Service | Port |\n| API | 8080 |\n| Control | 8081 |", 1},
		{"procedure", "To rotate the Atlas key:\n1. Create a replacement key.\n2. Update the clients to use it.\n3. Revoke the old key after verifying those clients.", 1},
		{"paper heading", "QASPER evidence. Paper: Character-Level Question Answering with Attention.\nIn our experiments, the Memory Neural Networks (MemNNs) proposed in babidataset serve as the baselines.", 1},
		{"local acronym", "The Atlas Query Router (AQR) routes client requests to a region. The AQR rejects requests when that region is unavailable.", 1},
		{"independent paragraphs", "Atlas stores durable Evidence in PostgreSQL.\n\nBoreal's office opens at 09:00 on Mondays.", 2},
		{"heading with independent paragraphs", "Field notes:\nAtlas stores durable Evidence in PostgreSQL.\n\nBoreal's office opens at 09:00 on Mondays.", 2},
		{"independent messages", "Mira: My preferred editor is Vim.\n\nNoah: My birthday is June 12.", 2},
	}
}
