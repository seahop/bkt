import { useState } from 'react';
import { X } from 'lucide-react';
import { addSSOGroupNames, SSO_GROUP_MAX_LEN } from '../utils/ssoGroups';

// Chip input for identity-provider group names linked to a bkt group. Names
// are committed on Enter, comma, blur or paste (comma/newline separated) and
// de-duplicated case-insensitively, matching the server's rules.
export default function SSOGroupsInput({
  value,
  onChange,
  disabled,
  id,
}: {
  value: string[];
  onChange: (next: string[]) => void;
  disabled?: boolean;
  id?: string;
}) {
  const [draft, setDraft] = useState('');

  const commit = (raw: string) => {
    if (raw.trim()) onChange(addSSOGroupNames(value, raw));
    setDraft('');
  };

  return (
    <div className="input flex flex-wrap items-center gap-1.5 py-1.5! focus-within:ring-2 focus-within:ring-blue-500/60 focus-within:border-blue-500/50">
      {value.map((name) => (
        <span key={name.toLowerCase()} className="badge-purple max-w-full">
          <span className="truncate">{name}</span>
          <button
            type="button"
            onClick={() => onChange(value.filter((n) => n !== name))}
            disabled={disabled}
            className="ml-0.5 opacity-70 hover:opacity-100"
            aria-label={`Remove ${name}`}
          >
            <X className="w-3 h-3" />
          </button>
        </span>
      ))}
      <input
        id={id}
        type="text"
        value={draft}
        disabled={disabled}
        maxLength={SSO_GROUP_MAX_LEN}
        onChange={(e) => {
          const v = e.target.value;
          if (v.includes(',')) commit(v);
          else setDraft(v);
        }}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault();
            commit(draft);
          } else if (e.key === 'Backspace' && draft === '' && value.length > 0) {
            onChange(value.slice(0, -1));
          }
        }}
        onBlur={() => commit(draft)}
        onPaste={(e) => {
          const text = e.clipboardData.getData('text');
          if (/[,\n]/.test(text)) {
            e.preventDefault();
            commit(draft + text);
          }
        }}
        className="flex-1 min-w-32 bg-transparent outline-hidden text-sm text-dark-text placeholder:text-dark-textMuted"
        placeholder={value.length === 0 ? 'e.g. engineering, platform-team' : 'Add another…'}
      />
    </div>
  );
}
