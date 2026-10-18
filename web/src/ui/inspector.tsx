import { ReactNode, useEffect, useRef, useState } from "react";
import { X } from "lucide-react";
import { Button } from "./primitives/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "./primitives/sheet";

export function useCompactWorkspace(minimumWidth = 1040) {
  const ref = useRef<HTMLElement>(null);
  const [compact, setCompact] = useState(true);
  useEffect(() => {
    if (!ref.current) return;
    const measure = () => {
      if (ref.current) setCompact(ref.current.getBoundingClientRect().width < minimumWidth);
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(ref.current);
    return () => observer.disconnect();
  }, [minimumWidth]);
  return { ref, compact };
}

export function ResponsiveInspector({ compact, open, onOpenChange, title, children }: {
  compact: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  children: ReactNode;
}) {
  const returnFocus = useRef<HTMLElement | null>(null);
  if (compact) return <Sheet open={open} onOpenChange={onOpenChange}>
    <SheetContent className="contextual-sheet" showCloseButton={false} onOpenAutoFocus={() => {
      returnFocus.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    }} onCloseAutoFocus={(event) => {
      event.preventDefault();
      if (returnFocus.current?.isConnected) returnFocus.current.focus();
      document.querySelector<HTMLElement>(".explorer-details-trigger")?.focus();
    }}>
      <SheetHeader><div className="inspector-head"><SheetTitle>{title}</SheetTitle><Button variant="ghost" size="icon" aria-label="Close details panel" onClick={() => onOpenChange(false)}><X size={16} /></Button></div><SheetDescription className="sr-only">Details for your current selection.</SheetDescription></SheetHeader>
      <div className="contextual-sheet-body">{children}</div>
    </SheetContent>
  </Sheet>;
  return open ? <aside className="contextual-inspector" aria-label={title}>
    <div className="inspector-head"><h2>{title}</h2><Button variant="ghost" size="icon" aria-label="Close details panel" onClick={() => onOpenChange(false)}><X size={16} /></Button></div>
    {children}
  </aside> : null;
}
