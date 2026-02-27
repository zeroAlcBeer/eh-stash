import React from 'react';

const NS_COLORS = {
    language: 'bg-rose-900/60 text-rose-300 ring-rose-500/30',
    parody: 'bg-orange-900/60 text-orange-300 ring-orange-500/30',
    character: 'bg-emerald-900/60 text-emerald-300 ring-emerald-500/30',
    group: 'bg-blue-900/60 text-blue-300 ring-blue-500/30',
    artist: 'bg-purple-900/60 text-purple-300 ring-purple-500/30',
    male: 'bg-stone-800/60 text-stone-300 ring-stone-500/30',
    female: 'bg-pink-900/60 text-pink-300 ring-pink-500/30',
    misc: 'bg-zinc-800/60 text-zinc-300 ring-zinc-500/30',
};

const TagBadge = ({ namespace, value, onClick }) => {
    const colors = NS_COLORS[namespace] || NS_COLORS.misc;
    return (
        <button
            type="button"
            onClick={onClick}
            className={`inline-flex items-center px-1.5 py-0.5 rounded text-[11px] font-medium ring-1 transition-opacity hover:opacity-80 ${colors}`}
        >
            <span className="opacity-50 mr-0.5">{namespace}:</span>{value}
        </button>
    );
};

export default TagBadge;
