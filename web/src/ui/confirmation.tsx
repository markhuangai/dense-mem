import { createContext, ReactNode, useContext, useRef, useState } from "react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "./primitives/alert-dialog";

export type Confirmation = { title: string; description: string; confirmLabel?: string; destructive?: boolean };
type Confirm = (options: Confirmation) => Promise<boolean>;
const ConfirmationContext = createContext<Confirm>((options) => Promise.resolve(window.confirm(options.description)));

export function ConfirmationProvider({ children }: { children: ReactNode }) {
  const [options, setOptions] = useState<Confirmation | null>(null);
  const pending = useRef<((confirmed: boolean) => void) | null>(null);

  function finish(confirmed: boolean) {
    pending.current?.(confirmed);
    pending.current = null;
    setOptions(null);
  }

  const confirm = useRef<Confirm>((next) => {
    if (pending.current) return Promise.resolve(false);
    setOptions(next);
    return new Promise<boolean>((resolve) => { pending.current = resolve; });
  }).current;

  return <ConfirmationContext.Provider value={confirm}>
    {children}
    <AlertDialog open={options !== null} onOpenChange={(open) => { if (!open) finish(false); }}>
      <AlertDialogContent>
        <AlertDialogHeader><AlertDialogTitle>{options?.title}</AlertDialogTitle><AlertDialogDescription>{options?.description}</AlertDialogDescription></AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel onClick={() => finish(false)}>Cancel</AlertDialogCancel>
          <AlertDialogAction className={options?.destructive ? "destructive-action" : undefined} onClick={() => finish(true)}>{options?.confirmLabel ?? "Continue"}</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  </ConfirmationContext.Provider>;
}

export function useConfirmation() {
  return useContext(ConfirmationContext);
}
