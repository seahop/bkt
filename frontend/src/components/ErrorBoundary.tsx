import { Component, ErrorInfo, ReactNode } from 'react'
import { AlertTriangle } from 'lucide-react'

interface ErrorBoundaryProps {
  children: ReactNode
}

interface ErrorBoundaryState {
  hasError: boolean
  error: Error | null
}

/**
 * Top-level error boundary. Catches render-time errors anywhere in the tree and
 * shows a fallback UI instead of an unrecoverable white screen.
 */
export default class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  constructor(props: ErrorBoundaryProps) {
    super(props)
    this.state = { hasError: false, error: null }
  }

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { hasError: true, error }
  }

  componentDidCatch(error: Error, errorInfo: ErrorInfo) {
    console.error('Uncaught error:', error, errorInfo)
  }

  handleReload = () => {
    // Reset state and reload the app to recover.
    this.setState({ hasError: false, error: null })
    window.location.reload()
  }

  render() {
    if (this.state.hasError) {
      return (
        <div className="min-h-screen bg-dark-bg flex items-center justify-center p-4">
          <div className="card w-full max-w-md p-8 flex flex-col items-center text-center">
            <span className="flex items-center justify-center w-12 h-12 rounded-xl bg-red-500/10 mb-4">
              <AlertTriangle className="w-6 h-6 text-red-400" />
            </span>
            <h1 className="text-xl font-semibold text-dark-text mb-1">Something went wrong</h1>
            <p className="text-sm text-dark-textSecondary mb-5">
              An unexpected error occurred while rendering the page.
            </p>
            {this.state.error?.message && (
              <pre className="w-full bg-dark-inset border border-dark-border rounded-lg p-3 text-left font-mono text-xs text-red-400 overflow-x-auto mb-5">
                {this.state.error.message}
              </pre>
            )}
            <button onClick={this.handleReload} className="btn-primary">
              Reload page
            </button>
          </div>
        </div>
      )
    }

    return this.props.children
  }
}
