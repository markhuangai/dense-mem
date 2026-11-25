import { FieldRow } from "../ui/components";
import { Input } from "../ui/primitives/input";
import { Textarea } from "../ui/primitives/textarea";
import { Button } from "../ui/primitives/button";
import { FormEvent, useEffect, useState } from "react";
import { type TopicEdit, type TopicRecord } from "./topic-types";

export function TopicEditor({ records, target, disabled, onAdd, onDirtyChange }: {
  records: TopicRecord[]; target: TopicRecord | null; disabled: boolean; onAdd: (edit: TopicEdit) => void; onDirtyChange?: (dirty: boolean) => void;
}) {
  const [key, setKey] = useState("");
  const [label, setLabel] = useState("");
  const [description, setDescription] = useState("");
  const [aliases, setAliases] = useState("");
  const [parent, setParent] = useState("");

  useEffect(() => {
    if (!target) return;
    if (target.definition) {
      setKey(target.definition.key); setLabel(target.definition.label);
      setDescription(target.definition.description); setAliases((target.definition.aliases ?? []).join(", "));
      setParent(target.definition.parent_id ?? "");
    }
  }, [target]);

  const definitions = records.filter((record) => record.definition && !record.retired);
  const valid = key.trim() && label.trim() && description.trim();

  function submit(event: FormEvent) {
    event.preventDefault();
    if (!valid) return;
    const editing = target?.definition ? target : null;
    const edit: TopicEdit = {
      action: "define", kind: "topic", expected_version: editing?.edit_version ?? editing?.version ?? 0,
      ...(editing ? { record_id: editing.id } : {}),
      definition: {
        key: key.trim(), label: label.trim(), description: description.trim(),
        aliases: aliases.split(",").map((alias) => alias.trim()).filter(Boolean),
        ...(parent.trim() ? { parent_id: parent.trim() } : {}),
      },
    };
    onAdd(edit);
    onDirtyChange?.(false);
  }

  return <form className="topic-editor" onChange={() => onDirtyChange?.(true)} onSubmit={submit}>
    <fieldset disabled={disabled}>
        <FieldRow label="Key" htmlFor="topic-definition-key"><Input id="topic-definition-key" aria-label="Definition key" value={key} maxLength={128} onChange={(event) => setKey(event.target.value)} /></FieldRow>
        <FieldRow label="Label" htmlFor="topic-definition-label"><Input id="topic-definition-label" aria-label="Definition label" value={label} maxLength={128} onChange={(event) => setLabel(event.target.value)} /></FieldRow>
        <FieldRow label="Description" htmlFor="topic-definition-description"><Textarea id="topic-definition-description" aria-label="Definition description" value={description} maxLength={512} onChange={(event) => setDescription(event.target.value)} /></FieldRow>
        <FieldRow label="Aliases, separated by commas" htmlFor="topic-definition-aliases"><Input id="topic-definition-aliases" aria-label="Definition aliases" value={aliases} onChange={(event) => setAliases(event.target.value)} /></FieldRow>
        <FieldRow label="Parent definition ID" htmlFor="topic-definition-parent"><Input id="topic-definition-parent" aria-label="Definition parent" list="topic-parents" value={parent} onChange={(event) => { setParent(event.target.value); }} /><datalist id="topic-parents">{definitions.filter((record) => record.kind === "topic" && record.id !== target?.id).map((record) => <option key={record.id} value={record.id}>{record.definition!.label}</option>)}</datalist></FieldRow>
        <p>Parent topics organize memory. Recall follows direct membership.</p>
      <Button variant="default" className="primary-button" type="submit" disabled={!valid}>Add topic change</Button>
    </fieldset>
  </form>;
}
