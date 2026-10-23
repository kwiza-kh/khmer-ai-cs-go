# MekongChat frontend

Next.js 16 App Router console for the Go backend in `../backend-go`. Production
runs the standalone server (`node server.js`) behind nginx on `:3001`.

## Commands

```bash
npm install
npm run dev            # local; API defaults to localhost
npm run lint

# build: NEXT_PUBLIC_API_URL is baked into the bundle at build time
NEXT_PUBLIC_API_URL=https://mekongchat.com/api/v1 npm run build
```

Omitting `NEXT_PUBLIC_API_URL` at build time makes the deployed console call
`localhost` — see `../AGENTS.md`. Release steps live in
`../deploy-khmer-ai-cs/SKILL.md`.

## Layout

| Path | What lives there |
|---|---|
| `src/app` | routes: inbox, knowledge, admin, platform-admin, widget, auth |
| `src/components/ui` | shadcn/base-ui primitives |
| `src/lib/api.ts` | the typed API client (every backend call goes through it) |
| `src/lib/i18n` | km / en / zh dictionaries; key sets must stay identical |

There are no frontend unit tests yet (CI builds only). `npm run lint` must stay
at 0 errors.
