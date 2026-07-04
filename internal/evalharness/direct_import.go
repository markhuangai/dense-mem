package evalharness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/service/fragmentcodec"
	"github.com/markhuangai/dense-mem/internal/service/fragmentidentity"
	neo4jstore "github.com/markhuangai/dense-mem/internal/storage/neo4j"
)

const (
	defaultDirectImportBatch    = 32
	defaultDirectImportProgress = 10000
)

type DirectImportOptions struct {
	TeamID          string
	BatchSize       int
	Concurrency     int
	ProgressEvery   int
	Neo4jURI        string
	Neo4jUser       string
	Neo4jPassword   string
	Neo4jDatabase   string
	KeepSourceDocID map[string]struct{}
}

type directImportRow struct {
	SourceDocID string
	FragmentID  string
}

type directImportExistingRow struct {
	IdempotencyKey string
	SourceDocID    string
	FragmentID     string
	Keep           bool
}

func DirectImportCorpusFile(ctx context.Context, path string, opts DirectImportOptions) (KnowledgeMapping, int, error) {
	opts.TeamID = strings.TrimSpace(opts.TeamID)
	if opts.TeamID == "" {
		return newKnowledgeMapping(), 0, fmt.Errorf("direct import requires team id")
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = defaultDirectImportBatch
	}
	if opts.ProgressEvery <= 0 {
		opts.ProgressEvery = defaultDirectImportProgress
	}

	cfg := loadDirectImportConfig(opts)
	if !cfg.IsEmbeddingConfigured() {
		return newKnowledgeMapping(), 0, fmt.Errorf("direct import requires configured embeddings")
	}

	client, err := neo4jstore.NewClient(ctx, &cfg)
	if err != nil {
		return newKnowledgeMapping(), 0, err
	}
	defer client.Close(context.Background())

	baseEmbedder := embedding.NewOpenAIEmbeddingProvider(&cfg, nil)
	embedder := embedding.NewRetryEmbeddingProviderWithKeyAndOptions(baseEmbedder, nil, cfg.GetAIAPIKey(), embedding.RetryEmbeddingOptions{
		MaxRetries: directImportEnvInt("DENSE_MEM_EVAL_EMBEDDING_MAX_RETRIES", 20),
		BaseDelay:  200 * time.Millisecond,
		MaxDelay:   60 * time.Second,
	})

	if opts.Concurrency <= 1 {
		return directImportCorpusFileSequential(ctx, client, embedder, path, opts)
	}
	return directImportCorpusFileConcurrent(ctx, client, embedder, path, opts)
}

func directImportCorpusFileSequential(
	ctx context.Context,
	client *neo4jstore.Neo4jClient,
	embedder embedding.EmbeddingProviderInterface,
	path string,
	opts DirectImportOptions,
) (KnowledgeMapping, int, error) {
	mapping := newKnowledgeMapping()
	batch := make([]CorpusItem, 0, opts.BatchSize)
	count := 0
	nextProgress := opts.ProgressEvery
	err := scanCorpusFile(path, func(item CorpusItem) error {
		if len(item.Claims) > 0 {
			return fmt.Errorf("direct import supports fragment-only rows; %s has typed claims", item.SourceDocID)
		}
		batch = append(batch, item)
		if len(batch) < opts.BatchSize {
			return nil
		}
		imported, err := directImportBatch(ctx, client, embedder, opts.TeamID, opts.KeepSourceDocID, batch)
		if err != nil {
			return err
		}
		for _, row := range imported {
			addSourceMapping(&mapping, Ref{Type: "fragment", ID: row.FragmentID, SourceDocID: row.SourceDocID}, true)
		}
		count += len(batch)
		if count >= nextProgress {
			fmt.Fprintf(os.Stderr, "direct import: imported %d rows\n", count)
			nextProgress += opts.ProgressEvery
		}
		batch = batch[:0]
		return nil
	})
	if err != nil {
		return mapping, count, err
	}
	if len(batch) > 0 {
		imported, err := directImportBatch(ctx, client, embedder, opts.TeamID, opts.KeepSourceDocID, batch)
		if err != nil {
			return mapping, count, err
		}
		for _, row := range imported {
			addSourceMapping(&mapping, Ref{Type: "fragment", ID: row.FragmentID, SourceDocID: row.SourceDocID}, true)
		}
		count += len(batch)
	}
	fmt.Fprintf(os.Stderr, "direct import: imported %d rows\n", count)
	return mapping, count, nil
}

func directImportCorpusFileConcurrent(
	ctx context.Context,
	client *neo4jstore.Neo4jClient,
	embedder embedding.EmbeddingProviderInterface,
	path string,
	opts DirectImportOptions,
) (KnowledgeMapping, int, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type result struct {
		imported []directImportRow
		count    int
		err      error
	}

	jobs := make(chan []CorpusItem)
	results := make(chan result)
	var wg sync.WaitGroup
	for i := 0; i < opts.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for batch := range jobs {
				imported, err := directImportBatch(ctx, client, embedder, opts.TeamID, opts.KeepSourceDocID, batch)
				select {
				case results <- result{imported: imported, count: len(batch), err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	var scanErr error
	go func() {
		defer close(jobs)
		batch := make([]CorpusItem, 0, opts.BatchSize)
		scanErr = scanCorpusFile(path, func(item CorpusItem) error {
			if len(item.Claims) > 0 {
				return fmt.Errorf("direct import supports fragment-only rows; %s has typed claims", item.SourceDocID)
			}
			batch = append(batch, item)
			if len(batch) < opts.BatchSize {
				return nil
			}
			if err := sendDirectImportBatch(ctx, jobs, batch); err != nil {
				return err
			}
			batch = make([]CorpusItem, 0, opts.BatchSize)
			return nil
		})
		if scanErr == nil && len(batch) > 0 {
			scanErr = sendDirectImportBatch(ctx, jobs, batch)
		}
		if scanErr != nil {
			cancel()
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	mapping := newKnowledgeMapping()
	count := 0
	nextProgress := opts.ProgressEvery
	var firstErr error
	for result := range results {
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
				cancel()
			}
			continue
		}
		for _, row := range result.imported {
			addSourceMapping(&mapping, Ref{Type: "fragment", ID: row.FragmentID, SourceDocID: row.SourceDocID}, true)
		}
		count += result.count
		if count >= nextProgress {
			fmt.Fprintf(os.Stderr, "direct import: imported %d rows\n", count)
			nextProgress += opts.ProgressEvery
		}
	}
	if firstErr != nil {
		return mapping, count, firstErr
	}
	if scanErr != nil {
		return mapping, count, scanErr
	}
	if err := ctx.Err(); err != nil {
		return mapping, count, err
	}
	fmt.Fprintf(os.Stderr, "direct import: imported %d rows\n", count)
	return mapping, count, nil
}

func sendDirectImportBatch(ctx context.Context, jobs chan<- []CorpusItem, batch []CorpusItem) error {
	batchCopy := append([]CorpusItem(nil), batch...)
	select {
	case jobs <- batchCopy:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func overrideDirectImportNeo4jConfig(cfg *config.Config, opts DirectImportOptions) {
	if strings.TrimSpace(opts.Neo4jURI) != "" {
		cfg.Neo4jURI = strings.TrimSpace(opts.Neo4jURI)
	}
	if strings.TrimSpace(opts.Neo4jUser) != "" {
		cfg.Neo4jUser = strings.TrimSpace(opts.Neo4jUser)
	}
	if strings.TrimSpace(opts.Neo4jPassword) != "" {
		cfg.Neo4jPassword = strings.TrimSpace(opts.Neo4jPassword)
	}
	if strings.TrimSpace(opts.Neo4jDatabase) != "" {
		cfg.Neo4jDatabase = strings.TrimSpace(opts.Neo4jDatabase)
	}
}

func loadDirectImportConfig(opts DirectImportOptions) config.Config {
	timeout := directImportEnvInt("AI_API_EMBEDDING_TIMEOUT_SECONDS", 30)
	dimensions := directImportEnvInt("AI_API_EMBEDDING_DIMENSIONS", 0)
	cfg := config.Config{
		Neo4jURI:                  os.Getenv("NEO4J_URI"),
		Neo4jUser:                 os.Getenv("NEO4J_USER"),
		Neo4jPassword:             os.Getenv("NEO4J_PASSWORD"),
		Neo4jDatabase:             os.Getenv("NEO4J_DATABASE"),
		AIAPIURL:                  os.Getenv("AI_API_URL"),
		AIAPIKey:                  os.Getenv("AI_API_KEY"),
		AIEmbeddingModel:          os.Getenv("AI_API_EMBEDDING_MODEL"),
		AIEmbeddingDimensions:     dimensions,
		AIEmbeddingTimeoutSeconds: timeout,
		EmbeddingDimensions:       dimensions,
	}
	overrideDirectImportNeo4jConfig(&cfg, opts)
	return cfg
}

func directImportEnvInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func directImportBatch(
	ctx context.Context,
	client *neo4jstore.Neo4jClient,
	embedder embedding.EmbeddingProviderInterface,
	teamID string,
	keepSourceDocIDs map[string]struct{},
	items []CorpusItem,
) ([]directImportRow, error) {
	existingRows, existingKeys, err := directImportExistingRows(ctx, client, teamID, keepSourceDocIDs, items)
	if err != nil {
		return nil, err
	}
	imported := make([]directImportRow, 0, len(items))
	for _, row := range existingRows {
		if row.Keep {
			imported = append(imported, directImportRow{SourceDocID: row.SourceDocID, FragmentID: row.FragmentID})
		}
	}

	missing := make([]CorpusItem, 0, len(items)-len(existingKeys))
	for _, item := range items {
		if _, ok := existingKeys[directImportIdempotencyKey(item.SourceDocID)]; ok {
			continue
		}
		missing = append(missing, item)
	}
	if len(missing) == 0 {
		return imported, nil
	}

	texts := make([]string, len(missing))
	for i, item := range missing {
		texts[i] = item.Content
	}
	vectors, model, err := embedder.EmbedBatch(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("embed batch: %w", err)
	}
	if len(vectors) != len(missing) {
		return nil, fmt.Errorf("embed batch returned %d vectors for %d rows", len(vectors), len(missing))
	}

	now := time.Now().UTC()
	rows := make([]map[string]any, 0, len(missing))
	for i, item := range missing {
		metadataJSON, err := fragmentcodec.EncodeOptionalMap(seedMetadata(item))
		if err != nil {
			return nil, fmt.Errorf("encode metadata for %s: %w", item.SourceDocID, err)
		}
		classificationJSON, err := fragmentcodec.EncodeOptionalMap(nil)
		if err != nil {
			return nil, fmt.Errorf("encode classification for %s: %w", item.SourceDocID, err)
		}
		rows = append(rows, map[string]any{
			"team_id":                 teamID,
			"source_doc_id":           item.SourceDocID,
			"fragment_id":             deterministicEvalFragmentID(item.SourceDocID),
			"content":                 item.Content,
			"content_hash":            fragmentidentity.ContentHash(item.Content).Hex,
			"idempotency_key":         directImportIdempotencyKey(item.SourceDocID),
			"source":                  firstNonEmpty(item.SourceDataset, item.Title, "eval-seed"),
			"source_type":             firstNonEmpty(item.SourceType, string(domain.SourceTypeManual)),
			"authority":               firstNonEmpty(item.Authority, string(domain.AuthorityUnknown)),
			"labels":                  item.Labels,
			"metadata_json":           metadataJSON,
			"embedding":               vectors[i],
			"embedding_model":         model,
			"embedding_dimensions":    len(vectors[i]),
			"source_quality":          item.SourceQuality,
			"classification_json":     classificationJSON,
			"owner_profile_id":        teamID,
			"owner_profile_name":      "",
			"created_by_profile_id":   teamID,
			"created_by_profile_name": "",
			"created_at":              now,
			"updated_at":              now,
			"status":                  string(domain.FragmentStatusActive),
			"keep":                    directImportKeepsSourceDocID(keepSourceDocIDs, item.SourceDocID),
		})
	}

	created, err := client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		result, err := tx.Run(ctx, directImportCypher, map[string]any{"rows": rows})
		if err != nil {
			return nil, err
		}
		imported := []directImportRow{}
		for result.Next(ctx) {
			record := result.Record()
			sourceDocID, _ := record.Get("source_doc_id")
			fragmentID, _ := record.Get("fragment_id")
			imported = append(imported, directImportRow{
				SourceDocID: stringValue(sourceDocID),
				FragmentID:  stringValue(fragmentID),
			})
		}
		if err := result.Err(); err != nil {
			return nil, err
		}
		if _, err := result.Consume(ctx); err != nil {
			return nil, err
		}
		return imported, nil
	})
	if err != nil {
		return nil, fmt.Errorf("write batch: %w", err)
	}
	imported = append(imported, created.([]directImportRow)...)
	return imported, nil
}

const directImportCypher = `
UNWIND $rows AS row
MERGE (sf:SourceFragment {team_id: row.team_id, idempotency_key: row.idempotency_key})
ON CREATE SET
	sf.fragment_id = row.fragment_id,
	sf.content = row.content,
	sf.content_hash = row.content_hash,
	sf.source = row.source,
	sf.source_type = row.source_type,
	sf.authority = row.authority,
	sf.labels = row.labels,
	sf.metadata_json = row.metadata_json,
	sf.embedding = row.embedding,
	sf.embedding_model = row.embedding_model,
	sf.embedding_dimensions = row.embedding_dimensions,
	sf.source_quality = row.source_quality,
	sf.classification_json = row.classification_json,
	sf.owner_profile_id = row.owner_profile_id,
	sf.owner_profile_name = row.owner_profile_name,
	sf.created_by_profile_id = row.created_by_profile_id,
	sf.created_by_profile_name = row.created_by_profile_name,
	sf.created_at = row.created_at,
	sf.updated_at = row.updated_at,
	sf.status = row.status
WITH row, sf
WHERE row.keep
RETURN row.source_doc_id AS source_doc_id, sf.fragment_id AS fragment_id
`

func directImportExistingRows(
	ctx context.Context,
	client *neo4jstore.Neo4jClient,
	teamID string,
	keepSourceDocIDs map[string]struct{},
	items []CorpusItem,
) ([]directImportExistingRow, map[string]struct{}, error) {
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		rows = append(rows, map[string]any{
			"source_doc_id":   item.SourceDocID,
			"idempotency_key": directImportIdempotencyKey(item.SourceDocID),
			"keep":            directImportKeepsSourceDocID(keepSourceDocIDs, item.SourceDocID),
		})
	}
	existing, err := client.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		result, err := tx.Run(ctx, directImportExistingCypher, map[string]any{"team_id": teamID, "rows": rows})
		if err != nil {
			return nil, err
		}
		out := []directImportExistingRow{}
		for result.Next(ctx) {
			record := result.Record()
			idempotencyKey, _ := record.Get("idempotency_key")
			sourceDocID, _ := record.Get("source_doc_id")
			fragmentID, _ := record.Get("fragment_id")
			keep, _ := record.Get("keep")
			out = append(out, directImportExistingRow{
				IdempotencyKey: stringValue(idempotencyKey),
				SourceDocID:    stringValue(sourceDocID),
				FragmentID:     stringValue(fragmentID),
				Keep:           boolValue(keep),
			})
		}
		if err := result.Err(); err != nil {
			return nil, err
		}
		if _, err := result.Consume(ctx); err != nil {
			return nil, err
		}
		return out, nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("read existing batch: %w", err)
	}
	existingRows := existing.([]directImportExistingRow)
	existingKeys := make(map[string]struct{}, len(existingRows))
	for _, row := range existingRows {
		if row.IdempotencyKey != "" {
			existingKeys[row.IdempotencyKey] = struct{}{}
		}
	}
	return existingRows, existingKeys, nil
}

const directImportExistingCypher = `
UNWIND $rows AS row
MATCH (sf:SourceFragment {team_id: $team_id, idempotency_key: row.idempotency_key})
RETURN row.idempotency_key AS idempotency_key,
	row.source_doc_id AS source_doc_id,
	sf.fragment_id AS fragment_id,
	row.keep AS keep
`

func deterministicEvalFragmentID(sourceDocID string) string {
	sum := sha256.Sum256([]byte("dense-mem-eval-fragment:" + sourceDocID))
	return "eval-" + hex.EncodeToString(sum[:16])
}

func directImportIdempotencyKey(sourceDocID string) string {
	return "eval:" + sourceDocID
}

func directImportKeepsSourceDocID(keepSourceDocIDs map[string]struct{}, sourceDocID string) bool {
	if len(keepSourceDocIDs) == 0 {
		return true
	}
	_, keep := keepSourceDocIDs[sourceDocID]
	return keep
}

func boolValue(value any) bool {
	v, ok := value.(bool)
	return ok && v
}
