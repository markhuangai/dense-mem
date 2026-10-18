import { Label } from "../ui/primitives/label";
import { useState } from "react";
import { ArrowUpRight, Plus, Search, Users } from "lucide-react";
import type { Team } from "../api";
import { LoadingState, SectionHeading } from "../ui/components";
import { Button } from "../ui/primitives/button";
import { Input } from "../ui/primitives/input";
import { formatDate } from "./utils";

export function TeamDirectory({ teams, loading, total, onCreate, onSelect, onLoadMore }: {
  teams: Team[];
  loading: boolean;
  total: number;
  onCreate: () => void;
  onSelect: (id: string) => void;
  onLoadMore: () => void;
}) {
  const [query, setQuery] = useState("");
  const normalized = query.trim().toLocaleLowerCase();
  const visible = teams.filter((team) => `${team.name} ${team.id}`.toLocaleLowerCase().includes(normalized));
  return <section className="team-directory" aria-label="Team directory">
    <SectionHeading title="Teams" subtitle="Choose a team to manage its memory operations and access." actions={<Button onClick={onCreate}><Plus aria-hidden="true" />New team</Button>} />
    <div className="team-directory-toolbar"><Label className="search-input-wrap"><Search size={17} aria-hidden="true" /><Input aria-label="Search teams" placeholder="Search loaded teams…" value={query} onChange={(event) => setQuery(event.target.value)} /></Label><span className="form-meta">{teams.length} of {total} teams</span></div>
    {loading && !teams.length ? <LoadingState label="Loading teams" /> : visible.length ? <div className="team-directory-list">{visible.map((team) => <Button variant="outline" className="team-directory-card" key={team.id} onClick={() => onSelect(team.id)}><span className="team-mark"><Users aria-hidden="true" /></span><span className="team-directory-copy"><strong>{team.name}</strong>{team.description && <p>{team.description}</p>}<small>Updated {formatDate(team.updated_at)}</small></span><ArrowUpRight className="ml-auto size-4 shrink-0 text-muted-foreground" aria-hidden="true" /></Button>)}</div> : <div className="empty-state"><div><h3>{teams.length ? "No matching teams" : "Create your first team"}</h3><p>{teams.length ? "Try another name or load more teams." : "Teams keep memory and access organized."}</p>{!teams.length && <Button onClick={onCreate}><Plus aria-hidden="true" />New team</Button>}</div></div>}
    {teams.length < total && <Button variant="outline" disabled={loading} onClick={onLoadMore}>{loading ? "Loading teams…" : "Load more teams"}</Button>}
  </section>;
}
