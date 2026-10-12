import { clsx, type ClassValue } from "clsx"
import { extendTailwindMerge } from "tailwind-merge"

// tailwind-merge 的默认配置只认识内置字号（text-sm…）。globals.css 里的
// 自定义字号 token（text-label / text-caption / text-body…）会被误当成
// 文字颜色，于是 `cn("text-primary-foreground", "text-caption")` 把颜色类
// 悄悄删掉 —— 实心主按钮因此回落到继承的 foreground，黑底黑字（对比度
// 1.14:1，2026-10-12 审查）。把字号登记进主题，字号与颜色不再互相吃掉。
const twMerge = extendTailwindMerge({
  extend: {
    theme: {
      text: ["label", "caption", "body-sm", "body", "subtitle", "title", "heading", "display", "hero"],
    },
  },
})

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
