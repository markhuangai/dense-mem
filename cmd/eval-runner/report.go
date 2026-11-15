package main

import (
	"encoding/json"
	"io"

	"github.com/markhuangai/dense-mem/internal/evalharness"
)

func writeReport(writer io.Writer, report evalharness.Report) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
