import { Button as ButtonPrimitive } from "@base-ui/react/button"
import { cva, type VariantProps } from "class-variance-authority"

import { cn } from "@/lib/utils"

const buttonVariants = cva(
  "group/button inline-flex shrink-0 items-center justify-center rounded-lg border border-transparent bg-clip-padding text-sm font-medium whitespace-nowrap transition-all outline-none select-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 active:not-aria-[haspopup]:translate-y-px disabled:pointer-events-none disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-3 aria-invalid:ring-destructive/20 dark:aria-invalid:border-destructive/50 dark:aria-invalid:ring-destructive/40 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
  {
    variants: {
      variant: {
        default:
          "bg-[linear-gradient(180deg,color-mix(in_oklch,var(--color-primary),white_10%),var(--color-primary)_52%,color-mix(in_oklch,var(--color-primary),black_14%))] text-primary-foreground shadow-[0_1px_2px_rgb(0_0_0/0.14),inset_0_1px_0_rgb(255_255_255/0.22),0_6px_18px_-6px_color-mix(in_oklch,var(--color-primary)_55%,transparent)] hover:brightness-[1.07] hover:shadow-[0_2px_4px_rgb(0_0_0/0.12),inset_0_1px_0_rgb(255_255_255/0.22),0_10px_26px_-8px_color-mix(in_oklch,var(--color-primary)_65%,transparent)] active:scale-[0.985]",
        outline:
          "border-border bg-card/70 shadow-[0_1px_2px_rgb(0_0_0/0.04),inset_0_1px_0_rgb(255_255_255/0.6)] hover:bg-muted/80 hover:text-foreground hover:shadow-[0_2px_6px_-1px_rgb(0_0_0/0.08)] active:scale-[0.985] aria-expanded:bg-muted aria-expanded:text-foreground dark:border-white/10 dark:bg-white/[0.04] dark:shadow-none dark:hover:bg-white/[0.08] dark:active:scale-[0.985]",
        secondary:
          "bg-secondary text-secondary-foreground shadow-[inset_0_1px_0_rgb(255_255_255/0.5)] hover:bg-[color-mix(in_oklch,var(--secondary),var(--foreground)_5%)] active:scale-[0.985] aria-expanded:bg-secondary aria-expanded:text-secondary-foreground dark:shadow-none",
        ghost:
          "hover:bg-foreground/[0.06] hover:text-foreground active:scale-[0.985] aria-expanded:bg-muted aria-expanded:text-foreground dark:hover:bg-white/[0.07]",
        destructive:
          "bg-destructive/10 text-destructive hover:bg-destructive/20 focus-visible:border-destructive/40 focus-visible:ring-destructive/20 dark:bg-destructive/20 dark:hover:bg-destructive/30 dark:focus-visible:ring-destructive/40",
        link: "text-primary underline-offset-4 hover:underline",
      },
      size: {
        default:
          "h-8 gap-1.5 px-2.5 has-data-[icon=inline-end]:pr-2 has-data-[icon=inline-start]:pl-2",
        xs: "h-6 gap-1 rounded-[min(var(--radius-md),10px)] px-2 text-xs in-data-[slot=button-group]:rounded-lg has-data-[icon=inline-end]:pr-1.5 has-data-[icon=inline-start]:pl-1.5 [&_svg:not([class*='size-'])]:size-3",
        sm: "h-7 gap-1 rounded-[min(var(--radius-md),12px)] px-2.5 text-[0.8rem] in-data-[slot=button-group]:rounded-lg has-data-[icon=inline-end]:pr-1.5 has-data-[icon=inline-start]:pl-1.5 [&_svg:not([class*='size-'])]:size-3.5",
        lg: "h-9 gap-1.5 px-2.5 has-data-[icon=inline-end]:pr-2 has-data-[icon=inline-start]:pl-2",
        icon: "size-8",
        "icon-xs":
          "size-6 rounded-[min(var(--radius-md),10px)] in-data-[slot=button-group]:rounded-lg [&_svg:not([class*='size-'])]:size-3",
        "icon-sm":
          "size-7 rounded-[min(var(--radius-md),12px)] in-data-[slot=button-group]:rounded-lg",
        "icon-lg": "size-9",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  }
)

function Button({
  className,
  variant = "default",
  size = "default",
  ...props
}: ButtonPrimitive.Props & VariantProps<typeof buttonVariants>) {
  return (
    <ButtonPrimitive
      data-slot="button"
      className={cn(buttonVariants({ variant, size, className }))}
      {...props}
    />
  )
}

export { Button, buttonVariants }
