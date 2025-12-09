import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { FolderOpen, Key, Shield, Database as DatabaseIcon } from 'lucide-react'
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
      <div className="flex items-center justify-center h-full">
        <div className="text-dark-textSecondary">Loading...</div>
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
      label: 'Objects',
      value: 0,
      icon: DatabaseIcon,
      color: 'text-purple-500',
      bgColor: 'bg-purple-500/10',
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

  return (
    <div className="p-8">
      <div className="mb-8">
        <h1 className="text-3xl font-bold text-dark-text mb-2">Dashboard</h1>
        <p className="text-dark-textSecondary">Welcome to your object storage system</p>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-6 mb-8">
        {stats.map((stat) => {
          const Icon = stat.icon
          const content = (
            <div className="bg-dark-surface border border-dark-border rounded-lg p-6 hover:border-dark-border transition-colors">
              <div className="flex items-center justify-between mb-4">
                <div className={`${stat.bgColor} ${stat.color} p-3 rounded-lg`}>
                  <Icon className="w-6 h-6" />
                </div>
              </div>
              <p className="text-3xl font-bold text-dark-text mb-1">{stat.value}</p>
              <p className="text-dark-textSecondary text-sm">{stat.label}</p>
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
        <div className="bg-dark-surface border border-dark-border rounded-lg p-6">
          <h2 className="text-xl font-semibold text-dark-text mb-4">Recent Buckets</h2>
          {buckets.length === 0 ? (
            <div className="text-center py-8">
              <FolderOpen className="w-12 h-12 text-dark-textSecondary mx-auto mb-3 opacity-50" />
              <p className="text-dark-textSecondary mb-4">No buckets yet</p>
              <Link
                to="/buckets"
                className="inline-block bg-blue-600 hover:bg-blue-700 text-white px-4 py-2 rounded-lg transition-colors"
              >
                Create your first bucket
              </Link>
            </div>
          ) : (
            <div className="space-y-3">
              {buckets.slice(0, 5).map((bucket) => (
                <Link
                  key={bucket.id}
                  to={`/buckets/${bucket.name}`}
                  className="flex items-center gap-3 p-3 rounded-lg hover:bg-dark-surfaceHover transition-colors"
                >
                  <FolderOpen className="w-5 h-5 text-blue-500" />
                  <div className="flex-1 min-w-0">
                    <p className="text-dark-text font-medium truncate">{bucket.name}</p>
                    <p className="text-xs text-dark-textSecondary">{bucket.region}</p>
                  </div>
                  {bucket.is_public && (
                    <span className="text-xs bg-green-500/10 text-green-500 px-2 py-1 rounded">
                      Public
                    </span>
                  )}
                </Link>
              ))}
            </div>
          )}
        </div>

        <div className="bg-dark-surface border border-dark-border rounded-lg p-6">
          <h2 className="text-xl font-semibold text-dark-text mb-4">Quick Actions</h2>
          <div className="space-y-3">
            <Link
              to="/buckets"
              className="flex items-center gap-3 p-4 bg-dark-bg hover:bg-dark-surfaceHover rounded-lg transition-colors border border-dark-border"
            >
              <FolderOpen className="w-6 h-6 text-blue-500" />
              <div>
                <p className="text-dark-text font-medium">Create Bucket</p>
                <p className="text-xs text-dark-textSecondary">Create a new storage bucket</p>
              </div>
            </Link>
            <Link
              to="/profile"
              className="flex items-center gap-3 p-4 bg-dark-bg hover:bg-dark-surfaceHover rounded-lg transition-colors border border-dark-border"
            >
              <Key className="w-6 h-6 text-green-500" />
              <div>
                <p className="text-dark-text font-medium">Generate Access Key</p>
                <p className="text-xs text-dark-textSecondary">Create API credentials</p>
              </div>
            </Link>
            <Link
              to="/policies"
              className="flex items-center gap-3 p-4 bg-dark-bg hover:bg-dark-surfaceHover rounded-lg transition-colors border border-dark-border"
            >
              <Shield className="w-6 h-6 text-orange-500" />
              <div>
                <p className="text-dark-text font-medium">Manage Policies</p>
                <p className="text-xs text-dark-textSecondary">Configure access control</p>
              </div>
            </Link>
          </div>
        </div>
      </div>
    </div>
  )
}
