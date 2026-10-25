import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'

const KEY = 'payoutrescue.tour'
const steps: { title: string; body: string; to: string }[] = [
  { title: 'Welcome to PayoutRescue', body: 'It watches your PayPal payouts, explains problems in plain words, and waits for a named person to approve anything that moves money. This is a 6-step tour; you can leave at any time.', to: '/' },
  { title: 'Payouts', body: 'Every payout is here. The sentence on top says how much money is in limbo and how many decisions wait for you. Click a row to review it.', to: '/' },
  { title: 'Approvals', body: 'Cancelling and replacing are the only actions that move money. Each needs your name, passing safety checks, and a fresh look at PayPal.', to: '/approvals' },
  { title: 'Conflicts', body: 'When the evidence disagrees (two emails in a reply, a claim during a cancel), the payout lands here for a person to decide. Resolving never sends money by itself.', to: '/conflicts' },
  { title: 'Batches and demo data', body: 'Create a batch, import a CSV, or load the demo data and the conflict pack to explore.', to: '/batches' },
  { title: 'Help, anywhere', body: 'Press ? for help about the page you are on, Ctrl+K to jump anywhere, and open Help and guide for everything, including every demo payout.', to: '/help' },
]

export function Tour() {
  const nav = useNavigate()
  const [i, setI] = useState<number | null>(() => (localStorage.getItem(KEY) ? null : 0))

  useEffect(() => {
    const restart = () => setI(0)
    window.addEventListener('payoutrescue:tour', restart)
    return () => window.removeEventListener('payoutrescue:tour', restart)
  }, [])

  const finish = useCallback(() => { localStorage.setItem(KEY, 'done'); setI(null) }, [])
  const go = (n: number) => { setI(n); nav(steps[n].to) }
  if (i === null) return null
  const s = steps[i]
  return (
    <div role="dialog" aria-label="Guided tour" className="rise fixed bottom-4 left-1/2 z-50 w-[min(520px,94vw)] -translate-x-1/2 rounded-lg border border-marine bg-surface p-4 shadow-lift print:hidden md:left-[calc(50%+118px)]">
      <p className="text-[12px] font-semibold text-muted">Step {i + 1} of {steps.length}</p>
      <h2 className="text-lg font-bold">{s.title}</h2>
      <p className="mt-1">{s.body}</p>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        {i > 0 && <button className="btn btn-quiet" onClick={() => go(i - 1)}>Back</button>}
        {i < steps.length - 1 ? <button className="btn btn-primary" onClick={() => go(i + 1)}>Next</button> : <button className="btn btn-primary" onClick={finish}>Finish</button>}
        <button className="btn btn-quiet ml-auto" onClick={finish}>Skip the tour</button>
      </div>
    </div>
  )
}
