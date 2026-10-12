import { FormEvent, useEffect, useState } from "react";
import type { OntologyEdit, OntologyKind, OntologyRecord, SourceHandle } from "./ontology-types";

export function OntologyEditor({ records, selected, target, disabled, onAdd }: {
  records: OntologyRecord[]; selected: SourceHandle[]; target: OntologyRecord | null; disabled: boolean; onAdd: (edit: OntologyEdit) => void;
}) {
  const [action, setAction] = useState<OntologyEdit["action"]>("define");
  const [kind, setKind] = useState<OntologyKind>("topic");
  const [key, setKey] = useState("");
  const [label, setLabel] = useState("");
  const [description, setDescription] = useState("");
  const [aliases, setAliases] = useState("");
  const [parent, setParent] = useState("");
  const [entityKind, setEntityKind] = useState("other");
  const [definitionId, setDefinitionId] = useState("");

  useEffect(() => {
    if (!target) return;
    if (target.definition) {
      setAction("define"); setKind(target.kind); setKey(target.definition.key); setLabel(target.definition.label);
      setDescription(target.definition.description); setAliases((target.definition.aliases ?? []).join(", "));
      setParent(target.definition.parent_id ?? ""); setEntityKind(target.definition.base_entity_kind ?? "other");
    } else if (target.group) setAction("keep_separate");
  }, [target]);

  const definitions = records.filter((record) => record.definition && !record.retired);
  const sourceKind = selected[0]?.kind;
  const compatible = definitions.filter((record) => action === "pin_definition" || (sourceKind === "entity" ? record.kind === "entity_class" : sourceKind === "predicate" ? record.kind === "predicate_concept" : record.kind === "topic"));
  const valid = action === "define" ? key.trim() && label.trim() && description.trim() : action === "classify" ? selected.length === 1 && definitionId : action === "pin_definition" ? definitionId : selected.length >= 2 && selected.length <= 128;

  function submit(event: FormEvent) {
    event.preventDefault();
    if (!valid) return;
    let edit: OntologyEdit;
    if (action === "define") {
      const editing = target?.definition && target.kind === kind;
      edit = { action, expected_version: editing ? target.version : 0, ...(editing ? { record_id: target.id } : {}), kind,
        definition: { key: key.trim(), label: label.trim(), description: description.trim(), aliases: aliases.split(",").map((alias) => alias.trim()).filter(Boolean),
          ...(parent ? { parent_id: parent } : {}), ...(kind === "entity_class" ? { base_entity_kind: entityKind } : {}) } };
    } else if (action === "classify") {
      edit = { action, expected_version: 0, source: selected[0], definition_id: definitionId };
    } else if (action === "pin_definition") {
      edit = { action, expected_version: 0, definition_id: definitionId };
    } else {
      edit = { action, expected_version: 0, members: selected };
    }
    onAdd(edit);
  }

  return <form className="edit-grid" onSubmit={submit}>
    <fieldset disabled={disabled}>
      <label>Correction<select aria-label="Correction" value={action} onChange={(event) => setAction(event.target.value as OntologyEdit["action"])}>
        <option value="define">Edit or add a definition</option><option value="classify">Classify one source</option><option value="group_together">Group sources together</option><option value="keep_separate">Keep sources separate</option><option value="pin_definition">Pin a definition</option>
      </select></label>
      {action === "define" ? <>
        <label>Definition type<select aria-label="Definition type" value={kind} onChange={(event) => { setKind(event.target.value as OntologyKind); setParent(""); }}><option value="topic">Topic</option><option value="entity_class">Entity class</option><option value="predicate_concept">Predicate concept</option></select></label>
        <label>Key<input aria-label="Definition key" value={key} maxLength={128} onChange={(event) => setKey(event.target.value)} /></label>
        <label>Label<input aria-label="Definition label" value={label} maxLength={128} onChange={(event) => setLabel(event.target.value)} /></label>
        <label>Description<textarea aria-label="Definition description" value={description} maxLength={512} onChange={(event) => setDescription(event.target.value)} /></label>
        <label>Aliases, separated by commas<input aria-label="Definition aliases" value={aliases} onChange={(event) => setAliases(event.target.value)} /></label>
        <label>Parent definition ID<input aria-label="Definition parent" list="ontology-parents" value={parent} onChange={(event) => setParent(event.target.value)} /><datalist id="ontology-parents">{definitions.filter((record) => record.kind === kind && record.id !== target?.id).map((record) => <option key={record.id} value={record.id}>{record.definition!.label}</option>)}</datalist></label>
        {kind === "entity_class" && <label>Entity kind<select aria-label="Class entity kind" value={entityKind} onChange={(event) => setEntityKind(event.target.value)}>{["person", "organization", "project", "product", "place", "document", "concept", "other"].map((value) => <option key={value}>{value}</option>)}</select></label>}
        <p>Definition edits stay pinned until you explicitly unpin them.</p>
      </> : action === "classify" || action === "pin_definition" ? <label>Definition ID<input aria-label="Target definition" list="ontology-definitions" value={definitionId} onChange={(event) => setDefinitionId(event.target.value)} /><datalist id="ontology-definitions">{compatible.map((record) => <option key={record.id} value={record.id}>{record.definition!.label}</option>)}</datalist></label> : <p>Selected sources must satisfy the existing grouping rules. Keeping sources separate applies to each selected pair.</p>}
      <button className="primary-button" type="submit" disabled={!valid}>Add correction to draft</button>
    </fieldset>
  </form>;
}
