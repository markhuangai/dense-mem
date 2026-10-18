import { Check, Copy, Info, Menu, PanelLeftClose, PanelLeftOpen, X } from "lucide-react";
import { FormEventHandler, ReactNode, useEffect, useRef, useState } from "react";
import { Button } from "./primitives/button";
import { Input } from "./primitives/input";
import { Badge } from "./primitives/badge";
import { Label } from "./primitives/label";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "./primitives/sheet";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "./primitives/tooltip";
import { portalHref } from "./navigation";
import type { ThemeName } from "./theme";
export type { ThemeName } from "./theme";

type BrandProps = { title: string; icon: ReactNode };
export type PortalNavItem = {
  id: string;
  label: string;
  icon: ReactNode;
  group?: string;
  active: boolean;
  disabled?: boolean;
  href?: string;
  onClick: () => void | Promise<unknown>;
};
type AuthShellProps = BrandProps & { theme: ThemeName; onSubmit: FormEventHandler<HTMLFormElement>; actions?: ReactNode; children: ReactNode };
type PortalShellProps = BrandProps & {
  theme: ThemeName;
  topbarActions: ReactNode;
  navLabel: string;
  navItemsLabel: string;
  navItems: PortalNavItem[];
  workspacePicker?: ReactNode;
  contextBar?: ReactNode;
  detailLabel: string;
  error?: string;
  children: ReactNode;
};
type SectionHeadingProps = { title: ReactNode; subtitle?: ReactNode; meta?: ReactNode; actions?: ReactNode };
type SummaryCardProps = { label: ReactNode; value: ReactNode; detail?: ReactNode; tone?: "neutral" | "warning" | "danger" };
type FieldRowProps = { label: ReactNode; htmlFor?: string; description?: ReactNode; children: ReactNode };
type SecretBoxProps = { value: string; valueLabel: string; copyLabel: string; dismissLabel: string; onDismiss: () => void };
type InfoTooltipProps = { label: string; children: ReactNode };
type LoadingStateProps = { label?: string; compact?: boolean };

export function Brand({ title, icon }: BrandProps) {
  return <div className="brand-row"><span className="brand-mark">{icon}</span><div className="brand-title"><strong>Dense-Mem</strong><span>{title.includes("Control") ? "Control" : "Knowledge"}</span></div></div>;
}

export function AuthShell({ theme, title, icon, actions, children, onSubmit }: AuthShellProps) {
  const control = title.includes("Control");
  return <main className="auth-shell" data-theme={theme}>
    <div className="auth-brand"><Brand title={title} icon={icon} />{actions}</div>
    <div className="auth-layout">
      <section className="auth-introduction" aria-label="Welcome">
        <Badge variant="outline">{control ? "Service administration" : "Your memory workspace"}</Badge>
        <h1>{control ? "A clear view of your memory service." : "Find the context that matters."}</h1>
        <p>{control ? "Manage teams, access, and memory operations in one workspace." : "Explore evidence, follow connections, and understand what your team knows."}</p>
        <div className="auth-provenance" aria-hidden="true"><span>Evidence</span><i /><span>Relationships</span><i /><span>Context</span></div>
      </section>
      <section className="auth-panel" aria-label={title}>
        <div><h2>{control ? "Sign in to Control" : "Sign in to Knowledge"}</h2><p className="section-subtitle">{control ? "Use your control token or an available identity provider." : "Use an API key or your organization’s sign-in."}</p></div>
        <form onSubmit={onSubmit}>{children}</form>
      </section>
    </div>
    <p className="auth-footer">Dense-Mem · Evidence with context</p>
  </main>;
}

export function PortalShell({ theme, title, icon, topbarActions, navLabel, navItemsLabel, navItems, workspacePicker, contextBar, detailLabel, error, children }: PortalShellProps) {
  const storageKey = title.includes("Control") ? "denseMem.controlSidebar" : "denseMem.userSidebar";
  const [collapsed, setCollapsed] = useState(() => localStorage.getItem(storageKey) === "collapsed");
  const [mobileOpen, setMobileOpen] = useState(false);
  const active = navItems.find((item) => item.active);
  const groups = [...new Set(navItems.map((item) => item.group ?? "Workspace"))];
  function toggleSidebar() { setCollapsed((current) => { localStorage.setItem(storageKey, current ? "expanded" : "collapsed"); return !current; }); }
  const navigation = <nav className="rail-tabs" aria-label={navItemsLabel}>{groups.map((group) => <div className="nav-group" key={group}><p className="nav-group-label">{group}</p>{navItems.filter((item) => (item.group ?? "Workspace") === group).map((item) => <TabButton key={item.id} {...item} onClick={async () => { const result = await item.onClick(); if (result !== false) setMobileOpen(false); }} />)}</div>)}</nav>;
  const brand = <Brand title={title} icon={icon} />;
  return <TooltipProvider delayDuration={250}><main className="app-shell" data-theme={theme} data-sidebar-collapsed={collapsed}>
    <a className="skip-link" href="#portal-content">Skip to content</a>
    <aside className="primary-rail" aria-label={navLabel}>
      <div className="rail-brand">{brand}</div>
      {workspacePicker && <div className="sidebar-workspace">{workspacePicker}</div>}
      {navigation}
      <div className="sidebar-footer"><span>{title.includes("Control") ? "Service workspace" : "Memory workspace"}</span><Button variant="ghost" size="icon" onClick={toggleSidebar} aria-label={collapsed ? "Expand navigation" : "Collapse navigation"} aria-expanded={!collapsed}>{collapsed ? <PanelLeftOpen aria-hidden="true" /> : <PanelLeftClose aria-hidden="true" />}</Button></div>
    </aside>
    <div className="portal-main">
      <header className="topbar">
        <Button className="mobile-nav-trigger" variant="ghost" size="icon" aria-label="Open navigation" onClick={() => setMobileOpen(true)}><Menu aria-hidden="true" /></Button>
        <div className="topbar-context">{contextBar}<span className="breadcrumb-separator" aria-hidden="true">/</span><strong>{active?.label ?? "Workspace"}</strong></div>
        <div className="topbar-actions">{topbarActions}</div>
      </header>
      <div className="portal-content" id="portal-content" tabIndex={-1}>
        {error && <div className="banner error" role="alert">{error}</div>}
        <section className="detail-pane" aria-label={detailLabel}>{children}</section>
      </div>
    </div>
    <Sheet open={mobileOpen} onOpenChange={setMobileOpen}><SheetContent className="mobile-navigation" side="left"><SheetHeader className="sr-only"><SheetTitle>{navLabel}</SheetTitle><SheetDescription>Choose a section of your workspace.</SheetDescription></SheetHeader><div className="rail-brand">{brand}</div>{error && <div className="banner error" role="alert">{error}</div>}{workspacePicker && <div className="sidebar-workspace">{workspacePicker}</div>}{navigation}</SheetContent></Sheet>
  </main></TooltipProvider>;
}

export function TabButton({ active, disabled, icon, label, href, onClick }: { active: boolean; disabled?: boolean; icon: ReactNode; label: string; href?: string; onClick: () => void | Promise<unknown> }) {
  return <a className={active ? "rail-tab-button active" : "rail-tab-button"} href={disabled ? undefined : href ?? portalHref({ section: label.toLowerCase().replaceAll(" ", "-") })} aria-current={active ? "page" : undefined} aria-disabled={disabled || undefined} tabIndex={disabled ? -1 : undefined} title={label} onClick={(event) => { if (!event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey && event.button === 0) { event.preventDefault(); if (!disabled) void onClick(); } }}>{icon}<span>{label}</span></a>;
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

export function LoadingState({ label = "Loading", compact = false }: LoadingStateProps) {
  return (
    <div
      className={compact ? "table-placeholder loading-state compact" : "table-placeholder loading-state"}
      role="status"
      aria-live="polite"
    >
      <span className="loading-orbit" aria-hidden="true" />
      <span className="loading-copy">{label}</span>
      <span className="loading-skeleton" aria-hidden="true">
        <span />
        <span />
        <span />
      </span>
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
      <Input
        ref={inputRef}
        className="secret-value"
        value={secret}
        readOnly
        aria-label={valueLabel}
        spellCheck={false}
        onFocus={(event) => event.currentTarget.select()}
      />
      <div className="secret-actions">
        <Button variant="ghost" size="icon" className="icon-button" type="button" aria-label={copyLabel} onClick={() => void copySecret()}>
          {copied ? <Check size={17} aria-hidden="true" /> : <Copy size={17} aria-hidden="true" />}
        </Button>
        <Button variant="ghost" size="icon" className="icon-button" type="button" aria-label={dismissLabel} onClick={onDismiss}>
          <X size={17} aria-hidden="true" />
        </Button>
      </div>
    </div>
  );
}

export function InfoTooltip({ label, children }: InfoTooltipProps) {
  const [open, setOpen] = useState(false);
  return <TooltipProvider delayDuration={150}><Tooltip open={open} onOpenChange={setOpen}>
    <TooltipTrigger asChild><Button type="button" variant="ghost" size="icon-sm" className="info-tooltip-trigger" aria-label={label} onClick={(event) => {
      // Radix dismisses tooltips on click, but info buttons must also open on touch.
      event.preventDefault();
      setOpen(true);
    }}><Info size={14} aria-hidden="true" /></Button></TooltipTrigger>
    <TooltipContent className="info-tooltip-content" sideOffset={6}>{children}</TooltipContent>
  </Tooltip></TooltipProvider>;
}

export async function writeClipboardText(text: string, fallbackInput: HTMLInputElement | null): Promise<boolean> {
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

export function FieldRow({ label, htmlFor, description, children }: FieldRowProps) {
  return (
    <div className="setting-row">
      <div className="setting-label"><Label htmlFor={htmlFor}>{label}</Label>{description && <p id={htmlFor ? `${htmlFor}-help` : undefined}>{description}</p>}</div>
      <div className="field-control">{children}</div>
    </div>
  );
}

export function UsageMeter({ label, value, limit, detail }: { label: string; value: number; limit: number; detail?: ReactNode }) {
  const percent = limit > 0 ? Math.max(0, Math.min(100, value / limit * 100)) : null;
  return <div className="usage-meter">
    <div className="usage-meter-heading"><span>{label}</span><strong>{value.toLocaleString()} <span>/ {limit.toLocaleString()}</span></strong></div>
    {percent !== null && <div className="usage-meter-track" role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent} aria-valuetext={`${value.toLocaleString()} of ${limit.toLocaleString()}`}><span style={{ width: `${percent}%` }} /></div>}
    {detail && <p className="form-meta">{detail}</p>}
  </div>;
}
