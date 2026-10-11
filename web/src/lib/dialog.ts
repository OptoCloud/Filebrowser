import type { Attachment } from 'svelte/attachments';

/**
 * Attachment for a <dialog> that is shown with showModal() for as long as it
 * is mounted: top layer, inert background, focus trap, and focus returned to
 * the opener afterwards. Escape (the cancel event) and any close the browser
 * forces call onclose instead, so the owner's state stays the source of truth.
 *
 * Pass a closure that reads the callback lazily (`modal(() => onclose())`), so
 * the attachment is not re-created when the callback's identity changes.
 */
export function modal(onclose: () => void): Attachment<HTMLDialogElement> {
  return (dlg) => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    let mounted = true;
    const cancel = (e: Event) => {
      e.preventDefault();
      onclose();
    };
    // Browsers may close without a cancelable cancel event (e.g. Escape pressed repeatedly).
    const close = () => {
      if (mounted) onclose();
    };
    dlg.addEventListener('cancel', cancel);
    dlg.addEventListener('close', close);
    dlg.showModal();
    return () => {
      mounted = false;
      dlg.removeEventListener('cancel', cancel);
      dlg.removeEventListener('close', close);
      if (dlg.open) dlg.close();
      if (opener?.isConnected) opener.focus();
    };
  };
}
