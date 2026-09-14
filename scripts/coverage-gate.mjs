import { readFileSync } from "node:fs";

const [, , label = "coverage", reportPath] = process.argv;
if (!reportPath) {
  console.error("usage: node scripts/coverage-gate.mjs <label> <summary.json>");
  process.exit(2);
}

let report;
try {
  report = JSON.parse(readFileSync(reportPath, "utf8"));
} catch (error) {
  console.error(`${label} coverage report is missing or invalid: ${error.message}`);
  process.exit(1);
}

const statements = report.total?.statements;
if (!statements || !Number.isInteger(statements.total) || !Number.isInteger(statements.covered) || statements.total <= 0) {
  console.error(`${label} statement coverage report is empty or invalid`);
  process.exit(1);
}
if (statements.covered * 10 <= statements.total * 9) {
  console.error(`${label} statement coverage ${statements.covered}/${statements.total} does not exceed 90%`);
  process.exit(1);
}
console.log(`${label} statement coverage ${statements.covered}/${statements.total} exceeds 90%`);
