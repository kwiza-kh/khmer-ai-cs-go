import * as React from "react"
import { Input as InputPrimitive } from "@base-ui/react/input"

import { cn } from "@/lib/utils"

function Input({ className, type, ...props }: React.ComponentProps<"input">) {
  return (
    <InputPrimitive
      type={type}
      data-slot="input"
      className={cn(
        "h-8 w-full min-w-0 rounded-lg border border-input bg-card/80 px-2.5 py-1 text-base shadow-[inset_0_1px_2px_rgb(0_0_0/0.03)] transition-all outline-none file:inline-flex file:h-6 file:border-0 file:bg-transparent file:text-sm file:font-medium file:text-foreground placeholder:text-muted-foreground/60 hover:border-ring/40 focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/30 focus-visible:shadow-[0_0_0_1px_color-mix(in_oklch,var(--color-ring)_35%,transparent),0_4px_14px_-6px_color-mix(in_oklch,var(--color-ring)_45%,transparent)] disabled:pointer-events-none disabled:cursor-not-allowed disabled:bg-input/50 disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-3 aria-invalid:ring-destructive/20 md:text-sm dark:border-white/10 dark:bg-input/45 dark:shadow-none dark:placeholder:text-muted-foreground/70 dark:disabled:bg-input/70 dark:aria-invalid:border-destructive/50 dark:aria-invalid:ring-destructive/40",
        className
      )}
      {...props}
    />
  )
}

export { Input }
