import { useEffect } from 'react'
import { X, CheckCircle, AlertCircle, Info } from 'lucide-react'

export type ToastType = 'success' | 'error' | 'info'

interface ToastProps {
  message: string
  type?: ToastType
  onClose: () => void
  duration?: number
}

// Errors often carry the only visible diagnostic for a failed asynchronous
// operation. Keep them open until the user explicitly dismisses them; callers
// can still provide a duration for an intentionally transient error message.
export function toastAutoDismissDuration(type: ToastType): number | undefined {
  return type === 'error' ? undefined : 3000
}

export default function Toast({ message, type = 'info', onClose, duration }: ToastProps) {
  const autoDismissDuration = duration ?? toastAutoDismissDuration(type)

  useEffect(() => {
    if (autoDismissDuration === undefined) return
    const timer = setTimeout(() => {
      onClose()
    }, autoDismissDuration)

    return () => clearTimeout(timer)
  }, [autoDismissDuration, onClose])

  const getIcon = () => {
    switch (type) {
      case 'success':
        return <CheckCircle className="w-5 h-5 text-emerald-600 dark:text-emerald-400" />
      case 'error':
        return <AlertCircle className="w-5 h-5 text-red-600 dark:text-red-400" />
      default:
        return <Info className="w-5 h-5 text-blue-600 dark:text-blue-400" />
    }
  }

  const getStyles = () => {
    switch (type) {
      case 'success':
        return 'bg-white dark:bg-gray-800 border-emerald-200 dark:border-emerald-800 shadow-lg'
      case 'error':
        return 'bg-white dark:bg-gray-800 border-red-200 dark:border-red-800 shadow-lg'
      default:
        return 'bg-white dark:bg-gray-800 border-gray-200 dark:border-gray-700 shadow-lg'
    }
  }

  return (
    <div className={`fixed top-6 right-6 z-50 animate-slide-in-right ${getStyles()} border rounded-xl p-4 min-w-[320px] max-w-[480px]`}>
      <div className="flex items-start space-x-3">
        <div className="flex-shrink-0 mt-0.5">
          {getIcon()}
        </div>
        <div className="flex-1 text-sm text-gray-900 dark:text-gray-100">
          {message}
        </div>
        <button
          onClick={onClose}
          className="flex-shrink-0 p-1 hover:bg-gray-100 dark:hover:bg-gray-700 rounded transition-colors"
        >
          <X className="w-4 h-4 text-gray-500 dark:text-gray-400" />
        </button>
      </div>
    </div>
  )
}
