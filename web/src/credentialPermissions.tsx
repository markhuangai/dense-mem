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
      <label className="permission-checkbox">
        <input type="checkbox" checked disabled readOnly aria-label="Read" />
        <span>Read</span>
      </label>
      <label className="permission-checkbox">
        <input
          type="checkbox"
          checked={write}
          disabled={disabled || forceWrite}
          aria-label="Write"
          onChange={(event) => onChange(credentialScopesFromFlags(forceWrite || event.target.checked, feedback))}
        />
        <span>Write</span>
      </label>
      <label className="permission-checkbox">
        <input
          type="checkbox"
          checked={feedback}
          disabled={disabled}
          aria-label="Recall feedback"
          onChange={(event) => onChange(credentialScopesFromFlags(write, event.target.checked))}
        />
        <span>Recall feedback</span>
      </label>
    </div>
  );
}
