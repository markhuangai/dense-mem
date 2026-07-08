export const PROFILE_SCOPE_READ = "read";
export const PROFILE_SCOPE_WRITE = "write";
export const PROFILE_SCOPE_FEEDBACK_READ = "feedback:read";

export function profileScopesFromFlags(write: boolean, feedback: boolean): string[] {
  return [
    PROFILE_SCOPE_READ,
    ...(write ? [PROFILE_SCOPE_WRITE] : []),
    ...(feedback ? [PROFILE_SCOPE_FEEDBACK_READ] : []),
  ];
}

export function hasProfileScope(scopes: readonly string[] | null | undefined, scope: string): boolean {
  return scopes?.includes(scope) ?? false;
}

export function normalizeProfileScopes(scopes: readonly string[] | null | undefined, options: { forceWrite?: boolean } = {}): string[] {
  return profileScopesFromFlags(
    Boolean(options.forceWrite) || hasProfileScope(scopes, PROFILE_SCOPE_WRITE),
    hasProfileScope(scopes, PROFILE_SCOPE_FEEDBACK_READ),
  );
}

export function ProfilePermissionCheckboxes({
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
  const write = forceWrite || hasProfileScope(scopes, PROFILE_SCOPE_WRITE);
  const feedback = hasProfileScope(scopes, PROFILE_SCOPE_FEEDBACK_READ);
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
          onChange={(event) => onChange(profileScopesFromFlags(forceWrite || event.target.checked, feedback))}
        />
        <span>Write</span>
      </label>
      <label className="permission-checkbox">
        <input
          type="checkbox"
          checked={feedback}
          disabled={disabled}
          aria-label="Recall feedback"
          onChange={(event) => onChange(profileScopesFromFlags(write, event.target.checked))}
        />
        <span>Recall feedback</span>
      </label>
    </div>
  );
}
