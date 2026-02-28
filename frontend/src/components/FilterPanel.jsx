import React, { useState, useRef, useEffect, useMemo } from 'react';
import { Search, X, ChevronDown, ChevronUp, SlidersHorizontal } from 'lucide-react';

const CATEGORIES = [
  'Manga', 'Doujinshi', 'Cosplay', 'Asian Porn',
  'Non-H', 'Western', 'Image Set', 'Game CG', 'Artist CG', 'Misc',
];

const MIN_FAV_OPTIONS = [
  { value: 0, label: 'All' },
  { value: 500, label: '≥ 500' },
  { value: 1000, label: '≥ 1000' },
  { value: 1500, label: '≥ 1500' },
  { value: 2000, label: '≥ 2000' },
];

function SelectField({ value, onChange, children, className = '' }) {
  return (
    <div className={`relative ${className}`}>
      <select
        value={value}
        onChange={onChange}
        className="w-full appearance-none px-3 py-1.5 pr-7 rounded-lg bg-white/5 border border-white/10 text-white text-sm
                   focus:outline-none focus:ring-2 focus:ring-blue-500/40 focus:border-blue-500/40 transition-all cursor-pointer"
      >
        {children}
      </select>
      <ChevronDown size={12} className="absolute right-2.5 top-1/2 -translate-y-1/2 text-gray-400 pointer-events-none" />
    </div>
  );
}

const SUGGESTION_LIMIT = 8;

const FilterPanel = ({ filters, onChange, tagSuggestions = [] }) => {
  const [collapsed, setCollapsed] = useState(true);
  const [tagInput, setTagInput] = useState(filters.tag || '');
  const [showSuggestions, setShowSuggestions] = useState(false);
  const [activeSuggestion, setActiveSuggestion] = useState(-1);
  const tagInputRef = useRef(null);

  // Sync external tag changes (e.g. from tag badge clicks)
  useEffect(() => {
    setTagInput(filters.tag || '');
    setActiveSuggestion(-1);
  }, [filters.tag]);

  const filteredSuggestions = useMemo(() => {
    const keyword = tagInput.trim().toLowerCase();
    const hits = keyword
      ? tagSuggestions.filter((tag) => tag.toLowerCase().includes(keyword))
      : tagSuggestions;
    return hits.slice(0, SUGGESTION_LIMIT);
  }, [tagInput, tagSuggestions]);

  const set = (field) => (e) => onChange({ ...filters, [field]: e.target.value });

  const commitTag = () => {
    const val = tagInput.trim();
    if (val !== filters.tag) onChange({ ...filters, tag: val });
  };

  const selectSuggestion = (value) => {
    setTagInput(value);
    setShowSuggestions(false);
    setActiveSuggestion(-1);
    if (value !== filters.tag) onChange({ ...filters, tag: value });
  };

  const clearTag = () => {
    setTagInput('');
    setShowSuggestions(false);
    setActiveSuggestion(-1);
    onChange({ ...filters, tag: '' });
  };

  // Count active API filters: category, tag, min_fav
  const activeCount = [
    filters.category,
    filters.tag,
    filters.min_fav > 0 ? filters.min_fav : null,
  ].filter(Boolean).length;

  return (
    <div className="sticky top-12 z-30 mb-3">
      <div className="rounded-xl border border-white/10 bg-zinc-900/90 backdrop-blur-md">

        {/* ── Header (always visible) ── */}
        <button
          type="button"
          onClick={() => setCollapsed((v) => !v)}
          className="w-full flex items-center justify-between px-4 py-2.5 text-sm hover:bg-white/5 transition-colors lg:hidden"
        >
          <span className="flex items-center gap-2 font-medium text-gray-300">
            <SlidersHorizontal size={14} />
            Filters
            {activeCount > 0 && (
              <span className="px-1.5 py-0.5 rounded-full bg-blue-500/20 text-blue-400 text-xs font-semibold">
                {activeCount}
              </span>
            )}
          </span>
          {collapsed ? <ChevronDown size={14} className="text-gray-400" /> : <ChevronUp size={14} className="text-gray-400" />}
        </button>

        {/* ── Filter fields ── */}
        <div className={`px-4 pb-3 pt-2 ${collapsed ? 'hidden lg:flex' : 'flex'} flex-wrap gap-3 items-end`}>

          {/* Category */}
          <div className="flex flex-col gap-1">
            <label className="text-[10px] font-medium text-gray-500 uppercase tracking-wider">Category</label>
            <SelectField value={filters.category || ''} onChange={set('category')} className="w-32">
              <option value="" className="bg-zinc-900">All</option>
              {CATEGORIES.map((c) => (
                <option key={c} value={c} className="bg-zinc-900">{c}</option>
              ))}
            </SelectField>
          </div>

          {/* Min Fav */}
          <div className="flex flex-col gap-1">
            <label className="text-[10px] font-medium text-gray-500 uppercase tracking-wider">Min Fav</label>
            <SelectField
              value={filters.min_fav || 0}
              onChange={(e) => onChange({ ...filters, min_fav: Number(e.target.value) })}
              className="w-28"
            >
              {MIN_FAV_OPTIONS.map((o) => (
                <option key={o.value} value={o.value} className="bg-zinc-900">{o.label}</option>
              ))}
            </SelectField>
          </div>

          {/* Tag search */}
          <div className="flex flex-col gap-1 flex-1 min-w-[160px]">
            <label className="text-[10px] font-medium text-gray-500 uppercase tracking-wider">Tag</label>
            <div className="relative">
              <div className="relative flex items-center">
                <Search size={13} className="absolute left-2.5 text-gray-500 pointer-events-none" />
                <input
                  ref={tagInputRef}
                  type="text"
                  value={tagInput}
                  onChange={(e) => {
                    setTagInput(e.target.value);
                    setShowSuggestions(true);
                    setActiveSuggestion(-1);
                  }}
                  onFocus={() => {
                    setShowSuggestions(true);
                  }}
                  onBlur={() => {
                    commitTag();
                    setTimeout(() => {
                      setShowSuggestions(false);
                      setActiveSuggestion(-1);
                    }, 100);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === 'Escape') {
                      setShowSuggestions(false);
                      setActiveSuggestion(-1);
                      return;
                    }

                    if (e.key === 'ArrowDown') {
                      if (!filteredSuggestions.length) return;
                      e.preventDefault();
                      setShowSuggestions(true);
                      setActiveSuggestion((prev) => (prev + 1) % filteredSuggestions.length);
                      return;
                    }

                    if (e.key === 'ArrowUp') {
                      if (!filteredSuggestions.length) return;
                      e.preventDefault();
                      setShowSuggestions(true);
                      setActiveSuggestion((prev) => (
                        prev <= 0 ? filteredSuggestions.length - 1 : prev - 1
                      ));
                      return;
                    }

                    if (e.key === 'Enter') {
                      if (showSuggestions && activeSuggestion >= 0 && filteredSuggestions[activeSuggestion]) {
                        e.preventDefault();
                        selectSuggestion(filteredSuggestions[activeSuggestion]);
                        return;
                      }
                      commitTag();
                      e.currentTarget.blur();
                    }
                  }}
                  placeholder="female:solo"
                  className="w-full pl-8 pr-7 py-1.5 rounded-lg bg-white/5 border border-white/10 text-white text-sm
                             placeholder-gray-600 focus:outline-none focus:ring-2 focus:ring-blue-500/40 focus:border-blue-500/40 transition-all"
                />
                {tagInput && (
                  <button
                    type="button"
                    onClick={clearTag}
                    className="absolute right-2 text-gray-500 hover:text-white transition-colors"
                  >
                    <X size={13} />
                  </button>
                )}
              </div>
              {showSuggestions && filteredSuggestions.length > 0 && (
                <div className="absolute top-full mt-1 z-40 w-full rounded-lg border border-white/10 bg-zinc-900 shadow-xl overflow-hidden">
                  {filteredSuggestions.map((suggestion, idx) => (
                    <button
                      key={suggestion}
                      type="button"
                      onMouseDown={(e) => e.preventDefault()}
                      onClick={() => selectSuggestion(suggestion)}
                      className={`w-full px-3 py-2 text-left text-xs transition-colors ${
                        idx === activeSuggestion
                          ? 'bg-blue-500/20 text-blue-200'
                          : 'text-gray-300 hover:bg-white/10 hover:text-white'
                      }`}
                    >
                      {suggestion}
                    </button>
                  ))}
                </div>
              )}
            </div>
            {showSuggestions && filteredSuggestions.length === 0 && tagInput.trim() && (
              <div className="mt-1 text-[11px] text-gray-500">无匹配 tag</div>
            )}
          </div>

          {/* Reset */}
          {activeCount > 0 && (
            <button
              type="button"
              onClick={() => onChange({ ...filters, category: '', min_fav: 0, tag: '' })}
              className="self-end mb-0.5 px-3 py-1.5 rounded-lg text-xs text-gray-400 hover:text-white hover:bg-white/10 transition-all border border-white/10"
            >
              Reset
            </button>
          )}
        </div>
      </div>
    </div>
  );
};

export default FilterPanel;
