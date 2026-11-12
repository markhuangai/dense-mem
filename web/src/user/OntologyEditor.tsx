import { FieldRow } from "../ui/components";
import { NativeSelect, NativeSelectOption } from "../ui/primitives/native-select";
import { Input } from "../ui/primitives/input";
import { Textarea } from "../ui/primitives/textarea";
import { Button } from "../ui/primitives/button";
import { FormEvent, useEffect, useState } from "react";
import { type OntologyEdit, type OntologyKind, type OntologyRecord, type SourceHandle } from "./ontology-types";

export function OntologyEditor({ records, selected, target, disabled, onAdd, onDirtyChange }: {
  records: OntologyRecord[]; selected: SourceHandle[]; target: OntologyRecord | null; disabled: boolean; onAdd: (edit: OntologyEdit) => void; onDirtyChange?: (dirty: boolean) => void;
}) {
  const [action, setAction] = useState<OntologyEdit["action"]>("define");
  const [kind, setKind] = useState<OntologyKind>("topic");
  const [key, setKey] = useState("");
  const [label, setLabel] = useState("");
  const [description, setDescription] = useState("");
  const [aliases, setAliases] = useState("");
  const [parent, setParent] = useState("");
  const [definitionId, setDefinitionId] = useState("");

  useEffect(() => {
    if (!target) return;
    if (target.definition) {
      setAction("define"); setKind(target.kind); setKey(target.definition.key); setLabel(target.definition.label);
      setDescription(target.definition.description); setAliases((target.definition.aliases ?? []).join(", "));
      setParent(target.definition.parent_id ?? "");
    } else if (target.group) setAction("keep_separate");
  }, [target]);

  const definitions = records.filter((record) => record.definition && !record.retired);
  const compatible = definitions.filter((record) => record.kind === "topic");
  const valid = action === "define" ? key.trim() && label.trim() && description.trim() : action === "classify" ? selected.length === 1 && definitionId : action === "pin_definition" ? definitionId : selected.length >= 2 && selected.length <= 128;

  function submit(event: FormEvent) {
    event.preventDefault();
    if (!valid) return;
    let edit: OntologyEdit;
    if (action === "define") {
      const editing = target?.definition && target.kind === kind;
      edit = { action, expected_version: editing ? target.version : 0, ...(editing ? { record_id: target.id } : {}), kind,
        definition: { key: key.trim(), label: label.trim(), description: description.trim(), aliases: aliases.split(",").map((alias) => alias.trim()).filter(Boolean),
          ...(parent.trim() ? { parent_id: parent.trim() } : {}) } };
    } else if (action === "classify") {
      edit = { action, expected_version: 0, source: selected[0], definition_id: definitionId };
    } else if (action === "pin_definition") {
      edit = { action, expected_version: 0, definition_id: definitionId };
    } else {
      edit = { action, expected_version: 0, members: selected };
    }
    onAdd(edit);
    onDirtyChange?.(false);
  }

  return <form className="ontology-editor" onChange={() => onDirtyChange?.(true)} onSubmit={submit}>
    <fieldset disabled={disabled}>
      <FieldRow label="Correction" htmlFor="ontology-correction"><NativeSelect id="ontology-correction" aria-label="Correction" value={action} onChange={(event) => setAction(event.target.value as OntologyEdit["action"])}>
        <NativeSelectOption value="define">Edit or add a definition</NativeSelectOption><NativeSelectOption value="classify">Classify one source</NativeSelectOption><NativeSelectOption value="group_together">Group sources together</NativeSelectOption><NativeSelectOption value="keep_separate">Keep sources separate</NativeSelectOption><NativeSelectOption value="pin_definition">Pin a definition</NativeSelectOption>
      </NativeSelect></FieldRow>
      {action === "define" ? <>
        <FieldRow label="Definition type" htmlFor="ontology-definition-type"><NativeSelect id="ontology-definition-type" aria-label="Definition type" value={kind} onChange={(event) => { setKind(event.target.value as OntologyKind); setParent(""); }}><NativeSelectOption value="topic">Topic</NativeSelectOption></NativeSelect></FieldRow>
        <FieldRow label="Key" htmlFor="ontology-definition-key"><Input id="ontology-definition-key" aria-label="Definition key" value={key} maxLength={128} onChange={(event) => setKey(event.target.value)} /></FieldRow>
        <FieldRow label="Label" htmlFor="ontology-definition-label"><Input id="ontology-definition-label" aria-label="Definition label" value={label} maxLength={128} onChange={(event) => setLabel(event.target.value)} /></FieldRow>
        <FieldRow label="Description" htmlFor="ontology-definition-description"><Textarea id="ontology-definition-description" aria-label="Definition description" value={description} maxLength={512} onChange={(event) => setDescription(event.target.value)} /></FieldRow>
        <FieldRow label="Aliases, separated by commas" htmlFor="ontology-definition-aliases"><Input id="ontology-definition-aliases" aria-label="Definition aliases" value={aliases} onChange={(event) => setAliases(event.target.value)} /></FieldRow>
        <FieldRow label="Parent definition ID" htmlFor="ontology-definition-parent"><Input id="ontology-definition-parent" aria-label="Definition parent" list="ontology-parents" value={parent} onChange={(event) => { setParent(event.target.value); }} /><datalist id="ontology-parents">{definitions.filter((record) => record.kind === kind && record.id !== target?.id).map((record) => <NativeSelectOption key={record.id} value={record.id}>{record.definition!.label}</NativeSelectOption>)}</datalist></FieldRow>
        <p>Parent topics organize memory. Recall follows direct membership.</p>
        <p>Definition edits stay pinned until you explicitly unpin them.</p>
      </> : action === "classify" || action === "pin_definition" ? <FieldRow label="Definition ID" htmlFor="ontology-target-definition"><Input id="ontology-target-definition" aria-label="Target definition" list="ontology-definitions" value={definitionId} onChange={(event) => setDefinitionId(event.target.value)} /><datalist id="ontology-definitions">{compatible.map((record) => <NativeSelectOption key={record.id} value={record.id}>{record.definition!.label}</NativeSelectOption>)}</datalist></FieldRow> : <p>Selected sources must satisfy the existing grouping rules. Keeping sources separate applies to each selected pair.</p>}
      <Button variant="default" className="primary-button" type="submit" disabled={!valid}>Add correction to draft</Button>
    </fieldset>
  </form>;
}
