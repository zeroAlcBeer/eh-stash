import React, { useState, useRef, useMemo, useId } from 'react';
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

function SelectField({ value, onChange, children, className = '', id }) {
  return (
    <div className={`relative ${className}`}>
      <select
        id={id}
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
  const [tagInput, setTagInput] = useState('');
  const [showSuggestions, setShowSuggestions] = useState(false);
  const [activeSuggestion, setActiveSuggestion] = useState(-1);
  const tagInputRef = useRef(null);
  const listboxId = useId();

  const tags = filters.tags || [];

  const filteredSuggestions = useMemo(() => {
    const keyword = tagInput.trim().toLowerCase();
    const selected = new Set(tags);
    const hits = keyword
      ? tagSuggestions.filter((tag) => !selected.has(tag) && tag.toLowerCase().includes(keyword))
      : tagSuggestions.filter((tag) => !selected.has(tag));
    return hits.slice(0, SUGGESTION_LIMIT);
  }, [tagInput, tagSuggestions, tags]);

  const set = (field) => (e) => onChange({ ...filters, [field]: e.target.value });

  const addTag = (val) => {
    if (!val || tags.includes(val)) return;
    onChange({ ...filters, tags: [...tags, val] });
  };

  const removeTag = (val) => {
    onChange({ ...filters, tags: tags.filter((t) => t !== val) });
  };

  const commitTag = () => {
    const val = tagInput.trim();
    if (val) {
      addTag(val);
      setTagInput('');
    }
  };

  const selectSuggestion = (value) => {
    setTagInput('');
    setShowSuggestions(false);
    setActiveSuggestion(-1);
    addTag(value);
  };

  const clearTag = () => {
    setTagInput('');
    setShowSuggestions(false);
    setActiveSuggestion(-1);
  };

  // Count active API filters: category, tags, min_fav
  const activeCount = [
    filters.category,
    tags.length > 0 ? true : null,
    filters.min_fav > 0 ? filters.min_fav : null,
  ].filter(Boolean).length;

  const hasSuggestions = showSuggestions && filteredSuggestions.length > 0;
  const activeOptionId = activeSuggestion >= 0 ? `${listboxId}-option-${activeSuggestion}` : undefined;

  return (
    <div className="sticky top-12 z-30 mb-3">
      <div className="rounded-xl border border-white/10 bg-zinc-900/90 backdrop-blur-md">

        {/* ── Header (always visible) ── */}
        <button
          type="button"
          onClick={() => setCollapsed((v) => !v)}
          aria-expanded={!collapsed}
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
            <label htmlFor="filter-category" className="text-xs font-medium text-gray-500 uppercase tracking-wider">Category</label>
            <SelectField id="filter-category" value={filters.category || ''} onChange={set('category')} className="w-32">
              <option value="" className="bg-zinc-900">All</option>
              {CATEGORIES.map((c) => (
                <option key={c} value={c} className="bg-zinc-900">{c}</option>
              ))}
            </SelectField>
          </div>

          {/* Min Fav */}
          <div className="flex flex-col gap-1">
            <label htmlFor="filter-min-fav" className="text-xs font-medium text-gray-500 uppercase tracking-wider">Min Fav</label>
            <SelectField
              id="filter-min-fav"
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
            <label htmlFor="filter-tag-input" className="text-xs font-medium text-gray-500 uppercase tracking-wider">Tag</label>
            {/* Selected tag pills */}
            {tags.length > 0 && (
              <div className="flex flex-wrap gap-1 mb-1">
                {tags.map((t) => (
                  <span
                    key={t}
                    className="inline-flex items-center gap-1 px-2 py-0.5 rounded-md bg-blue-500/20 text-blue-300 text-xs border border-blue-500/30"
                  >
                    {t}
                    <button
                      type="button"
                      onClick={() => removeTag(t)}
                      className="p-0.5 hover:text-white transition-colors rounded"
                      aria-label={`移除标签 ${t}`}
                    >
                      <X size={12} />
                    </button>
                  </span>
                ))}
              </div>
            )}
            <div className="relative">
              <div className="relative flex items-center">
                <Search size={13} className="absolute left-2.5 text-gray-500 pointer-events-none" aria-hidden="true" />
                <input
                  ref={tagInputRef}
                  id="filter-tag-input"
                  type="text"
                  role="combobox"
                  aria-expanded={hasSuggestions}
                  aria-controls={listboxId}
                  aria-activedescendant={activeOptionId}
                  aria-autocomplete="list"
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
                      e.preventDefault();
                      commitTag();
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
                    className="absolute right-1.5 p-1 text-gray-500 hover:text-white transition-colors rounded"
                    aria-label="清除搜索"
                  >
                    <X size={13} />
                  </button>
                )}
              </div>
              {hasSuggestions && (
                <ul
                  id={listboxId}
                  role="listbox"
                  className="absolute top-full mt-1 z-40 w-full rounded-lg border border-white/10 bg-zinc-900 shadow-xl overflow-hidden"
                >
                  {filteredSuggestions.map((suggestion, idx) => (
                    <li
                      key={suggestion}
                      id={`${listboxId}-option-${idx}`}
                      role="option"
                      aria-selected={idx === activeSuggestion}
                    >
                      <button
                        type="button"
                        tabIndex={-1}
                        onMouseDown={(e) => e.preventDefault()}
                        onClick={() => selectSuggestion(suggestion)}
                        className={`w-full px-3 py-2 text-left text-xs transition-colors ${idx === activeSuggestion
                            ? 'bg-blue-500/20 text-blue-200'
                            : 'text-gray-300 hover:bg-white/10 hover:text-white'
                          }`}
                      >
                        {suggestion}
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </div>
            {showSuggestions && filteredSuggestions.length === 0 && tagInput.trim() && (
              <div className="mt-1 text-xs text-gray-500">无匹配 tag</div>
            )}
          </div>

          {/* Reset */}
          {activeCount > 0 && (
            <button
              type="button"
              onClick={() => onChange({ ...filters, category: '', min_fav: 0, tags: [] })}
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
