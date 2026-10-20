/** @type {import('tailwindcss').Config} */
// Colors come from CSS variables (so light and dark themes swap cleanly). The helper
// makes opacity modifiers such as bg-brick/10 work by mixing the variable with transparent.
const c = (name) => ({ opacityValue }) =>
  opacityValue === undefined
    ? `var(--${name})`
    : `color-mix(in srgb, var(--${name}) calc(${opacityValue} * 100%), transparent)`

export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  darkMode: ['class', '[data-theme="dark"]'],
  theme: {
    extend: {
      colors: {
        bg: c('bg'), surface: c('surface'), sunken: c('sunken'), ink: c('ink'), muted: c('muted'), line: c('line'),
        marine: c('marine'), 'marine-ink': c('marine-ink'), signal: c('signal'), brick: c('brick'), pine: c('pine'), iris: c('iris'),
      },
      fontFamily: {
        sans: ['"Public Sans Variable"', 'ui-sans-serif', 'system-ui', 'sans-serif'],
        mono: ['"JetBrains Mono Variable"', 'ui-monospace', 'SFMono-Regular', 'monospace'],
      },
      boxShadow: { lift: '0 1px 0 var(--line), 0 8px 24px -12px rgba(8, 30, 36, 0.25)' },
    },
  },
  plugins: [],
}
