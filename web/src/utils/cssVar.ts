/**
 * Read the current value of a CSS custom property.
 *
 * canvas, chart.js and xterm.js take real colour strings and do not resolve `var()`,
 * so colours handed to them have to be looked up here instead of written as `var(--token)`.
 * Everything that ends up in CSS (including `:style` bindings and injected stylesheets)
 * should use `var(--token)` directly.
 */
export const cssVar = (name: string, fallback = ''): string => {
    if (typeof document === 'undefined') return fallback
    const value = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
    return value || fallback
}
