import { mergeProps } from "@base-ui/react/merge-props"
import { useRender } from "@base-ui/react/use-render"
import { cva, type VariantProps } from "class-variance-authority"

import { cn } from "@/lib/utils"

const badgeVariants = cva(
  "group/badge inline-flex h-5 w-fit shrink-0 items-center justify-center gap-1 overflow-hidden rounded-4xl border border-transparent px-2 py-0 text-xs leading-none font-medium whitespace-nowrap transition-all focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 has-data-[icon=inline-end]:pr-1.5 has-data-[icon=inline-start]:pl-1.5 aria-invalid:border-destructive aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 [&>svg]:pointer-events-none [&>svg]:size-3!",
  {
    variants: {
      variant: {
        default:
          "bg-[linear-gradient(120deg,var(--color-primary),color-mix(in_oklch,var(--color-primary),hsl(214_92%_55%)_42%))] text-primary-foreground shadow-[0_2px_8px_-2px_color-mix(in_oklch,var(--color-primary)_50%,transparent)] [a]:hover:brightness-110",
        secondary:
          "bg-secondary text-secondary-foreground [a]:hover:bg-secondary/80",
        destructive:
          "bg-danger/12 text-danger focus-visible:ring-destructive/20 dark:bg-danger/18 dark:focus-visible:ring-destructive/40 [a]:hover:bg-danger/20",
        // 语义变体: 用 token 替换散落在各页面的 emerald-400 / amber-400 / blue-400 写法.
        success:
          "bg-success/15 text-success border-success/20 [a]:hover:bg-success/25 dark:bg-success/20",
        warning:
          "bg-warning/15 text-warning border-warning/20 [a]:hover:bg-warning/25 dark:bg-warning/20",
        info:
          "bg-info/15 text-info border-info/20 [a]:hover:bg-info/25 dark:bg-info/20",
        outline:
          "border-border text-foreground [a]:hover:bg-muted [a]:hover:text-muted-foreground",
        ghost:
          "hover:bg-muted hover:text-muted-foreground dark:hover:bg-muted/50",
        link: "text-primary underline-offset-4 hover:underline",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  }
)

function Badge({
  className,
  variant = "default",
  render,
  ...props
}: useRender.ComponentProps<"span"> & VariantProps<typeof badgeVariants>) {
  return useRender({
    defaultTagName: "span",
    props: mergeProps<"span">(
      {
        className: cn(badgeVariants({ variant }), className),
      },
      props
    ),
    render,
    state: {
      slot: "badge",
      variant,
    },
  })
}

export { Badge, badgeVariants }
