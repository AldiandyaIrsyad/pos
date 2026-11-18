import { Navigate, NavLink, Route, Routes } from 'react-router'
import CashierPage from './cashier/CashierPage'
import ReportsPage from './reports/ReportsPage'

const tabClass = ({ isActive }: { isActive: boolean }) =>
  `rounded px-3 py-1.5 text-sm font-medium ${
    isActive ? 'bg-slate-900 text-white' : 'text-slate-600 hover:bg-slate-200'
  }`

export default function App() {
  return (
    <div className="min-h-screen bg-slate-100 text-slate-900">
      <header className="border-b border-slate-200 bg-white">
        <nav className="mx-auto flex max-w-6xl items-center gap-2 px-4 py-3">
          <span className="mr-4 font-semibold">POS</span>
          <NavLink to="/cashier" className={tabClass}>
            Cashier
          </NavLink>
          <NavLink to="/reports" className={tabClass}>
            Reports
          </NavLink>
        </nav>
      </header>
      <main className="mx-auto max-w-6xl px-4 py-6">
        <Routes>
          <Route path="/cashier" element={<CashierPage />} />
          <Route path="/reports" element={<ReportsPage />} />
          <Route path="*" element={<Navigate to="/cashier" replace />} />
        </Routes>
      </main>
    </div>
  )
}
