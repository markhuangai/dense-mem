package evalharness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

type SourceFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Rows   int    `json:"rows"`
}

type Dataset struct {
	ID        string     `json:"id"`
	Adapter   string     `json:"adapter"`
	Corpus    SourceFile `json:"corpus"`
	Questions SourceFile `json:"questions"`
}

type Registry struct {
	Version  int       `json:"version"`
	Revision string    `json:"revision"`
	BaseURL  string    `json:"base_url"`
	License  string    `json:"license"`
	Datasets []Dataset `json:"datasets"`
}

func ReadRegistry(path string) (Registry, error) {
	var registry Registry
	err := readJSON(path, &registry)
	if err != nil {
		return registry, err
	}
	if registry.Version != 1 || registry.Revision != "5ec05b38deecc3318bb432c69865959c56058990" || len(registry.Datasets) == 0 {
		return registry, errors.New("invalid dataset registry identity")
	}
	seen := map[string]bool{}
	for _, dataset := range registry.Datasets {
		if seen[dataset.ID] || dataset.ID == "" {
			return registry, errors.New("duplicate or empty dataset")
		}
		seen[dataset.ID] = true
		if dataset.Adapter != dataset.ID {
			return registry, errors.New("unsupported dataset adapter")
		}
		for _, source := range []SourceFile{dataset.Corpus, dataset.Questions} {
			if source.Rows <= 0 || len(source.SHA256) != 64 || strings.ContainsAny(source.Path, "/\\") {
				return registry, errors.New("invalid dataset source lock")
			}
			if _, err := hex.DecodeString(source.SHA256); err != nil {
				return registry, errors.New("invalid source digest")
			}
		}
	}
	return registry, nil
}

func (r Registry) Dataset(id string) (Dataset, error) {
	for _, dataset := range r.Datasets {
		if dataset.ID == id {
			return dataset, nil
		}
	}
	return Dataset{}, errors.New("dataset is not registered")
}

func FileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return bytesHash(data), nil
}

func bytesHash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func LoadDataset(dataset Dataset, directory string) ([]CorpusItem, []Question, error) {
	var rows []struct {
		Index json.RawMessage `json:"idx"`
		Title string          `json:"title"`
		Text  string          `json:"text"`
	}
	var questions []map[string]json.RawMessage
	for i, source := range []SourceFile{dataset.Corpus, dataset.Questions} {
		path := filepath.Join(directory, source.Path)
		hash, err := FileHash(path)
		if err != nil {
			return nil, nil, err
		}
		if hash != source.SHA256 {
			return nil, nil, errors.New("public source digest mismatch")
		}
		if i == 0 {
			if err = readJSON(path, &rows); err != nil {
				return nil, nil, err
			}
			if len(rows) != source.Rows {
				return nil, nil, errors.New("corpus row count mismatch")
			}
		} else {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, nil, err
			}
			if err = json.Unmarshal(data, &questions); err != nil {
				return nil, nil, err
			}
			if len(questions) != source.Rows {
				return nil, nil, errors.New("question count mismatch")
			}
		}
	}
	corpus := make([]CorpusItem, 0, len(rows))
	byText := map[string][]string{}
	seen := map[string]bool{}
	for _, row := range rows {
		var index int
		if err := json.Unmarshal(row.Index, &index); err != nil {
			return nil, nil, errors.New("corpus index must be an integer")
		}
		id := dataset.ID + ":" + strconv.Itoa(index)
		content := row.Title + "\n" + row.Text
		if seen[id] || row.Text == "" || utf8.RuneCountInString(content) > 16384 {
			return nil, nil, errors.New("invalid corpus row or admission limit")
		}
		seen[id] = true
		byText[content] = append(byText[content], id)
		corpus = append(corpus, CorpusItem{SourceDocID: id, Title: row.Title, Content: content, SourceDataset: dataset.ID})
	}
	result := make([]Question, 0, len(questions))
	seen = map[string]bool{}
	for i, raw := range questions {
		question, err := adaptQuestion(dataset.Adapter, raw, i, byText)
		if err != nil {
			return nil, nil, fmt.Errorf("%s question %d: %w", dataset.ID, i, err)
		}
		if question.Query == "" || len(question.Answers) == 0 || len(question.GoldSources) == 0 || seen[question.ID] {
			return nil, nil, errors.New("incomplete or duplicated public question")
		}
		seen[question.ID] = true
		result = append(result, question)
	}
	return corpus, result, nil
}

func adaptQuestion(adapter string, raw map[string]json.RawMessage, index int, byText map[string][]string) (Question, error) {
	q := Question{ID: adapter + ":" + strconv.Itoa(index)}
	if err := json.Unmarshal(raw["question"], &q.Query); err != nil {
		return q, err
	}
	answerField := "answer"
	if adapter == "nq_rear" {
		answerField = "reference"
	}
	if adapter == "popqa" {
		answerField = "possible_answers"
	}
	if err := json.Unmarshal(raw[answerField], &q.Answers); err != nil {
		var text string
		if err = json.Unmarshal(raw[answerField], &text); err != nil {
			return q, errors.New("missing published answer")
		}
		if adapter == "popqa" {
			if err = json.Unmarshal([]byte(text), &q.Answers); err != nil {
				return q, errors.New("invalid published aliases")
			}
		} else {
			q.Answers = []string{text}
		}
	}
	if adapter == "musique" && raw["answer_aliases"] != nil {
		var aliases []string
		if err := json.Unmarshal(raw["answer_aliases"], &aliases); err != nil {
			return q, err
		}
		q.Answers = append(q.Answers, aliases...)
	}
	var gold []string
	switch adapter {
	case "hotpotqa", "2wikimultihopqa":
		var facts [][]json.RawMessage
		var contexts [][]json.RawMessage
		if err := json.Unmarshal(raw["supporting_facts"], &facts); err != nil {
			return q, err
		}
		if err := json.Unmarshal(raw["context"], &contexts); err != nil {
			return q, err
		}
		titles := map[string]bool{}
		for _, fact := range facts {
			var title string
			if len(fact) != 2 {
				return q, errors.New("invalid supporting fact")
			}
			if err := json.Unmarshal(fact[0], &title); err != nil {
				return q, err
			}
			titles[title] = true
		}
		join := ""
		if adapter == "2wikimultihopqa" {
			join = " "
		}
		for title := range titles {
			matches := map[string]bool{}
			for _, context := range contexts {
				if len(context) != 2 {
					return q, errors.New("invalid context")
				}
				var name string
				var sentences []string
				if err := json.Unmarshal(context[0], &name); err != nil {
					return q, err
				}
				if name != title {
					continue
				}
				if err := json.Unmarshal(context[1], &sentences); err != nil {
					return q, err
				}
				matches[title+"\n"+strings.Join(sentences, join)] = true
			}
			if len(matches) != 1 {
				return q, errors.New("missing or ambiguous supporting title")
			}
			for text := range matches {
				gold = append(gold, text)
			}
		}
	case "musique", "nq_rear", "popqa":
		field := "paragraphs"
		if adapter == "nq_rear" {
			field = "contexts"
		}
		var passages []struct {
			Title         string `json:"title"`
			Text          string `json:"text"`
			ParagraphText string `json:"paragraph_text"`
			Supporting    bool   `json:"is_supporting"`
		}
		if err := json.Unmarshal(raw[field], &passages); err != nil {
			return q, err
		}
		for _, p := range passages {
			if !p.Supporting {
				continue
			}
			text := p.Text
			if adapter == "musique" {
				text = p.ParagraphText
			}
			gold = append(gold, p.Title+"\n"+text)
		}
	default:
		return q, errors.New("unsupported adapter")
	}
	seen := map[string]bool{}
	for _, text := range gold {
		ids := byText[text]
		if len(ids) != 1 {
			return q, errors.New("gold passage has missing or ambiguous corpus mapping")
		}
		if !seen[ids[0]] {
			q.GoldSources = append(q.GoldSources, ids[0])
			seen[ids[0]] = true
		}
	}
	return q, nil
}
