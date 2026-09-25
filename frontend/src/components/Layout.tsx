import { useEffect, useState } from 'react'
import { Outlet, Link, useLocation, useNavigate } from 'react-router-dom'
import { useAuthStore } from '../store/authStore'
import {
  LayoutDashboard,
  FolderOpen,
  User,
  Shield,
  Settings,
  LogOut,
  Database,
  Server,
  Menu,
  X,
} from 'lucide-react'

interface NavItem {
  path: string
  icon: typeof LayoutDashboard
  label: string
}

export default function Layout() {
  const { user, logout } = useAuthStore()
  const location = useLocation()
  const navigate = useNavigate()
  // Mobile (< md) navigation drawer; on md+ the sidebar is always visible.
  const [navOpen, setNavOpen] = useState(false)
  const closeNav = () => setNavOpen(false)

  useEffect(() => {
    if (!navOpen) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setNavOpen(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [navOpen])

  const handleLogout = async () => {
    await logout()
    navigate('/login')
  }

  const generalNav: NavItem[] = [
    { path: '/', icon: LayoutDashboard, label: 'Dashboard' },
    { path: '/buckets', icon: FolderOpen, label: 'Buckets' },
    { path: '/profile', icon: User, label: 'Profile' },
  ]

  const adminNav: NavItem[] = user?.is_admin
    ? [
        { path: '/policies', icon: Shield, label: 'Policies' },
        { path: '/s3-configs', icon: Server, label: 'S3 Configs' },
        { path: '/admin', icon: Settings, label: 'Admin' },
      ]
    : []

  const isActive = (path: string) =>
    path === '/'
      ? location.pathname === '/'
      : location.pathname === path || location.pathname.startsWith(path + '/')

  const renderItem = (item: NavItem) => {
    const Icon = item.icon
    const active = isActive(item.path)
    return (
      <li key={item.path}>
        <Link
          to={item.path}
          onClick={closeNav}
          aria-current={active ? 'page' : undefined}
          className={`group relative flex items-center gap-3 px-3 py-2 rounded-lg text-sm font-medium transition-colors duration-150 ${
            active
              ? 'bg-accent-soft text-blue-400'
              : 'text-dark-textSecondary hover:bg-dark-surfaceHover hover:text-dark-text'
          }`}
        >
          {active && (
            <span className="absolute left-0 top-1/2 -translate-y-1/2 w-0.5 h-5 rounded-full bg-blue-500" />
          )}
          <Icon
            className={`w-[18px] h-[18px] shrink-0 ${
              active ? 'text-blue-400' : 'text-dark-textMuted group-hover:text-dark-textSecondary'
            }`}
          />
          <span>{item.label}</span>
        </Link>
      </li>
    )
  }

  return (
    <div className="flex h-screen bg-dark-bg">
      {/* Mobile backdrop behind the open drawer */}
      {navOpen && (
        <div
          className="fixed inset-0 z-30 bg-black/60 md:hidden"
          onClick={closeNav}
          aria-hidden="true"
        />
      )}

      {/* Sidebar: static on md+, slide-in drawer below md */}
      <aside
        id="app-sidebar"
        className={`fixed inset-y-0 left-0 z-40 w-60 bg-dark-surface border-r border-dark-border flex flex-col transform transition-transform duration-200 md:static md:translate-x-0 ${
          navOpen ? 'translate-x-0' : '-translate-x-full'
        }`}
      >
        <Link
          to="/"
          onClick={closeNav}
          className="flex items-center gap-2.5 px-5 h-16 border-b border-dark-border shrink-0"
        >
          <span className="flex items-center justify-center w-8 h-8 rounded-lg bg-blue-600/15">
            <Database className="w-[18px] h-[18px] text-blue-500" />
          </span>
          <span className="leading-tight">
            <span className="block text-[15px] font-semibold text-dark-text tracking-tight">bkt</span>
            <span className="block text-[11px] text-dark-textMuted">Object Storage</span>
          </span>
        </Link>

        <nav className="flex-1 overflow-y-auto px-3 py-4">
          <ul className="space-y-1">{generalNav.map(renderItem)}</ul>

          {adminNav.length > 0 && (
            <>
              <p className="px-3 mt-6 mb-2 text-[11px] font-medium uppercase tracking-wider text-dark-textMuted">
                Administration
              </p>
              <ul className="space-y-1">{adminNav.map(renderItem)}</ul>
            </>
          )}
        </nav>

        <div className="p-3 border-t border-dark-border shrink-0">
          <div className="flex items-center gap-3 px-2 py-2">
            <div className="w-8 h-8 rounded-full bg-linear-to-br from-blue-500 to-blue-700 flex items-center justify-center text-white text-sm font-semibold shrink-0">
              {user?.username.charAt(0).toUpperCase()}
            </div>
            <div className="flex-1 min-w-0">
              <p className="text-sm font-medium text-dark-text truncate leading-tight">
                {user?.username}
                {user?.is_admin && (
                  <span className="ml-1.5 align-middle badge-blue text-[10px]! px-1.5! py-0!">admin</span>
                )}
              </p>
              <p className="text-xs text-dark-textMuted truncate">{user?.email}</p>
            </div>
            <button
              onClick={handleLogout}
              title="Sign out"
              className="btn-icon hover:text-red-400! hover:bg-red-500/10!"
            >
              <LogOut className="w-4 h-4" />
            </button>
          </div>
        </div>
      </aside>

      {/* Main content */}
      <div className="flex-1 flex flex-col min-w-0">
        {/* Mobile top bar */}
        <header className="md:hidden flex items-center gap-3 h-14 px-4 border-b border-dark-border bg-dark-surface shrink-0">
          <button
            type="button"
            onClick={() => setNavOpen((o) => !o)}
            className="btn-icon"
            aria-label={navOpen ? 'Close menu' : 'Open menu'}
            aria-controls="app-sidebar"
            aria-expanded={navOpen}
          >
            {navOpen ? <X className="w-5 h-5" /> : <Menu className="w-5 h-5" />}
          </button>
          <Link to="/" onClick={closeNav} className="flex items-center gap-2">
            <Database className="w-[18px] h-[18px] text-blue-500" />
            <span className="text-[15px] font-semibold text-dark-text tracking-tight">bkt</span>
          </Link>
        </header>
        <main className="flex-1 overflow-auto">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
