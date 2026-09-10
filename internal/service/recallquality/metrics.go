// Package recallquality preserves the pre-cutover quality API for the
// evaluation harness until the final compatibility cleanup in issue #382.
package recallquality

import recallquality "github.com/markhuangai/dense-mem/internal/recall/quality"

type (
	ResultRef = recallquality.ResultRef
	Judgment  = recallquality.Judgment
	Metrics   = recallquality.Metrics
)

var ScoreAtK = recallquality.ScoreAtK
