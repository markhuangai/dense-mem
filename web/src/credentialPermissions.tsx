import { Label } from "./ui/primitives/label";
import { Checkbox } from "./ui/primitives/checkbox";
export type MemoryPermission = { read: boolean; write: boolean };
export type MemoryAccess = { private: MemoryPermission; team_shared: MemoryPermission };

export const CREDENTIAL_SCOPE_READ = "read";
export const CREDENTIAL_SCOPE_WRITE = "write";
export const CREDENTIAL_SCOPE_FEEDBACK_READ = "feedback:read";

export function credentialScopesFromFlags(write: boolean, feedback: boolean): string[] {
  return [
    CREDENTIAL_SCOPE_READ,
    ...(write ? [CREDENTIAL_SCOPE_WRITE] : []),
    ...(feedback ? [CREDENTIAL_SCOPE_FEEDBACK_READ] : []),
  ];
}

export function hasCredentialScope(scopes: readonly string[] | null | undefined, scope: string): boolean {
  return scopes?.includes(scope) ?? false;
}

export function normalizeCredentialScopes(scopes: readonly string[] | null | undefined, options: { forceWrite?: boolean } = {}): string[] {
  return credentialScopesFromFlags(
    Boolean(options.forceWrite) || hasCredentialScope(scopes, CREDENTIAL_SCOPE_WRITE),
    hasCredentialScope(scopes, CREDENTIAL_SCOPE_FEEDBACK_READ),
  );
}

export function CredentialPermissionCheckboxes({
  scopes,
  forceWrite = false,
  disabled = false,
  ariaLabel,
  className = "",
  onChange,
}: {
  scopes: readonly string[] | null | undefined;
  forceWrite?: boolean;
  disabled?: boolean;
  ariaLabel: string;
  className?: string;
  onChange: (scopes: string[]) => void;
}) {
  const write = forceWrite || hasCredentialScope(scopes, CREDENTIAL_SCOPE_WRITE);
  const feedback = hasCredentialScope(scopes, CREDENTIAL_SCOPE_FEEDBACK_READ);
  const rootClassName = `permission-checkbox-group${className ? ` ${className}` : ""}`;

  return (
    <div className={rootClassName} role="group" aria-label={ariaLabel}>
      <Label className="permission-checkbox">
        <Checkbox checked disabled aria-label="Read" />
        <span>Read</span>
      </Label>
      <Label className="permission-checkbox">
        <Checkbox
          checked={write}
          disabled={disabled || forceWrite}
          aria-label="Write"
          onCheckedChange={(event) => onChange(credentialScopesFromFlags(forceWrite || (event === true), feedback))}
        />
        <span>Write</span>
      </Label>
      <Label className="permission-checkbox">
        <Checkbox
          checked={feedback}
          disabled={disabled}
          aria-label="Recall feedback"
          onCheckedChange={(event) => onChange(credentialScopesFromFlags(write, (event === true)))}
        />
        <span>Recall feedback</span>
      </Label>
    </div>
  );
}

export function memoryPermissionLabel(permission: MemoryPermission, team = false): string {
  if (!permission.read) return "No access";
  if (permission.write) return team ? "Team read/write" : "Read/write";
  return team ? "Team-read-only" : "Read only";
}

export function MemoryAccessSummary({ access }: { access: MemoryAccess }) {
  if (!access) return <small>Memory access unavailable</small>;
  return <small className="table-subline">Private: {memoryPermissionLabel(access.private)} · {memoryPermissionLabel(access.team_shared, true)}</small>;
}
