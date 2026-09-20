# RelayChat Design System

Design tokens, component conventions, and visual guidelines for the RelayChat frontend.

## Color Tokens

All colors use OKLCH and are defined as CSS custom properties in `globals.css`.

### Core Palette

| Token | Light | Dark | Usage |
|-------|-------|------|-------|
| `--primary` | `oklch(0.22 0.02 270)` | `oklch(0.92 0.01 270)` | Primary actions, active states |
| `--brand` | `oklch(0.45 0.18 270)` | `oklch(0.65 0.18 270)` | Logo accent, brand highlights, sidebar active icons |
| `--brand-muted` | `oklch(0.45 0.18 270 / 12%)` | `oklch(0.65 0.18 270 / 15%)` | Subtle brand backgrounds (onboarding empty states) |
| `--background` | `oklch(1 0 0)` | `oklch(0.145 0.005 270)` | Page background (dark has faint violet tint) |
| `--sidebar` | `oklch(0.985 0.003 270)` | `oklch(0.18 0.008 270)` | Sidebar background (slightly tinted vs main bg) |
| `--border` | `oklch(0.922 0 0)` | `oklch(1 0 0 / 13%)` | Borders (dark bumped from 10% → 13% for contrast) |

### Semantic Colors

| Token | Hue | Usage |
|-------|-----|-------|
| `--success` | 155 (green) | Resolved sessions, positive outcomes |
| `--warning` | 55 (amber) | Pending handoffs, caution states |
| `--info` | 250 (blue) | Active sessions, informational badges |
| `--danger` | 27 (red) | Errors, destructive actions, unread counts |

### Platform Brand Colors

Registered as Tailwind theme tokens (`bg-brand-*`, `text-brand-*`):
- Messenger: `#0084ff` · Instagram: `#e1306c` · Telegram: `#229ed9`
- WhatsApp: `#25d366` · LINE: `#06c755` · Zalo: `#0068ff`

## Typography Scale

Named tokens in `globals.css` — prefer these over arbitrary `text-[Npx]` values:

| Token | Size | Usage |
|-------|------|-------|
| `--text-label` | 11px | Captions, badges, section labels, nav section headers |
| `--text-caption` | 12px | Helper text, timestamps, metadata |
| `--text-body-sm` | 13px | Nav items, table cells, search input |
| `--text-body` | 14px | Default body text, form inputs |
| `--text-subtitle` | 15px | Card titles, brand name |
| `--text-title` | 18px | Page section titles |
| `--text-heading` | 20px | Page headers |
| `--text-display` | 24px | Hero / login title |

### Font Stack

```
Inter (latin) → Noto Sans Khmer (khmer) → system-ui → -apple-system → sans-serif
```

Khmer characters have ascenders/descenders that extend beyond Latin baselines. When setting line-height for Khmer-heavy content, use ≥1.7 to prevent clipping.

## Spacing & Layout

- **Sidebar width**: 224px (collapsed nav) / 232px (mobile overlay)
- **Inbox left panel**: 320px (`w-80`)
- **Inbox right panel**: 320px (`w-80`)
- **Content max-width**: `max-w-6xl` (dashboards), `max-w-3xl` (settings/forms)
- **Border radius base**: `0.625rem` (10px), with computed scale (`sm` through `4xl`)

## Visual Hierarchy in Inbox

Three-column workspace uses distinct background tones for separation:
1. **Left (session list)**: `bg-sidebar` — muted, clearly secondary
2. **Center (chat)**: `bg-background` — primary focus area, highest contrast
3. **Right (details)**: `bg-muted/20` — subtle tint,辅助 context

Message area uses `gap-3.5` and `py-5` for breathing room.

## Component Conventions

### EmptyState Variants

| Variant | Tint | Use when |
|---------|------|----------|
| `default` | Grey/muted | No results, empty lists |
| `error` | Red/danger | Failures, permission denied, network errors |
| `onboarding` | Brand/violet | First-use guidance, feature discovery |

### StatCard

Supports `loading` prop for skeleton state. Always pass `loading={true}` during initial data fetch instead of rendering nothing or a spinner.

### Animations

| Token | Duration | Easing | Use |
|-------|----------|--------|-----|
| `animate-fade-up` | 450ms | ease-out | Route transitions |
| `animate-fade-in` | 300ms | ease | Generic appearance |
| `animate-scale-in` | 200ms | ease-out | Dialogs, popovers |
| `animate-slide-in-right` | 300ms | ease-out | Panels, drawers |
| `animate-pulse-subtle` | 2s | ease-in-out | Loading skeletons |

## Accessibility

- Chat message area: `role="log"` + `aria-live="polite"`
- Global search: `role="searchbox"` + `aria-keyshortcuts="Meta+k Control+k"`
- Session detail panel: `aria-label="Session details"`
- Platform filter chips: `aria-pressed` + `role="group"`
- Status colors always paired with text labels (never color-only)
- All interactive elements reachable via keyboard (↑/↓ navigation in inbox)

## Dark Mode Notes

- Background and card surfaces carry a faint violet tint (`0.005` chroma at hue 270) to avoid pure-black flatness
- Border opacity increased to 13% (from default 10%) for adequate contrast
- Sidebar-primary uses same hue family (270) as light mode, adjusted lightness only
- Brand token shifts lighter in dark mode (`0.45` → `0.65` lightness)
