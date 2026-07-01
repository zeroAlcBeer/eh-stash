import React, { useState, useRef, useEffect } from 'react';
import { Settings, Check } from 'lucide-react';
import { t } from '../shared/i18n';
import { useAllowCosplay } from '../shared/settings';

export default function SettingsMenu() {
  const [open, setOpen] = useState(false);
  const [allowCosplay, setAllowCosplayValue] = useAllowCosplay();
  const ref = useRef(null);

  useEffect(() => {
    if (!open) return;
    const onClick = (e) => { if (!ref.current?.contains(e.target)) setOpen(false); };
    const onKey = (e) => { if (e.key === 'Escape') setOpen(false); };
    document.addEventListener('mousedown', onClick);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onClick);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  return (
    <div className="relative" ref={ref}>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={t('settings.open')}
        className="p-1.5 rounded-lg text-gray-400 hover:text-white hover:bg-white/10 transition-colors"
      >
        <Settings size={16} />
      </button>

      {open && (
        <div
          role="menu"
          aria-label={t('settings.title')}
          className="absolute right-0 top-full mt-1 w-72 rounded-lg bg-zinc-900 border border-white/10 shadow-xl z-50 p-3"
        >
          <p className="text-xs font-medium text-gray-500 uppercase tracking-wider px-1 mb-2">
            {t('settings.title')}
          </p>

          <button
            type="button"
            role="menuitemcheckbox"
            aria-checked={allowCosplay}
            onClick={() => setAllowCosplayValue(!allowCosplay)}
            className="w-full flex items-start gap-2.5 p-2 rounded-lg hover:bg-white/5 transition-colors text-left"
          >
            <span
              className={`mt-0.5 flex shrink-0 items-center justify-center w-4 h-4 rounded border transition-colors ${
                allowCosplay
                  ? 'bg-amber-500 border-amber-500 text-zinc-950'
                  : 'border-white/20 bg-transparent'
              }`}
              aria-hidden="true"
            >
              {allowCosplay && <Check size={12} strokeWidth={3} />}
            </span>
            <span className="flex-1">
              <span className="block text-sm text-gray-200">
                {t('settings.allowCosplay.label')}
              </span>
              <span className="block text-xs text-gray-500 mt-0.5 leading-relaxed">
                {t('settings.allowCosplay.hint')}
              </span>
            </span>
          </button>
        </div>
      )}
    </div>
  );
}
