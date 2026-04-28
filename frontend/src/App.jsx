import React from 'react';
import GalleryPage from './pages/GalleryPage';
import FavoritesPage from './pages/FavoritesPage';
import RecommendedPage from './pages/RecommendedPage';
import AdminPage from './pages/AdminPage';
import { ErrorBoundary } from './components/ErrorBoundary';
import { BrowserRouter, NavLink, Route, Routes, Link } from 'react-router-dom';
import { Heart, LayoutGrid, Sparkles } from 'lucide-react';

function NotFoundPage() {
  return (
    <div className="flex flex-col items-center justify-center py-24 text-center">
      <p className="text-6xl font-bold text-gray-700 mb-2">404</p>
      <p className="text-sm text-gray-500 mb-4">页面不存在</p>
      <Link
        to="/"
        className="px-4 py-2 rounded-lg bg-blue-600 hover:bg-blue-500 text-white text-sm font-medium transition-colors"
      >
        返回首页
      </Link>
    </div>
  );
}

function App() {
  return (
    <BrowserRouter>
      <div className="min-h-screen bg-zinc-950 text-gray-100 flex flex-col">
        {/* Nav */}
        <header className="sticky top-0 z-40 border-b border-white/10 bg-zinc-950/80 backdrop-blur-md">
          <div className="max-w-screen-2xl mx-auto px-4 h-12 flex items-center justify-between">
            <span className="font-bold tracking-tight text-white">EH-Stash</span>
            <nav className="flex gap-1">
              <NavLink
                to="/"
                end
                className={({ isActive }) =>
                  `px-3 py-1.5 rounded-lg text-sm font-medium transition-colors flex items-center gap-1.5 ${isActive ? 'bg-white/10 text-white' : 'text-gray-400 hover:text-white hover:bg-white/5'
                  }`
                }
              >
                <LayoutGrid size={13} />
                Gallery
              </NavLink>
              <NavLink
                to="/recommended"
                className={({ isActive }) =>
                  `px-3 py-1.5 rounded-lg text-sm font-medium transition-colors flex items-center gap-1.5 ${isActive ? 'bg-white/10 text-white' : 'text-gray-400 hover:text-white hover:bg-white/5'
                  }`
                }
              >
                <Sparkles size={13} />
                For You
              </NavLink>
              <NavLink
                to="/favorites"
                className={({ isActive }) =>
                  `px-3 py-1.5 rounded-lg text-sm font-medium transition-colors flex items-center gap-1.5 ${isActive ? 'bg-white/10 text-white' : 'text-gray-400 hover:text-white hover:bg-white/5'
                  }`
                }
              >
                <Heart size={13} />
                Favorites
              </NavLink>
              <NavLink
                to="/admin"
                className={({ isActive }) =>
                  `px-3 py-1.5 rounded-lg text-sm font-medium transition-colors ${isActive ? 'bg-white/10 text-white' : 'text-gray-400 hover:text-white hover:bg-white/5'
                  }`
                }
              >
                Admin
              </NavLink>
            </nav>
          </div>
        </header>

        {/* Main */}
        <main className="flex-1 max-w-screen-2xl mx-auto w-full px-4 pt-4">
          <ErrorBoundary>
            <Routes>
              <Route path="/" element={<GalleryPage key="gallery" />} />
              <Route path="/recommended" element={<RecommendedPage key="recommended" />} />
              <Route path="/favorites" element={<FavoritesPage key="favorites" />} />
              <Route path="/admin" element={<AdminPage />} />
              <Route path="*" element={<NotFoundPage />} />
            </Routes>
          </ErrorBoundary>
        </main>
      </div>
    </BrowserRouter>
  );
}

export default App;
