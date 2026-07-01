import React, { useEffect, useReducer, useRef } from 'react';
import { X, Star, Heart, FileText, Globe, Calendar, ExternalLink } from 'lucide-react';
import { fetchGalleryGroup } from '../api';
import { getExUrl, LINK_TARGET, CATEGORY_STYLES, FALLBACK_IMAGE, getThumbUrl } from '../shared/gallery';
import { t, formatDate } from '../shared/i18n';
import { IS_PUBLIC } from '../shared/mode';

const initialState = { galleries: [], loading: true };

function reducer(state, action) {
  switch (action.type) {
    case 'load':
      return { galleries: [], loading: true };
    case 'success':
      return { galleries: action.galleries, loading: false };
    case 'error':
      return { galleries: [], loading: false };
    default:
      return state;
  }
}

export default function GroupModal({ groupId, onClose }) {
  const dialogRef = useRef(null);
  const [state, dispatch] = useReducer(reducer, initialState);
  const { galleries, loading } = state;

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog || dialog.open) return;
    dialog.showModal();
  }, []);

  const handleBackdropClick = (e) => {
    if (e.target === dialogRef.current) onClose();
  };

  useEffect(() => {
    if (!groupId) return;
    dispatch({ type: 'load' });
    fetchGalleryGroup(groupId)
      .then((galleries) => dispatch({ type: 'success', galleries }))
      .catch(() => dispatch({ type: 'error' }));
  }, [groupId]);

  return (
    <dialog
      ref={dialogRef}
      onClose={onClose}
      onClick={handleBackdropClick}
      aria-label={loading ? t('loading') : t('group.versions', { count: galleries.length })}
      className="m-auto rounded-lg ring-1 ring-white/10 w-full max-w-[calc(100%-2rem)] sm:max-w-2xl max-h-[80vh] overflow-y-auto p-0 bg-zinc-900 text-white"
    >
      {/* Header */}
      <div className="sticky top-0 bg-zinc-900 border-b border-white/10 px-4 py-3 flex items-center justify-between">
        <span className="text-sm font-medium text-gray-200">
          {loading ? t('loading') : t('group.versions', { count: galleries.length })}
        </span>
        <button
          type="button"
          onClick={onClose}
          className="p-2 -mr-1 text-gray-500 hover:text-white rounded-lg hover:bg-white/10 transition-colors"
          aria-label={t('group.close')}
        >
          <X size={18} />
        </button>
      </div>

      {/* List */}
      <div className="p-3 flex flex-col gap-2">
        {galleries.map((g) => {
          const exUrl = getExUrl(g.gid, g.token);
          const catStyle = CATEGORY_STYLES[g.category] || CATEGORY_STYLES['Misc'];
          const date = formatDate(g.posted_at);
          const displayTitle = g.title_jpn || g.title;

          return (
            <a
              key={g.gid}
              href={exUrl}
              target={LINK_TARGET}
              rel="noopener noreferrer"
              className="flex gap-3 rounded-lg bg-zinc-800 ring-1 ring-white/5 hover:ring-amber-400/60 transition-all p-2.5"
            >
              {/* Thumbnail */}
              <img
                src={g.thumb ? getThumbUrl(g.gid) : FALLBACK_IMAGE}
                alt={displayTitle}
                width={60}
                height={84}
                className="w-[60px] h-[84px] object-contain bg-zinc-950 rounded shrink-0"
                loading="lazy"
              />
              {/* Info */}
              <div className="flex-1 min-w-0 flex flex-col gap-1">
                <p className="text-sm text-gray-200 line-clamp-2 leading-snug font-medium">
                  {!IS_PUBLIC && g.is_favorited && <Heart size={12} className="inline fill-rose-400 text-rose-400 mr-1 -mt-0.5" />}
                  {displayTitle}
                </p>
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-gray-500">
                  <span className={`px-1.5 py-0.5 rounded text-xs font-bold text-white ${catStyle}`}>
                    {g.category}
                  </span>
                  <span className="flex items-center gap-0.5 text-amber-400">
                    <Star size={10} className="fill-amber-400" />{g.rating?.toFixed(2) ?? '-'}
                  </span>
                  <span className="flex items-center gap-0.5 text-rose-400">
                    <Heart size={10} className="fill-rose-400" />{g.fav_count ?? '-'}
                  </span>
                  {g.pages && (
                    <span className="flex items-center gap-0.5">
                      <FileText size={10} />{g.pages}p
                    </span>
                  )}
                  {g.language && (
                    <span className="flex items-center gap-0.5">
                      <Globe size={10} />{g.language}
                    </span>
                  )}
                  {date && (
                    <span className="flex items-center gap-0.5">
                      <Calendar size={10} />{date}
                    </span>
                  )}
                  <ExternalLink size={10} className="ml-auto text-gray-600" />
                </div>
              </div>
            </a>
          );
        })}
      </div>
    </dialog>
  );
}
