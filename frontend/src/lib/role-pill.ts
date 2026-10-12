// 角色胶囊样式 —— 用户表、个人资料卡（/settings）共用这一份，同一个角色
// 在全站永远同色（2026-10-12 审查: 资料卡的 platform_admin 还是浅蓝 info，
// 与用户表的 ink 胶囊不一致）。
//
// 最高权限用 primary 实心胶囊, 不再借用 warning(琥珀) —— 那会让"最高权限"
// 读成"警告"。
//
// ⚠️ 用在 SelectTrigger 上时必须成对写 light / dark: SelectTrigger 基类带
// `dark:bg-input/30`, 它在构建产物里的顺序晚于基础 `bg-*`, 深色下会直接盖掉
// 只写了 light 的写法 (曾导致 platform_admin 变成深底 + 深字, 对比度 1.01:1
// 完全不可见)。
export const ROLE_PILL: Record<string, string> = {
  platform_admin: "bg-primary text-primary-foreground [&_svg]:text-primary-foreground dark:bg-primary dark:text-primary-foreground",
  admin: "bg-primary/12 text-primary dark:bg-primary/15 dark:text-primary",
  user: "bg-muted text-muted-foreground dark:bg-muted/60 dark:text-muted-foreground",
};
