import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { FolderOpen, Key, Shield, ArrowRight } from 'lucide-react'
import { bucketApi, accessKeyApi } from '../services/api'
import { listPolicies } from '../services/policy'
import type { Bucket, AccessKey } from '../types'

export default function Dashboard() {
  const [buckets, setBuckets] = useState<Bucket[]>([])
  const [accessKeys, setAccessKeys] = useState<AccessKey[]>([])
  const [policyCount, setPolicyCount] = useState(0)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    loadDashboardData()
  }, [])

  const loadDashboardData = async () => {
    try {
      const [bucketsData, keysData, policiesData] = await Promise.all([
        bucketApi.listBuckets(),
        accessKeyApi.listAccessKeys(),
        listPolicies().catch(() => []),
      ])
      setBuckets(bucketsData || [])
      setAccessKeys(keysData || [])
      setPolicyCount(policiesData?.length || 0)
    } catch (error) {
      console.error('Failed to load dashboard data:', error)
      setBuckets([])
      setAccessKeys([])
      setPolicyCount(0)
    } finally {
      setLoading(false)
    }
  }

  if (loading) {
    return (
      <div className="flex flex-col items-center justify-center h-64 gap-3">
        <div className="spinner" />
        <p className="text-sm text-dark-textSecondary">Loading dashboard…</p>
      </div>
    )
  }

  const stats = [
    {
      label: 'Total Buckets',
      value: buckets.length,
      icon: FolderOpen,
      color: 'text-blue-500',
      bgColor: 'bg-blue-500/10',
      link: '/buckets',
    },
    {
      label: 'Access Keys',
      value: accessKeys.length,
      icon: Key,
      color: 'text-green-500',
      bgColor: 'bg-green-500/10',
      link: '/profile',
    },
    {
      label: 'Policies',
      value: policyCount,
      icon: Shield,
      color: 'text-orange-500',
      bgColor: 'bg-orange-500/10',
      link: '/policies',
    },
  ]

  const quickActions = [
    {
      to: '/buckets',
      icon: FolderOpen,
      color: 'text-blue-500',
      bgColor: 'bg-blue-500/10',
      label: 'Create Bucket',
      description: 'Create a new storage bucket',
    },
    {
      to: '/profile',
      icon: Key,
      color: 'text-green-500',
      bgColor: 'bg-green-500/10',
      label: 'Generate Access Key',
      description: 'Create API credentials',
    },
    {
      to: '/policies',
      icon: Shield,
      color: 'text-orange-500',
      bgColor: 'bg-orange-500/10',
      label: 'Manage Policies',
      description: 'Configure access control',
    },
  ]

  return (
    <div className="page">
      <div className="flex items-start justify-between gap-4 mb-8">
        <div>
          <h1 className="page-title">Dashboard</h1>
          <p className="page-subtitle">Overview of your object storage system</p>
        </div>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
        {stats.map((stat) => {
          const Icon = stat.icon
          const content = (
            <div
              className={`card p-5 ${
                stat.link ? 'hover:border-dark-borderStrong transition-colors' : ''
              }`}
            >
              <div className="flex items-center gap-4">
                <span
                  className={`flex items-center justify-center w-11 h-11 rounded-lg shrink-0 ${stat.bgColor} ${stat.color}`}
                >
                  <Icon className="w-5 h-5" />
                </span>
                <div>
                  <p className="text-2xl font-semibold tabular-nums text-dark-text leading-tight">
                    {stat.value}
                  </p>
                  <p className="text-xs uppercase tracking-wider text-dark-textSecondary mt-0.5">
                    {stat.label}
                  </p>
                </div>
              </div>
            </div>
          )

          return stat.link ? (
            <Link key={stat.label} to={stat.link}>
              {content}
            </Link>
          ) : (
            <div key={stat.label}>{content}</div>
          )
        })}
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        <div className="card p-6">
          <h2 className="text-base font-semibold text-dark-text mb-4">Recent Buckets</h2>
          {buckets.length === 0 ? (
            <div className="flex flex-col items-center text-center py-8">
              <FolderOpen className="w-10 h-10 text-dark-textMuted mb-3" />
              <p className="text-sm text-dark-textSecondary mb-4">No buckets yet</p>
              <Link to="/buckets" className="btn-primary">
                Create your first bucket
              </Link>
            </div>
          ) : (
            <div className="space-y-1">
              {buckets.slice(0, 5).map((bucket) => (
                <Link
                  key={bucket.id}
                  to={`/buckets/${bucket.name}`}
                  className="flex items-center gap-3 px-3 py-2.5 rounded-lg hover:bg-dark-surfaceHover transition-colors"
                >
                  <span className="flex items-center justify-center w-8 h-8 rounded-lg bg-blue-500/10 shrink-0">
                    <FolderOpen className="w-4 h-4 text-blue-500" />
                  </span>
                  <div className="flex-1 min-w-0">
                    <p className="text-sm font-medium text-dark-text truncate">{bucket.name}</p>
                    <p className="text-xs text-dark-textMuted">{bucket.region}</p>
                  </div>
                  {bucket.is_public && <span className="badge-green">Public</span>}
                </Link>
              ))}
            </div>
          )}
        </div>

        <div className="card p-6">
          <h2 className="text-base font-semibold text-dark-text mb-4">Quick Actions</h2>
          <div className="space-y-1">
            {quickActions.map((action) => {
              const Icon = action.icon
              return (
                <Link
                  key={action.to}
                  to={action.to}
                  className="group flex items-center gap-3 px-3 py-2.5 rounded-lg hover:bg-dark-surfaceHover transition-colors"
                >
                  <span
                    className={`flex items-center justify-center w-9 h-9 rounded-lg shrink-0 ${action.bgColor} ${action.color}`}
                  >
                    <Icon className="w-[18px] h-[18px]" />
                  </span>
                  <div className="flex-1 min-w-0">
                    <p className="text-sm font-medium text-dark-text">{action.label}</p>
                    <p className="text-xs text-dark-textSecondary">{action.description}</p>
                  </div>
                  <ArrowRight className="w-4 h-4 text-dark-textMuted opacity-0 group-hover:opacity-100 transition-opacity" />
                </Link>
              )
            })}
          </div>
        </div>
      </div>
    </div>
  )
}
