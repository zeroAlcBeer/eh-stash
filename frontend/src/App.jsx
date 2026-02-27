import React from 'react';
import GalleryPage from './pages/GalleryPage';
import AdminPage from './pages/AdminPage';
import { BrowserRouter, NavLink, Route, Routes } from 'react-router-dom';

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
                  `px-3 py-1.5 rounded-lg text-sm font-medium transition-colors ${isActive ? 'bg-white/10 text-white' : 'text-gray-400 hover:text-white hover:bg-white/5'
                  }`
                }
              >
                Gallery
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
          <Routes>
            <Route path="/" element={<GalleryPage />} />
            <Route path="/admin" element={<AdminPage />} />
          </Routes>
        </main>
      </div>
    </BrowserRouter>
  );
}

export default App;
