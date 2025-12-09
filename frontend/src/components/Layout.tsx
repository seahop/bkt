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
  Server
} from 'lucide-react'

export default function Layout() {
  const { user, logout } = useAuthStore()
  const location = useLocation()
  const navigate = useNavigate()

  const handleLogout = async () => {
    await logout()
    navigate('/login')
  }

  const navItems = [
    { path: '/', icon: LayoutDashboard, label: 'Dashboard' },
    { path: '/buckets', icon: FolderOpen, label: 'Buckets' },
    { path: '/profile', icon: User, label: 'Profile' },
  ]

  // Admin-only pages
  if (user?.is_admin) {
    navItems.push(
      { path: '/policies', icon: Shield, label: 'Policies' },
      { path: '/s3-configs', icon: Server, label: 'S3 Configs' },
      { path: '/admin', icon: Settings, label: 'Admin' }
    )
  }

  return (
    <div className="flex h-screen bg-dark-bg">
      {/* Sidebar */}
      <aside className="w-64 bg-dark-surface border-r border-dark-border flex flex-col">
        <div className="p-6 border-b border-dark-border">
          <div className="flex items-center gap-2">
            <Database className="w-8 h-8 text-blue-500" />
            <h1 className="text-xl font-bold text-dark-text">bkt</h1>
          </div>
          <p className="text-sm text-dark-textSecondary mt-1">S3-Compatible Storage</p>
        </div>

        <nav className="flex-1 p-4">
          <ul className="space-y-2">
            {navItems.map((item) => {
              const Icon = item.icon
              const isActive = location.pathname === item.path
              return (
                <li key={item.path}>
                  <Link
                    to={item.path}
                    className={`flex items-center gap-3 px-4 py-3 rounded-lg transition-colors ${
                      isActive
                        ? 'bg-blue-600 text-white'
                        : 'text-dark-textSecondary hover:bg-dark-surfaceHover hover:text-dark-text'
                    }`}
                  >
                    <Icon className="w-5 h-5" />
                    <span>{item.label}</span>
                  </Link>
                </li>
              )
            })}
          </ul>
        </nav>

        <div className="p-4 border-t border-dark-border">
          <div className="flex items-center gap-3 px-4 py-2">
            <div className="w-10 h-10 bg-blue-600 rounded-full flex items-center justify-center text-white font-semibold">
              {user?.username.charAt(0).toUpperCase()}
            </div>
            <div className="flex-1 min-w-0">
              <p className="text-sm font-medium text-dark-text truncate">{user?.username}</p>
              <p className="text-xs text-dark-textSecondary truncate">{user?.email}</p>
            </div>
          </div>
          <button
            onClick={handleLogout}
            className="w-full mt-2 flex items-center gap-3 px-4 py-2 rounded-lg text-dark-textSecondary hover:bg-dark-surfaceHover hover:text-red-500 transition-colors"
          >
            <LogOut className="w-5 h-5" />
            <span>Logout</span>
          </button>
        </div>
      </aside>

      {/* Main content */}
      <main className="flex-1 overflow-auto">
        <Outlet />
      </main>
    </div>
  )
}
