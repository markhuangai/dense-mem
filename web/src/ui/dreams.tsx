import { InfoTooltip } from "./components";

type DreamDerivation = {
  premise_position: number;
  relationship_id: string;
  relationship_version: number;
  quote: string;
  authority: string;
};

type DreamEvidence = {
  hypothesis: string;
  derivations?: readonly DreamDerivation[];
};

type DreamRunPresentation = {
  attempted_paths?: number;
  provider_proposals?: number;
  outcome_summary?: Record<string, number>;
};

export function DreamEvidenceSummary({ dream }: { dream: DreamEvidence }) {
  const derivations = dream.derivations ?? [];
  if (derivations.length === 0) {
    return <span className="form-meta">No cited excerpts</span>;
  }
  return (
    <span>
      {derivations.length} cited excerpt{derivations.length === 1 ? "" : "s"}
      <InfoTooltip label={`Evidence used for ${dream.hypothesis}`}>
        <div>
          {derivations.map((derivation, index) => (
            <div key={`${derivation.relationship_id}-${derivation.premise_position}-${index}`}>
              Premise {derivation.premise_position} · {derivation.authority} · “{derivation.quote}”
            </div>
          ))}
        </div>
      </InfoTooltip>
    </span>
  );
}

export function MetricLabel({ label, detail }: { label: string; detail: string }) {
  return (
    <span>
      {label} <InfoTooltip label={`${label} meaning`}>{detail}</InfoTooltip>
    </span>
  );
}

export function runOutcome(run: DreamRunPresentation): string {
  const outcomes = run.outcome_summary;
  if (!outcomes) {
    return "-";
  }
  if ((outcomes.provider_failed ?? 0) > 0) {
    return "Provider call failed";
  }
  const attemptedPaths = run.attempted_paths ?? outcomes.attempted_paths ?? 0;
  if (attemptedPaths === 0) {
    if ((outcomes.blocked_targets ?? 0) > 0) {
      return `${outcomes.blocked_targets} target${outcomes.blocked_targets === 1 ? "" : "s"} already exist`;
    }
    if ((outcomes.previously_assessed_paths ?? 0) > 0) {
      return "All paths already assessed";
    }
    return "No eligible two-hop path";
  }
  if ((outcomes.policy_rejections ?? 0) > 0) {
    return `${outcomes.policy_rejections} blocked during persistence`;
  }
  if ((outcomes.provider_proposals ?? run.provider_proposals ?? 0) === 0) {
    return "Provider returned no supported relationship";
  }
  return "Provider result stored";
}

export function runStatusClass(status: string): string {
  if (status === "error" || status === "failed") {
    return "status-pill error";
  }
  if (status === "skipped") {
    return "status-pill warning";
  }
  return "status-pill";
}
