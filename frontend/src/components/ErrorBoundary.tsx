import { Component, type ReactNode } from 'react'
import { ErrorBox } from './ui'

/**
 * Contains a render error to one screen, so navigation and the other screens
 * keep working. Shows the real error text rather than a blank page.
 */
export class ErrorBoundary extends Component<{ children: ReactNode; name: string }, { error: Error | null }> {
  state: { error: Error | null } = { error: null }

  static getDerivedStateFromError(error: Error) {
    return { error }
  }

  render() {
    if (this.state.error) {
      return (
        <ErrorBox
          title={`The ${this.props.name} screen failed to render. This is a frontend bug, not a ledger result.`}
          message={String(this.state.error)}
          onRetry={() => this.setState({ error: null })}
        />
      )
    }
    return this.props.children
  }
}
