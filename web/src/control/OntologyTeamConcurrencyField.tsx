import { useCallback, useEffect, useState } from "react";
import type { ControlApi, OntologyMaintenanceStatus, Page, Team } from "../api";
import type { OntologyMaintenancePolicy } from "../ontology-maintenance-api-types";
import { Button } from "../ui/primitives/button";
import { Input } from "../ui/primitives/input";
import { Label } from "../ui/primitives/label";
import { NativeSelect, NativeSelectOption } from "../ui/primitives/native-select";
import { useUnsavedChanges } from "../ui/navigation";
import { readError } from "./utils";

type Override = { team_id: string; max_concurrency: number };

function teamLimit(policy: OntologyMaintenancePolicy, team: string): number {
  return Math.min(policy.max_concurrency, policy.team_concurrency_overrides?.find((item) => item.team_id === team)?.max_concurrency ?? policy.team_max_concurrency ?? 1);
}

export function OntologyTeamConcurrencyField({ api, value, onChange, status }: {
  api: ControlApi; value: string; onChange: (value: string) => void; status: OntologyMaintenanceStatus | null;
}) {
  const overrides: Override[] = JSON.parse(value || "[]");
  const [page, setPage] = useState<Page<Team> | null>(null);
  const [names, setNames] = useState<Record<string, string>>({});
  const [team, setTeam] = useState("");
  const [limit, setLimit] = useState("1");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  useUnsavedChanges(Boolean(team));
  const load = useCallback(async (offset = 0) => {
    setLoading(true);
    setError("");
    try {
      const next = await api.listTeams(10, offset);
      setPage(next);
      setNames((current) => ({ ...current, ...Object.fromEntries(next.data.map((item) => [item.id, item.name])) }));
    } catch (err) { setError(readError(err)); }
    finally { setLoading(false); }
  }, [api]);
  useEffect(() => { void load(); }, [load]);
  const valid = team && Number.isInteger(Number(limit)) && Number(limit) >= 1 && Number(limit) <= 8
    && (overrides.length < 128 || overrides.some((item) => item.team_id === team));
  const teams = [...new Set([...overrides.map((item) => item.team_id), ...(status?.window?.policy.team_concurrency_overrides ?? []).map((item) => item.team_id), ...(status?.pending_policy.team_concurrency_overrides ?? []).map((item) => item.team_id)])];
  return <div className="stack">
    <p className="form-meta">Choose a team and stage its limit, then save config. Overrides take effect at the next window and share the global allowance.</p>
    {error && <p role="alert" className="banner error">{error}</p>}
    <div className="setting-row"><Label htmlFor="ontology-override-team">Team for override</Label>
      <NativeSelect id="ontology-override-team" disabled={loading} value={team} onChange={(event) => { setTeam(event.target.value); setLimit(String(overrides.find((item) => item.team_id === event.target.value)?.max_concurrency ?? 1)); }}>
        <NativeSelectOption value="">Select a team</NativeSelectOption>
        {team && !page?.data.some((item) => item.id === team) && <NativeSelectOption value={team}>{names[team] ?? team}</NativeSelectOption>}
        {page?.data.map((item) => <NativeSelectOption key={item.id} value={item.id}>{item.name}</NativeSelectOption>)}
      </NativeSelect>
    </div>
    <div className="button-row">
      <Button type="button" variant="outline" disabled={loading || !page || page.pagination.offset === 0} onClick={() => void load(Math.max(0, page!.pagination.offset - 10))}>Previous teams</Button>
      <Button type="button" variant="outline" disabled={loading || !page || page.pagination.offset + page.data.length >= page.pagination.total} onClick={() => void load(page!.pagination.offset + 10)}>Next teams</Button>
      {error && <Button type="button" variant="outline" onClick={() => void load(page?.pagination.offset)}>Retry teams</Button>}
    </div>
    <div className="setting-row"><Label htmlFor="ontology-override-limit">Concurrency override</Label><Input id="ontology-override-limit" type="number" min="1" max="8" step="1" value={limit} onChange={(event) => setLimit(event.target.value)} /></div>
    {team && <p className="form-meta">Current window limit: {status ? status.window ? teamLimit(status.window.policy, team) : "No window" : "Unavailable"}. Saved next-window limit: {status ? teamLimit(status.pending_policy, team) : "Unavailable"}.</p>}
    <Button type="button" variant="outline" disabled={!valid} onClick={() => { onChange(JSON.stringify([...overrides.filter((item) => item.team_id !== team), { team_id: team, max_concurrency: Number(limit) }])); setTeam(""); }}>Stage team override</Button>
    <ul className="stack">{teams.map((id) => {
      const draft = overrides.find((item) => item.team_id === id);
      const saved = status?.pending_policy.team_concurrency_overrides?.find((item) => item.team_id === id);
      return <li key={id} className="stack"><strong>{names[id] ?? id}</strong>
        <span>Staged: {draft?.max_concurrency ?? "Use default"} · Saved: {saved?.max_concurrency ?? "Use default"} · Current effective: {status?.window ? teamLimit(status.window.policy, id) : "No window"}{status && ` · Pending effective: ${teamLimit(status.pending_policy, id)}`}</span>
        {draft && <Button type="button" variant="outline" aria-label={`Use default for ${names[id] ?? id}`} onClick={() => onChange(JSON.stringify(overrides.filter((item) => item.team_id !== id)))}>Use default</Button>}
      </li>;
    })}</ul>
  </div>;
}
