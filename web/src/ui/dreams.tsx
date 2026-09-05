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
  evidence_derivations?: readonly {
    evidence_id: string;
    source_group_key: string;
    span_start: number;
    span_end: number;
    quote: string;
    authority: string;
  }[];
};

type DreamRunPresentation = {
  lane?: string;
  status?: string;
  attempted_paths?: number;
  provider_proposals?: number;
  evidence_targets?: number;
  evaluated_evidence_targets?: number;
  outcome_summary?: Record<string, number>;
};

const EVIDENCE_PASSES_PER_TARGET = 2;

export function DreamEvidenceSummary({ dream }: { dream: DreamEvidence }) {
  const derivations = dream.derivations ?? [];
  const evidenceDerivations = dream.evidence_derivations ?? [];
  if (derivations.length === 0 && evidenceDerivations.length === 0) {
    return <span className="form-meta">No cited excerpts</span>;
  }
  return (
    <span>
      {derivations.length + evidenceDerivations.length} cited excerpt{derivations.length + evidenceDerivations.length === 1 ? "" : "s"}
      <InfoTooltip label={`Evidence used for ${dream.hypothesis}`}>
        <div>
          {derivations.map((derivation, index) => (
            <div key={`${derivation.relationship_id}-${derivation.premise_position}-${index}`}>
              Premise {derivation.premise_position} · {derivation.authority} · “{derivation.quote}”
            </div>
          ))}
          {evidenceDerivations.map((derivation, index) => (
            <div key={`${derivation.evidence_id}-${derivation.span_start}-${index}`}>
              Evidence · {derivation.authority} · “{derivation.quote}”
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
  if (run.lane === "evidence_discovery") {
    const targets = run.evidence_targets ?? outcomes.evidence_targets ?? 0;
    const evaluated = run.evaluated_evidence_targets ?? outcomes.evaluated_evidence_targets ?? 0;
    const providerProposals = outcomes.provider_proposals ?? run.provider_proposals ?? 0;
    if (targets === 0) {
      return "No eligible evidence target";
    }
    if (run.status === "completed") {
      return providerProposals === 0
        ? "Provider returned no supported relationship"
        : "Evidence discovery stored";
    }
    if (evaluated === targets && providerProposals === 0) {
      return "Provider returned no supported relationship";
    }
    if (evaluated < targets * EVIDENCE_PASSES_PER_TARGET) {
      return `${evaluated} of ${targets * EVIDENCE_PASSES_PER_TARGET} evidence target passes evaluated`;
    }
    if (providerProposals === 0) {
      return "Provider returned no supported relationship";
    }
    return "Evidence discovery stored";
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
