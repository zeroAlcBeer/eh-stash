import React, { useState, useCallback, useRef, useEffect, useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { LayoutGrid, LayoutList, ChevronFirst, ChevronLast, ChevronLeft, ChevronRight, Loader2, AlertCircle, Heart, Star, MessageCircle, Calendar, ChevronDown, Languages } from 'lucide-react';
import { fetchGalleries } from '../api';
import GalleryCard from '../components/GalleryCard';
import FilterPanel from '../components/FilterPanel';
import { useTagTranslation } from '../hooks/useTagTranslation';

const PAGE_SIZE = 100;

const SORT_OPTIONS = [
  { value: 'fav_count', label: 'Fav', Icon: Heart },
  { value: 'rating', label: 'Rating', Icon: Star },
  { value: 'comment_count', label: 'Comments', Icon: MessageCircle },
  { value: 'posted_at', label: 'Date', Icon: Calendar },
];

const RATING_CYCLE = [0, 3.5, 4, 4.5];

const SORT_FNS = {
  fav_count: (a, b) => (b.fav_count ?? 0) - (a.fav_count ?? 0),
  rating: (a, b) => (b.rating ?? 0) - (a.rating ?? 0),
  comment_count: (a, b) => (b.comment_count ?? 0) - (a.comment_count ?? 0),
  posted_at: (a, b) => new Date(b.posted_at ?? 0) - new Date(a.posted_at ?? 0),
};

function getInitialViewMode() {
  try { return localStorage.getItem('viewMode') || 'grid'; } catch { return 'grid'; }
}

// ─── Sort Dropdown ────────────────────────────────────────────────────────────

function SortDropdown({ value, onChange }) {
  const [open, setOpen] = useState(false);
  const ref = useRef(null);
  const current = SORT_OPTIONS.find((o) => o.value === value) ?? SORT_OPTIONS[0];

  useEffect(() => {
    if (!open) return;
    const handler = (e) => { if (!ref.current?.contains(e.target)) setOpen(false); };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, [open]);

  return (
    <div className="relative" ref={ref}>
      <button
        onClick={() => setOpen((v) => !v)}
        className="flex items-center gap-1.5 px-2.5 py-1.5 rounded-lg text-xs text-gray-400 hover:text-white hover:bg-white/10 transition-all border border-white/10"
      >
        <current.Icon size={13} />
        {current.label}
        <ChevronDown size={11} className="text-gray-600" />
      </button>
      {open && (
        <div className="absolute right-0 top-full mt-1 w-32 rounded-lg bg-zinc-900 border border-white/10 shadow-xl z-50 overflow-hidden">
          {SORT_OPTIONS.map((o) => (
            <button
              key={o.value}
              onClick={() => { onChange(o.value); setOpen(false); }}
              className={`w-full flex items-center gap-2 px-3 py-2 text-xs transition-colors ${o.value === value ? 'text-white bg-white/10' : 'text-gray-400 hover:text-white hover:bg-white/5'
                }`}
            >
              <o.Icon size={13} />
              {o.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

// ─── Rating Cycle Button ──────────────────────────────────────────────────────

function RatingCycleButton({ value, onChange }) {
  const active = value > 0;
  const cycle = () => {
    const idx = RATING_CYCLE.indexOf(value);
    onChange(RATING_CYCLE[(idx + 1) % RATING_CYCLE.length]);
  };
  return (
    <button
      onClick={cycle}
      className={`flex items-center gap-1.5 px-2.5 py-1.5 rounded-lg text-xs transition-all border ${active
        ? 'text-amber-400 border-amber-500/40 bg-amber-500/10 hover:bg-amber-500/20'
        : 'text-gray-400 border-white/10 hover:text-white hover:bg-white/10'
        }`}
    >
      <Star size={13} className={active ? 'fill-amber-400' : ''} />
      {active ? `≥${value}` : 'Any'}
    </button>
  );
}

// ─── Pagination Bar ───────────────────────────────────────────────────────────

function PaginationBar({ page, totalPages, onPageChange }) {
  const btnCls = (disabled) =>
    `flex items-center justify-center w-8 h-8 rounded-lg transition-colors ${disabled
      ? 'text-gray-700 cursor-not-allowed'
      : 'text-gray-300 hover:bg-white/10 hover:text-white'
    }`;

  const pageNums = () => {
    const pages = [];
    const delta = 2;
    const left = Math.max(2, page - delta);
    const right = Math.min(totalPages - 1, page + delta);
    pages.push(1);
    if (left > 2) pages.push('…');
    for (let i = left; i <= right; i++) pages.push(i);
    if (right < totalPages - 1) pages.push('…');
    if (totalPages > 1) pages.push(totalPages);
    return pages;
  };

  return (
    <div className="fixed bottom-0 left-0 right-0 z-30 flex justify-center py-2 bg-zinc-950/80 backdrop-blur-md border-t border-white/5">
      <div className="flex items-center gap-1">
        <button className={btnCls(page === 1)} onClick={() => onPageChange(1)} disabled={page === 1}>
          <ChevronFirst size={15} />
        </button>
        <button className={btnCls(page === 1)} onClick={() => onPageChange(page - 1)} disabled={page === 1}>
          <ChevronLeft size={15} />
        </button>

        {pageNums().map((p, i) =>
          p === '…'
            ? <span key={`ellipsis-${i}`} className="w-8 text-center text-gray-600 text-sm">…</span>
            : (
              <button
                key={p}
                onClick={() => onPageChange(p)}
                className={`w-8 h-8 rounded-lg text-sm font-medium transition-colors ${p === page
                  ? 'bg-blue-600 text-white'
                  : 'text-gray-400 hover:bg-white/10 hover:text-white'
                  }`}
              >
                {p}
              </button>
            )
        )}

        <button className={btnCls(page === totalPages)} onClick={() => onPageChange(page + 1)} disabled={page === totalPages}>
          <ChevronRight size={15} />
        </button>
        <button className={btnCls(page === totalPages)} onClick={() => onPageChange(totalPages)} disabled={page === totalPages}>
          <ChevronLast size={15} />
        </button>
      </div>
    </div>
  );
}

// ─── Main Page ────────────────────────────────────────────────────────────────

const GalleryPage = () => {
  const [page, setPage] = useState(1);
  const [filters, setFilters] = useState({
    category: '',
    sort: 'fav_count',
    min_rating: 0,
    min_fav: 0,
    tag: '',
  });
  const [viewMode, setViewMode] = useState(getInitialViewMode);
  const [showTranslation, setShowTranslation] = useState(false);
  const { translate } = useTagTranslation(showTranslation);

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['galleries', page, filters.category, filters.min_fav, filters.tag],
    queryFn: () => {
      const { sort, min_rating, ...apiFilters } = filters;
      return fetchGalleries({ page, page_size: PAGE_SIZE, sort: 'gid_desc', ...apiFilters });
    },
    keepPreviousData: true,
  });

  const handleFilterChange = useCallback((newFilters) => {
    setFilters(newFilters);
    setPage(1);
  }, []);

  const handleTagSearch = useCallback((tag) => {
    if (!tag) return;
    setFilters((prev) => ({ ...prev, tag }));
    setPage(1);
    window.scrollTo({ top: 0, behavior: 'smooth' });
  }, []);

  const handlePageChange = (value) => {
    setPage(value);
    window.scrollTo({ top: 0, behavior: 'smooth' });
  };

  const toggleViewMode = () => {
    const next = viewMode === 'grid' ? 'list' : 'grid';
    setViewMode(next);
    try { localStorage.setItem('viewMode', next); } catch { }
  };

  const rawItems = data?.items || [];
  const totalPages = data?.pages || 1;
  const total = data?.total ?? 0;
  const pageSize = data?.size ?? PAGE_SIZE;
  const items = [...rawItems]
    .filter((g) => !filters.min_rating || (g.rating ?? 0) >= filters.min_rating)
    .sort(SORT_FNS[filters.sort] ?? SORT_FNS.fav_count);
  const tagSuggestions = useMemo(() => {
    const freq = new Map();
    for (const gallery of items) {
      const tagMap = gallery?.tags || {};
      for (const [ns, values] of Object.entries(tagMap)) {
        if (!Array.isArray(values)) continue;
        for (const value of values) {
          if (!value) continue;
          const key = `${ns}:${value}`;
          freq.set(key, (freq.get(key) || 0) + 1);
        }
      }
    }
    return [...freq.entries()]
      .sort((a, b) => {
        if (b[1] !== a[1]) return b[1] - a[1];
        return a[0].localeCompare(b[0]);
      })
      .slice(0, 50)
      .map(([tag]) => tag);
  }, [items]);

  return (
    // pb-14 reserves space above the sticky bottom pagination bar
    <div className="pb-14">
      <FilterPanel
        filters={filters}
        onChange={handleFilterChange}
        tagSuggestions={tagSuggestions}
      />

      {/* Results bar */}
      <div className="flex items-center justify-between mb-3 px-0.5">
        <div className="text-xs text-gray-500">
          {isLoading ? '…' : (
            <>
              {total.toLocaleString()} results · page {page} / {totalPages}
              {filters.min_rating > 0 && (
                <span className="ml-1 text-amber-400/70">· 显示 {items.length} 条</span>
              )}
            </>
          )}
        </div>
        <div className="flex items-center gap-3">
          {/* Translation toggle – only useful in list mode */}
          {viewMode === 'list' && (
            <button
              onClick={() => setShowTranslation((v) => !v)}
              className={`flex items-center gap-1.5 px-2.5 py-1.5 rounded-lg text-xs transition-all border ${showTranslation
                  ? 'text-sky-400 border-sky-500/40 bg-sky-500/10 hover:bg-sky-500/20'
                  : 'text-gray-400 border-white/10 hover:text-white hover:bg-white/10'
                }`}
              title="切换标签中文翻译"
            >
              <Languages size={13} />
              中文
            </button>
          )}
          <SortDropdown
            value={filters.sort}
            onChange={(v) => setFilters((prev) => ({ ...prev, sort: v }))}
          />
          <RatingCycleButton
            value={filters.min_rating}
            onChange={(v) => setFilters((prev) => ({ ...prev, min_rating: v }))}
          />
          <button
            onClick={toggleViewMode}
            className="flex items-center gap-1.5 px-2.5 py-1.5 rounded-lg text-xs text-gray-400 hover:text-white hover:bg-white/10 transition-all border border-white/10"
            title={viewMode === 'grid' ? 'Switch to list view' : 'Switch to grid view'}
          >
            {viewMode === 'grid' ? <LayoutList size={14} /> : <LayoutGrid size={14} />}
            {viewMode === 'grid' ? 'List' : 'Grid'}
          </button>
        </div>
      </div>

      {/* Error state */}
      {isError && (
        <div className="flex items-center gap-2 rounded-lg bg-rose-500/10 border border-rose-500/30 px-4 py-3 text-sm text-rose-400 mb-4">
          <AlertCircle size={16} /> {error.message}
        </div>
      )}

      {/* Loading */}
      {isLoading ? (
        <div className="flex justify-center items-center py-24">
          <Loader2 size={28} className="animate-spin text-gray-600" />
        </div>
      ) : (
        viewMode === 'grid' ? (
          <div className="grid grid-cols-3 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-6 xl:grid-cols-6 gap-3">
            {items.map((gallery) => (
              <GalleryCard key={gallery.gid} gallery={gallery} viewMode="grid" />
            ))}
          </div>
        ) : (
          <div className="flex flex-col gap-2">
            {items.map((gallery) => (
              <GalleryCard key={gallery.gid} gallery={gallery} viewMode="list" onTagSearch={handleTagSearch} translate={showTranslation ? translate : undefined} />
            ))}
          </div>
        )
      )}

      <PaginationBar page={page} totalPages={totalPages || 1} onPageChange={handlePageChange} />
    </div>
  );
};

export default GalleryPage;
