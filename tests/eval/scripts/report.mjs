const policies = ["single", "multi_turn"];
const unavailable = "Unavailable";

function label(value, limit = 160) {
  return typeof value === "string" && value && !/[^A-Za-z0-9 ,./_:@=-]/.test(value)
    ? value.slice(0, limit) : unavailable;
}

function number(value) {
  return Number.isSafeInteger(value) && value >= 0 ? value.toLocaleString("en-US") : unavailable;
}

function fraction(value) {
  return Number.isFinite(value) && value >= 0 && value <= 1 ? `${(100 * value).toFixed(2)}%` : unavailable;
}

function seconds(value) {
  return Number.isFinite(value) && value >= 0 ? (value / 1000).toFixed(2) : unavailable;
}

export function evaluationSummary(report, { heading, source, image, workers }) {
  const models = new Set(report.datasets.map((row) => row.identity?.model).filter(Boolean));
  const model = models.size === 1 ? label(models.values().next().value) : models.size > 1 ? "Mixed settings" : unavailable;
  const lines = [
    `**${heading}** completed ${report.completed_answers}/${report.expected_answers} answers. The run ${report.complete ? "passed" : "is incomplete"}.`,
    "",
    `- Source: \`${label(source)}\``,
    `- Image: \`${label(image)}\``,
    `- Model: \`${model}\``,
    `- Answer workers per dataset: ${number(workers)}`,
    "- Numeric report: `evaluation-comparison`",
    "",
    "| Dataset | Policy | Status | Completed / expected | Failed | Missing | R@1 | R@5 | R@10 | All support | EM | F1 | Cited gold | Reported tokens (in / out / total) |",
    "| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
  ];
  const failures = [];
  for (const row of report.datasets) {
    const status = row.unavailable ? "Unavailable" : row.complete ? "Complete" : "Incomplete";
    for (const policy of policies) {
      const axis = row.matrix?.find((entry) => entry.policy === policy);
      const mean = axis?.mean;
      const tokens = axis ? [axis.reported_input_tokens, axis.reported_output_tokens, axis.reported_total_tokens].map(number).join(" / ") : unavailable;
      lines.push(`| ${label(row.dataset)} | ${policy} | ${status} | ${number(axis?.completed ?? 0)} / ${number(row.expected / 2)} | ${number(axis?.failed)} | ${number(axis ? axis.missing : row.expected / 2)} | ${fraction(mean?.recall_at_1)} | ${fraction(mean?.recall_at_5)} | ${fraction(mean?.recall_at_10)} | ${fraction(axis?.all_support_recovery_rate)} | ${fraction(mean?.exact_match)} | ${fraction(mean?.f1)} | ${fraction(mean?.cited_gold_source_coverage)} | ${tokens} |`);
    }
    if (row.unavailable || row.failure) {
      const capture = row.capture_failure;
      const detail = capture ? ` (phase ${label(capture.phase)}, record ${label(capture.kind)}, sequence ${number(capture.sequence)}, reason ${label(capture.reason)}${capture.protection ? `, protection ${label(capture.protection)}` : ""})` : "";
      failures.push(`- **${label(row.dataset)}** — ${label(row.unavailable || row.failure)}${detail}`);
    }
  }
  lines.push("", "Reported tokens cover available measurements. The comparison artifact records usage limitations.",
    "", "| Dataset | Scoring (s) | Capture sink (s) | Runtime log preservation (s) |",
    "| --- | ---: | ---: | ---: |");
  for (const { dataset, timings_ms: timings } of report.datasets) {
    lines.push(`| ${label(dataset)} | ${seconds(timings?.ontology_off_scoring)} | ${seconds(timings?.capture_sink_ms)} | ${seconds(timings?.runtime_log_capture)} |`);
  }
  if (report.failure) failures.unshift(`- ${label(report.failure)}`);
  if (failures.length) lines.push("", "### Incomplete results", "", ...failures.map((line) => line.replaceAll("@", "&#64;")));
  return `${lines.join("\n")}\n`;
}
