import { useState } from "react";
import { Check, ChevronsUpDown, Users } from "lucide-react";
import { Button } from "./primitives/button";
import { Input } from "./primitives/input";
import { Popover, PopoverContent, PopoverTrigger } from "./primitives/popover";

type WorkspaceOption = { id: string; name: string };

export function WorkspacePicker({ options, selectedID, onSelect, disabled = false, loading = false, onLoadMore }: {
  options: WorkspaceOption[];
  selectedID: string;
  onSelect: (id: string) => void | Promise<unknown>;
  disabled?: boolean;
  loading?: boolean;
  onLoadMore?: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const selected = options.find((option) => option.id === selectedID);
  const filtered = options.filter((option) => `${option.name} ${option.id}`.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()));
  return <Popover open={open} onOpenChange={setOpen}>
    <PopoverTrigger asChild><Button variant="outline" className="workspace-trigger" disabled={disabled || loading} aria-label="Choose team" aria-expanded={open}><Users aria-hidden="true" /><span>{selected?.name ?? "Choose a team"}</span><ChevronsUpDown aria-hidden="true" /></Button></PopoverTrigger>
    <PopoverContent className="workspace-picker" align="start">
      <Input aria-label="Find a team" placeholder="Find a team…" value={query} onChange={(event) => setQuery(event.target.value)} />
      <div className="workspace-options" aria-label="Available teams">{filtered.map((option) => <Button variant={option.id === selectedID ? "secondary" : "ghost"} className="workspace-option" key={option.id} disabled={disabled || loading} onClick={async () => { const result = await onSelect(option.id); if (result !== false) { setOpen(false); setQuery(""); } }}><span>{option.name}</span>{option.id === selectedID && <Check className="ml-auto" aria-label="Selected" />}</Button>)}{!filtered.length && <p className="form-meta">No matching loaded teams.</p>}</div>
      {onLoadMore && <Button variant="ghost" className="w-full" disabled={loading} onClick={onLoadMore}>Load more teams</Button>}
    </PopoverContent>
  </Popover>;
}
