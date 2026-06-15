import { Check, Copy, X } from "lucide-react";
import { FormEventHandler, ReactNode, useEffect, useRef, useState } from "react";

export type ThemeName = "light" | "dark";

type BrandProps = {
  title: string;
  icon: ReactNode;
};

export type PortalNavItem = {
  id: string;
  label: string;
  icon: ReactNode;
  active: boolean;
  disabled?: boolean;
  onClick: () => void;
};

type AuthShellProps = BrandProps & {
  theme: ThemeName;
  onSubmit: FormEventHandler<HTMLFormElement>;
  actions?: ReactNode;
  children: ReactNode;
};

type PortalShellProps = BrandProps & {
  theme: ThemeName;
  topbarActions: ReactNode;
  navLabel: string;
  navItems: PortalNavItem[];
  sidebarTitle: ReactNode;
  sidebarMeta?: ReactNode;
  sidebarSubtitle?: ReactNode;
  sidebarBody?: ReactNode;
  detailLabel: string;
  error?: string;
  children: ReactNode;
};

type SectionHeadingProps = {
  title: ReactNode;
  subtitle?: ReactNode;
  meta?: ReactNode;
  actions?: ReactNode;
};

type SummaryCardProps = {
  label: ReactNode;
  value: ReactNode;
  detail?: ReactNode;
  tone?: "neutral" | "warning" | "danger";
};

type FieldRowProps = {
  label: ReactNode;
  htmlFor?: string;
  children: ReactNode;
};

type SecretBoxProps = {
  value: string;
  valueLabel: string;
  copyLabel: string;
  dismissLabel: string;
  onDismiss: () => void;
};

export function Brand({ title, icon }: BrandProps) {
  return (
    <div className="brand-row">
      <span className="brand-mark">{icon}</span>
      <h1>{title}</h1>
    </div>
  );
}

export function AuthShell({ theme, title, icon, actions, children, onSubmit }: AuthShellProps) {
  return (
    <main className="auth-shell" data-theme={theme}>
      <form className="auth-panel" onSubmit={onSubmit}>
        <div className="brand-row">
          <span className="brand-mark">{icon}</span>
          <h1>{title}</h1>
          {actions}
        </div>
        {children}
      </form>
    </main>
  );
}

export function PortalShell({
  theme,
  title,
  icon,
  topbarActions,
  navLabel,
  navItems,
  sidebarTitle,
  sidebarMeta,
  sidebarSubtitle,
  sidebarBody,
  detailLabel,
  error,
  children,
}: PortalShellProps) {
  return (
    <main className="app-shell" data-theme={theme}>
      <header className="topbar">
        <Brand title={title} icon={icon} />
        <div className="topbar-actions">{topbarActions}</div>
      </header>

      {error && <div className="banner error" role="alert">{error}</div>}

      <section className="workspace">
        <aside className="control-sidebar" aria-label={navLabel}>
          <nav className="portal-tabs" aria-label={navLabel.replace("navigation", "sections")}>
            {navItems.map((item) => (
              <TabButton
                key={item.id}
                active={item.active}
                disabled={item.disabled}
                icon={item.icon}
                label={item.label}
                onClick={item.onClick}
              />
            ))}
          </nav>
          <div className="sidebar-panel">
            <SectionHeading title={sidebarTitle} subtitle={sidebarSubtitle} meta={sidebarMeta} />
            {sidebarBody}
          </div>
        </aside>

        <section className="detail-pane" aria-label={detailLabel}>
          {children}
        </section>
      </section>
    </main>
  );
}

export function TabButton({
  active,
  disabled,
  icon,
  label,
  onClick,
}: {
  active: boolean;
  disabled?: boolean;
  icon: ReactNode;
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      className={active ? "tab-button active" : "tab-button"}
      type="button"
      aria-current={active ? "page" : undefined}
      disabled={disabled}
      onClick={onClick}
    >
      {icon}
      <span>{label}</span>
    </button>
  );
}

export function SectionHeading({ title, subtitle, meta, actions }: SectionHeadingProps) {
  return (
    <div className="section-heading">
      <div className="section-title-stack">
        <h2>{title}</h2>
        {subtitle && <p className="section-subtitle">{subtitle}</p>}
      </div>
      {actions ?? (meta !== undefined && meta !== null ? <span>{meta}</span> : null)}
    </div>
  );
}

export function SummaryCard({ label, value, detail, tone = "neutral" }: SummaryCardProps) {
  return (
    <div className={`summary-item ${tone}`}>
      <span>{label}</span>
      <strong>{value}</strong>
      {detail && <small>{detail}</small>}
    </div>
  );
}

export function SecretBox({ value, valueLabel, copyLabel, dismissLabel, onDismiss }: SecretBoxProps) {
  const [copied, setCopied] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const secret = value.trim();

  useEffect(() => {
    if (!copied) {
      return;
    }
    const timeout = window.setTimeout(() => setCopied(false), 2000);
    return () => window.clearTimeout(timeout);
  }, [copied]);

  async function copySecret() {
    setCopied(await writeClipboardText(secret, inputRef.current));
  }

  return (
    <div className="secret-box" role="status">
      <input
        ref={inputRef}
        className="secret-value"
        value={secret}
        readOnly
        aria-label={valueLabel}
        spellCheck={false}
        onFocus={(event) => event.currentTarget.select()}
      />
      <div className="secret-actions">
        <button className="icon-button" type="button" aria-label={copyLabel} onClick={() => void copySecret()}>
          {copied ? <Check size={17} aria-hidden="true" /> : <Copy size={17} aria-hidden="true" />}
        </button>
        <button className="icon-button" type="button" aria-label={dismissLabel} onClick={onDismiss}>
          <X size={17} aria-hidden="true" />
        </button>
      </div>
    </div>
  );
}

async function writeClipboardText(text: string, fallbackInput: HTMLInputElement | null): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // Fall back to the selected input path below.
  }

  if (!fallbackInput) {
    return false;
  }
  fallbackInput.focus();
  fallbackInput.select();
  fallbackInput.setSelectionRange(0, text.length);
  try {
    return document.execCommand("copy");
  } catch {
    return false;
  }
}

export function FieldRow({ label, htmlFor, children }: FieldRowProps) {
  return (
    <>
      <label htmlFor={htmlFor}>{label}</label>
      <div className="field-control">{children}</div>
    </>
  );
}
