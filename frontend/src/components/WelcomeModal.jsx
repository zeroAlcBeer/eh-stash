import React, { useState } from 'react';
import { ShieldCheck, Database, Heart, Layers, Ban } from 'lucide-react';
import { t } from '../shared/i18n';
import { useFocusTrap } from '../hooks/useFocusTrap';

// Two-step compliance gate:
//   step 'age'    — Yes/No buttons. Yes → 'compare', No → 'denied' (terminal).
//   step 'compare'— side-by-side feature explanation. Continue → ack.
//   step 'denied' — read-only message screen; user must close the tab.
//
// Backdrop click and Esc are no-ops by design. Only the explicit buttons
// advance the state. localStorage flag is set on the final ack so the modal
// never reappears.
export const WELCOME_STORAGE_KEY = 'ehstash:welcome-acked';

export function isWelcomeAcked() {
  try { return localStorage.getItem(WELCOME_STORAGE_KEY) === '1'; } catch { return false; }
}

export default function WelcomeModal({ onAck }) {
  const dialogRef = useFocusTrap(true);
  const [step, setStep] = useState('age');

  const handleConfirm = () => {
    try { localStorage.setItem(WELCOME_STORAGE_KEY, '1'); } catch { /* ignore */ }
    onAck();
  };

  return (
    <div className="fixed inset-0 z-[100] flex items-center justify-center p-4">
      <div className="absolute inset-0 bg-black/80 backdrop-blur-sm" />
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="welcome-title"
        className="relative bg-zinc-900 rounded-xl ring-1 ring-white/10 w-full max-w-lg max-h-[90vh] overflow-y-auto"
      >
        {step === 'age' && <AgeStep onYes={() => setStep('compare')} onNo={() => setStep('denied')} />}
        {step === 'compare' && <CompareStep onContinue={handleConfirm} />}
        {step === 'denied' && <DeniedStep />}
      </div>
    </div>
  );
}

function AgeStep({ onYes, onNo }) {
  return (
    <>
      <div className="px-6 pt-6 pb-2 flex items-center gap-2.5">
        <ShieldCheck size={20} className="text-amber-400" aria-hidden="true" />
        <h2 id="welcome-title" className="text-lg font-semibold text-white">
          {t('welcome.title')}
        </h2>
      </div>

      <div className="px-6 py-4">
        <p className="font-medium text-amber-300 mb-2">{t('welcome.age.heading')}</p>
        <p className="text-sm text-gray-400 leading-relaxed">{t('welcome.age.body')}</p>
      </div>

      <div className="px-6 pb-6 pt-2 flex flex-col-reverse sm:flex-row gap-2.5">
        <button
          type="button"
          onClick={onNo}
          className="flex-1 px-4 py-2.5 rounded-lg border border-white/15 text-gray-300 hover:bg-white/5 hover:text-white text-sm font-medium transition-colors"
        >
          {t('welcome.age.no')}
        </button>
        <button
          type="button"
          onClick={onYes}
          autoFocus
          className="flex-1 px-4 py-2.5 rounded-lg bg-amber-500 hover:bg-amber-400 text-zinc-950 text-sm font-semibold transition-colors"
        >
          {t('welcome.age.yes')}
        </button>
      </div>
    </>
  );
}

function CompareItem({ Icon, iconColor, title, body }) {
  return (
    <div className="flex gap-3">
      <Icon size={18} className={`mt-0.5 shrink-0 ${iconColor}`} aria-hidden="true" />
      <div className="min-w-0">
        <p className="text-sm font-medium text-gray-200">{title}</p>
        <p className="text-xs text-gray-400 leading-relaxed mt-0.5">{body}</p>
      </div>
    </div>
  );
}

function CompareStep({ onContinue }) {
  return (
    <>
      <div className="px-6 pt-6 pb-2">
        <h2 id="welcome-title" className="text-lg font-semibold text-white">
          {t('welcome.compare.title')}
        </h2>
        <p className="text-xs text-gray-500 mt-1">{t('welcome.compare.subtitle')}</p>
      </div>

      <div className="px-6 py-4 space-y-4">
        <CompareItem
          Icon={Database}
          iconColor="text-sky-400"
          title={t('welcome.compare.index.title')}
          body={t('welcome.compare.index.body')}
        />
        <CompareItem
          Icon={Heart}
          iconColor="text-rose-400"
          title={t('welcome.compare.fav.title')}
          body={t('welcome.compare.fav.body')}
        />
        <CompareItem
          Icon={Layers}
          iconColor="text-amber-400"
          title={t('welcome.compare.group.title')}
          body={t('welcome.compare.group.body')}
        />
      </div>

      <div className="px-6 pb-6 pt-2">
        <button
          type="button"
          onClick={onContinue}
          autoFocus
          className="w-full px-4 py-2.5 rounded-lg bg-amber-500 hover:bg-amber-400 text-zinc-950 text-sm font-semibold transition-colors"
        >
          {t('welcome.compare.continue')}
        </button>
      </div>
    </>
  );
}

function DeniedStep() {
  return (
    <div className="px-6 py-10 text-center">
      <Ban size={40} className="mx-auto text-rose-500 mb-3" aria-hidden="true" />
      <h2 id="welcome-title" className="text-lg font-semibold text-white mb-2">
        {t('welcome.denied.title')}
      </h2>
      <p className="text-sm text-gray-400 max-w-sm mx-auto leading-relaxed">
        {t('welcome.denied.body')}
      </p>
    </div>
  );
}
