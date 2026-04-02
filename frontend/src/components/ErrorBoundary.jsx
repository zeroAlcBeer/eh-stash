import React from 'react';
import { AlertCircle } from 'lucide-react';

export class ErrorBoundary extends React.Component {
  constructor(props) {
    super(props);
    this.state = { error: null };
  }

  static getDerivedStateFromError(error) {
    return { error };
  }

  render() {
    if (this.state.error) {
      return (
        <div className="flex flex-col items-center justify-center py-24 px-4 text-center">
          <AlertCircle size={36} className="text-rose-500 mb-3" />
          <h2 className="text-lg font-semibold text-white mb-1">页面出错了</h2>
          <p className="text-sm text-gray-400 mb-4 max-w-md">{this.state.error.message}</p>
          <button
            onClick={() => {
              this.setState({ error: null });
              window.location.reload();
            }}
            className="px-4 py-2 rounded-lg bg-blue-600 hover:bg-blue-500 text-white text-sm font-medium transition-colors"
          >
            刷新页面
          </button>
        </div>
      );
    }

    return this.props.children;
  }
}
