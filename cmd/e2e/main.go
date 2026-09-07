package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type databaseCase struct {
	ID       string `json:"id"`
	Package  string `json:"package"`
	Run      string `json:"run"`
	Phase    string `json:"phase"`
	Scenario string `json:"scenario,omitempty"`
	Source   string `json:"source"`
}

type caseFragment struct {
	Version    int            `json:"version"`
	Capability string         `json:"capability"`
	Cases      []databaseCase `json:"cases"`
}

type goEvent struct {
	Action string `json:"Action"`
	Test   string `json:"Test"`
}

type overlay struct {
	Replace map[string]string `json:"Replace"`
}

var testDeclarationPattern = regexp.MustCompile(`(?m)^\s*func\s+(Test[A-Za-z0-9_]*)\s*\(`)

func main() {
	rootFlag := flag.String("root", "", "repository root; defaults to the parent of this module")
	phaseFlag := flag.String("phase", "precheck", "case phase: precheck or scenario")
	capabilityFlag := flag.String("capability", "", "comma-separated capability names")
	scenarioFlag := flag.String("scenario", "", "scenario name for scenario-owned cases")
	caseFlag := flag.String("case", "", "comma-separated case IDs")
	listFlag := flag.Bool("list", false, "list selected case IDs and exit")
	timeoutFlag := flag.Duration("timeout", 20*time.Minute, "maximum duration for each package batch")
	totalTimeoutFlag := flag.Duration("total-timeout", 0, "maximum duration for the selected database phase; zero disables the phase-wide limit")
	flag.Parse()

	root, err := repositoryRoot(*rootFlag)
	if err != nil {
		fatal(err)
	}
	if *phaseFlag != "precheck" && *phaseFlag != "scenario" {
		fatal(fmt.Errorf("invalid phase %q", *phaseFlag))
	}
	if *timeoutFlag <= 0 {
		fatal(errors.New("timeout must be positive"))
	}
	if *totalTimeoutFlag < 0 {
		fatal(errors.New("total-timeout cannot be negative"))
	}

	cases, err := loadCases(root, *phaseFlag, *capabilityFlag, *scenarioFlag, *caseFlag)
	if err != nil {
		fatal(err)
	}
	if len(cases) == 0 {
		fatal(errors.New("no database cases selected"))
	}
	if *listFlag {
		for _, item := range cases {
			fmt.Println(item.ID)
		}
		return
	}

	overlayPath, err := writeOverlay(root)
	if err != nil {
		fatal(err)
	}
	defer os.Remove(overlayPath)

	var deadline time.Time
	if *totalTimeoutFlag > 0 {
		deadline = time.Now().Add(*totalTimeoutFlag)
	}
	for _, batch := range groupCases(cases) {
		batchTimeout := *timeoutFlag
		if !deadline.IsZero() {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				fatal(fmt.Errorf("selected database phase exceeded %s", *totalTimeoutFlag))
			}
			if remaining < batchTimeout {
				batchTimeout = remaining
			}
		}
		if err := runBatch(root, overlayPath, batch, batchTimeout); err != nil {
			fatal(err)
		}
	}
}

func repositoryRoot(value string) (string, error) {
	if value != "" {
		return filepath.Abs(value)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(workingDirectory, "..", ".."))
}

func loadCases(root, phase, capabilityValue, scenarioValue, caseValue string) ([]databaseCase, error) {
	registryDirectory := filepath.Join(root, "scripts", "e2e-db-cases")
	entries, err := os.ReadDir(registryDirectory)
	if err != nil {
		return nil, fmt.Errorf("read database case registry: %w", err)
	}

	capabilities := splitFilter(capabilityValue)
	caseIDs := make(map[string]bool)
	for _, value := range splitFilter(caseValue) {
		caseIDs[value] = true
	}
	seen := make(map[string]bool)
	allCases := make([]databaseCase, 0)
	selected := make([]databaseCase, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		capability := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		capabilitySelected := len(capabilities) == 0 || contains(capabilities, capability)
		contents, err := os.ReadFile(filepath.Join(registryDirectory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read database case registry %s: %w", entry.Name(), err)
		}
		var fragment caseFragment
		if err := json.Unmarshal(contents, &fragment); err != nil {
			return nil, fmt.Errorf("decode database case registry %s: %w", entry.Name(), err)
		}
		if fragment.Version != 1 || fragment.Capability != capability {
			return nil, fmt.Errorf("database case registry %s has invalid version or capability", entry.Name())
		}
		for _, item := range fragment.Cases {
			if item.ID == "" || item.Package == "" || item.Run == "" || item.Source == "" {
				return nil, fmt.Errorf("database case %s has incomplete identity", item.ID)
			}
			if item.Phase != "precheck" && item.Phase != "scenario" {
				return nil, fmt.Errorf("database case %s has invalid phase %q", item.ID, item.Phase)
			}
			if item.Phase == "scenario" && item.Scenario == "" {
				return nil, fmt.Errorf("scenario case %s has no scenario owner", item.ID)
			}
			if _, err := regexp.Compile(item.Run); err != nil {
				return nil, fmt.Errorf("database case %s has invalid run expression: %w", item.ID, err)
			}
			if seen[item.ID] {
				return nil, fmt.Errorf("duplicate database case %s", item.ID)
			}
			seen[item.ID] = true
			allCases = append(allCases, item)
			if !capabilitySelected || item.Phase != phase || (scenarioValue != "" && item.Scenario != scenarioValue) || (len(caseIDs) > 0 && !caseIDs[item.ID]) {
				continue
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(item.Source))); err != nil {
				return nil, fmt.Errorf("database case %s source %s is unavailable: %w", item.ID, item.Source, err)
			}
			selected = append(selected, item)
		}
	}
	if err := reconcileCaseRegistry(root, allCases); err != nil {
		return nil, err
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].Package == selected[j].Package {
			return selected[i].ID < selected[j].ID
		}
		return selected[i].Package < selected[j].Package
	})
	return selected, nil
}

func reconcileCaseRegistry(root string, cases []databaseCase) error {
	registered := make(map[string]databaseCase, len(cases))
	for _, item := range cases {
		name := testName(item.Run)
		if name == "" || !strings.HasPrefix(name, "Test") {
			return fmt.Errorf("database case %s has no concrete test name", item.ID)
		}
		key := item.Source + "\x00" + name
		if _, exists := registered[key]; exists {
			return fmt.Errorf("database case registry maps %s to multiple cases", key)
		}
		registered[key] = item
	}

	declared := make(map[string]bool, len(registered))
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", ".cache":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(entry.Name()) != ".e2e" {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		source := filepath.ToSlash(relative)
		contents, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read E2E test source %s: %w", source, err)
		}
		packageName := "./" + filepath.ToSlash(filepath.Dir(relative))
		for _, match := range testDeclarationPattern.FindAllStringSubmatch(string(contents), -1) {
			name := match[1]
			key := source + "\x00" + name
			item, ok := registered[key]
			if !ok {
				return fmt.Errorf("database case registry has no entry for %s in %s", name, source)
			}
			if item.Package != packageName {
				return fmt.Errorf("database case %s uses package %s, want %s for %s", item.ID, item.Package, packageName, source)
			}
			if declared[key] {
				return fmt.Errorf("duplicate test declaration %s in %s", name, source)
			}
			declared[key] = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	for key, item := range registered {
		if !declared[key] {
			return fmt.Errorf("database case %s has no declaration in %s", item.ID, item.Source)
		}
	}
	return nil
}

func splitFilter(value string) []string {
	items := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			items = append(items, item)
		}
	}
	return items
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func writeOverlay(root string) (string, error) {
	replacements := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", ".cache":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".e2e") {
			return nil
		}
		base := strings.TrimSuffix(path, ".e2e")
		destination := base + "_test.go"
		replacements[destination] = path
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("discover E2E test sources: %w", err)
	}
	if len(replacements) == 0 {
		return "", errors.New("no E2E test sources found")
	}
	file, err := os.CreateTemp("", "dense-mem-e2e-overlay-*.json")
	if err != nil {
		return "", fmt.Errorf("create E2E test overlay: %w", err)
	}
	path := file.Name()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(overlay{Replace: replacements}); err != nil {
		file.Close()
		os.Remove(path)
		return "", fmt.Errorf("write E2E test overlay: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("close E2E test overlay: %w", err)
	}
	return path, nil
}

type packageBatch struct {
	Package string
	Cases   []databaseCase
}

const maxDatabaseCasesPerBatch = 40

func groupCases(cases []databaseCase) []packageBatch {
	byPackage := make(map[string][]databaseCase)
	for _, item := range cases {
		byPackage[item.Package] = append(byPackage[item.Package], item)
	}
	packages := make([]string, 0, len(byPackage))
	for packageName := range byPackage {
		packages = append(packages, packageName)
	}
	sort.Strings(packages)
	batches := make([]packageBatch, 0, len(packages))
	for _, packageName := range packages {
		items := byPackage[packageName]
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		for start := 0; start < len(items); start += maxDatabaseCasesPerBatch {
			end := start + maxDatabaseCasesPerBatch
			if end > len(items) {
				end = len(items)
			}
			batches = append(batches, packageBatch{Package: packageName, Cases: items[start:end]})
		}
	}
	return batches
}

func runBatch(root, overlayPath string, batch packageBatch, timeout time.Duration) error {
	parts := make([]string, 0, len(batch.Cases))
	for _, item := range batch.Cases {
		parts = append(parts, "(?:"+item.Run+")")
	}
	pattern := "^(?:" + strings.Join(parts, "|") + ")$"
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	testTimeout := timeout - time.Minute
	if testTimeout <= 0 {
		testTimeout = timeout
	}
	command := exec.CommandContext(ctx, "go", "test", "-overlay", overlayPath, "-json", "-count=1", "-timeout", testTimeout.String(), "-run", pattern, batch.Package)
	command.Dir = root
	command.Env = append(os.Environ(), "DENSE_MEM_E2E_DB_RUNNER=1")
	var output strings.Builder
	command.Stdout = io.MultiWriter(os.Stdout, &output)
	command.Stderr = io.MultiWriter(os.Stderr, &output)
	fmt.Printf("running %d database cases in %s\n", len(batch.Cases), batch.Package)
	err := command.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("database batch %s exceeded %s: %w", batch.Package, timeout, ctx.Err())
	}
	if err != nil {
		return fmt.Errorf("database batch %s failed: %w", batch.Package, err)
	}
	result := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(output.String()))
	for scanner.Scan() {
		var event goEvent
		if json.Unmarshal([]byte(scanner.Text()), &event) != nil || event.Test == "" {
			continue
		}
		if event.Action == "pass" || event.Action == "skip" || event.Action == "fail" {
			result[event.Test] = event.Action
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read database batch %s output: %w", batch.Package, err)
	}
	for _, item := range batch.Cases {
		name := testName(item.Run)
		action, ok := result[name]
		if !ok {
			return fmt.Errorf("required database case %s did not execute", item.ID)
		}
		if action != "pass" {
			return fmt.Errorf("required database case %s ended with %s", item.ID, action)
		}
	}
	return nil
}

func testName(expression string) string {
	expression = strings.TrimPrefix(expression, "^")
	expression = strings.TrimSuffix(expression, "$")
	if index := strings.IndexAny(expression, "(|"); index >= 0 {
		expression = expression[:index]
	}
	return expression
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "dense-mem E2E database runner: %v\n", err)
	os.Exit(1)
}
