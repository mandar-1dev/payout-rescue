export function Mark({ size = 28 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" aria-hidden="true">
      <rect width="32" height="32" rx="8" fill="var(--marine)" />
      <path d="M6 20h20" stroke="var(--marine-ink)" strokeWidth="2.4" strokeLinecap="round" opacity=".45" />
      <path d="M6 20h13" stroke="#F2B544" strokeWidth="2.4" strokeLinecap="round" />
      <circle cx="19" cy="20" r="3.2" fill="var(--marine-ink)" />
      <path d="M9 12.5c2-3 5-4.5 8-4.5s5 1 6.5 2.5" fill="none" stroke="var(--marine-ink)" strokeWidth="2" strokeLinecap="round" opacity=".7" />
    </svg>
  )
}
