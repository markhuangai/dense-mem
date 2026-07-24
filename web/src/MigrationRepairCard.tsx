import type { MigrationRepairSummary } from "./api";

type MigrationRepairCardProps = {
  repair: MigrationRepairSummary;
  repairBlocked: boolean;
  claimEpoch?: number;
};

export function MigrationRepairCard({ repair, repairBlocked, claimEpoch }: MigrationRepairCardProps) {
  return (
    <div className={repair.required || repairBlocked ? "migration-repair-card warning" : "migration-repair-card"} aria-label="Resume repair summary">
      <div><h3>{repairBlocked ? "Resume is blocked" : repair.required ? "Resume will repair placement state" : "Resume can continue from the checkpoint"}</h3></div>
      <p>{repairBlocked
        ? "Some terminal migration records need operator investigation before resume can succeed."
        : repair.required
        ? "The next resume will requeue stale migration-owned placement records before workers continue."
        : "No stale retryable migration-owned placement rows were detected."}</p>
      <dl>
        <div><dt>Legacy predicate reviews</dt><dd>{repair.legacy_predicate_reviews}</dd></div><div><dt>Orphan reviews</dt><dd>{repair.orphan_reviews}</dd></div>
        <div><dt>Processing rows</dt><dd>{repair.abandoned_processing}</dd></div>
        <div><dt>Retryable failures</dt><dd>{repair.retryable_failures}</dd></div><div><dt>Held reviews</dt><dd>{repair.held_reviews}</dd></div>
        <div><dt>Blocked</dt><dd>{repair.blocked_items}</dd></div><div><dt>Blocking exclusions</dt><dd>{repair.blocking_exclusions ?? 0}</dd></div>
        <div><dt>Claim epoch</dt><dd>{repair.claim_epoch_before ?? claimEpoch ?? "—"}</dd></div>
      </dl>
      {!!repair.failure_groups?.length && (
        <div className="migration-failure-groups" aria-label="Failure groups">
          {repair.failure_groups.map((group) => <span key={`${group.stage}:${group.class}`}>{group.stage}/{group.class}: {group.count}</span>)}
        </div>
      )}
    </div>
  );
}
