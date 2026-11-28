import * as React from "react"
import { cn } from "../classnames"

function Input({ className, type, inputMode, ...props }: React.ComponentProps<"input">) {
  return (
    <input
      type={type}
      inputMode={inputMode}
      data-slot="input"
      data-field-size={type === "number" || inputMode === "numeric" || inputMode === "decimal" ? "number" : type === "time" ? "time" : type?.startsWith("date") ? "date" : undefined}
      className={cn(
        "h-8.5 w-full min-w-0 rounded-md border border-input bg-card px-3 py-1 text-base selection:bg-primary selection:text-primary-foreground file:inline-flex file:h-7 file:border-0 file:bg-transparent file:text-sm file:font-medium file:text-foreground placeholder:text-muted-foreground disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50 md:text-sm",
        "data-[field-size=number]:w-[min(7.5rem,100%)] data-[field-size=time]:w-[min(7.5rem,100%)] data-[field-size=date]:w-[min(17rem,100%)]",
        "focus-visible:border-ring focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-ring",
        "aria-invalid:border-destructive aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40",
        className
      )}
      {...props}
    />
  )
}

export { Input }
