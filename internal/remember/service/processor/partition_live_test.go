package processor

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/observability"
	provider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	remember "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/stretchr/testify/require"
)

// TestRememberPartitionLiveSemanticCohort is opt-in because it uses the pinned real provider.
func TestRememberPartitionLiveSemanticCohort(t *testing.T) {
	if os.Getenv("DENSE_MEM_LIVE_PARTITION") != "1" {
		t.Skip("requires approved live-provider semantic validation")
	}
	cfg := &config.Config{AIVerifierAPIURL: os.Getenv("AI_VERIFIER_API_URL"), AIVerifierAPIKey: os.Getenv("AI_VERIFIER_API_KEY"), AIVerifierModel: "dense-mem-testing", AIVerifierTimeoutSeconds: 160, AIVerifierDisableTemperature: true}
	require.NotEmpty(t, cfg.AIVerifierAPIURL)
	require.NotEmpty(t, cfg.AIVerifierAPIKey)
	protector := observability.NewCredentialProtector(cfg.AIVerifierAPIKey)
	logger := observability.NewWithProtector(slog.LevelDebug, protector)
	httpClient := observability.DiagnosticHTTPClient(&http.Client{Timeout: 160 * time.Second}, logger.ContextSlog(), protector)
	limits := assessor.DefaultSemanticAssessmentLimits()
	limits.ProviderTemperatureDisabled = true
	p := &rememberSynchronousProcessor{provider: provider.NewOpenAIAssessorWithAssessmentLimits(cfg, httpClient, limits), limits: limits, logger: logger, protector: protector}
	cohort := []struct {
		name, text string
		parts      int
	}{
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
	for repetition := 1; repetition <= 3; repetition++ {
		input := remember.RememberProcessRequest{}
		for _, example := range cohort {
			input.Evidence = append(input.Evidence, remember.EvidenceInput{Content: example.text})
		}
		ctx, cancel := context.WithTimeout(remember.WithRememberDeadlines(t.Context(), time.Now()), 180*time.Second)
		output, err := p.partitionRemember(ctx, input, uuid.NewString())
		cancel()
		require.NoError(t, err)

		for index, example := range cohort {
			var texts []string
			for _, item := range output.Evidence {
				if item.Partition.SubmittedIndex == index {
					texts = append(texts, item.Content)
				}
			}
			require.Equal(t, example.text, strings.Join(texts, ""), example.name)
			expected := []string{example.text}
			if example.parts == 2 {
				expected = strings.SplitAfter(example.text, "\n\n")
			}
			require.Equal(t, expected, texts, "repetition %d: %s", repetition, example.name)
		}
	}
}
