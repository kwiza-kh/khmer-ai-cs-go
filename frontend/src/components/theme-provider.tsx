"use client";

import * as React from "react";
import { ThemeProvider as NextThemesProvider } from "next-themes";

type ThemeProviderProps = React.ComponentProps<typeof NextThemesProvider>;

/**
 * Wraps next-themes so the rest of the app can use `useTheme()`.
 * attribute="class" toggles the `.dark` class on <html>, which globals.css
 * keys off of. `disableTransitionOnChange` avoids the 150ms color transition
 * flashing when the theme swaps.
 */
export function ThemeProvider({ children, ...props }: ThemeProviderProps) {
  return <NextThemesProvider {...props}>{children}</NextThemesProvider>;
}
